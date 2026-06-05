package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupAnthropic(t *testing.T) (proxyURL string, cleanup func()) {
	t.Helper()
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		proxy, err := testutil.NewTestProxy("")
		if err != nil {
			t.Fatalf("start real proxy: %v", err)
		}
		return proxy.URL, proxy.Close
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	return proxy.URL, func() {
		proxy.Close()
		mock.Close()
	}
}

func TestAnthropic_ListModels(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/anthropic/v1/models")
	if err != nil {
		t.Fatalf("GET /anthropic/v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Data []interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) == 0 {
		t.Error("expected non-empty model list")
	}
}

func TestAnthropic_Messages(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	// claude-3-5-sonnet-latest aliases to anthropic--claude-3.5-sonnet which matches the mock deployment
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Say hi"}]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty content[0].text")
	}
}

func TestAnthropic_Messages_Stream(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"stream": true,
		"messages": [{"role": "user", "content": "Say hi"}]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "event: message_start") {
		t.Errorf("expected 'event: message_start' in stream, got: %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Errorf("expected message_stop event in stream")
	}
}

func TestAnthropic_SystemPromotion(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	// Send system as a message role — proxy should promote it to top-level system field
	// and inject anthropic_version. The mock validates both.
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages system: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	// anthropic_version injection and system promotion are validated inside the mock handler.
}
