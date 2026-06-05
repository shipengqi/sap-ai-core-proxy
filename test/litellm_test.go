package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupLiteLLM(t *testing.T) (proxyURL string, cleanup func()) {
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

func TestLiteLLM_ListModels(t *testing.T) {
	proxyURL, cleanup := setupLiteLLM(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/litellm/v1/models")
	if err != nil {
		t.Fatalf("GET /litellm/v1/models: %v", err)
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

func TestLiteLLM_ModelInfo(t *testing.T) {
	proxyURL, cleanup := setupLiteLLM(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/litellm/v1/model/info")
	if err != nil {
		t.Fatalf("GET /litellm/v1/model/info: %v", err)
	}
	defer resp.Body.Close()

	// Any non-5xx response is acceptable — the orchestration model info endpoint
	// may return various formats depending on SAP AI Core version.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected non-5xx, got %d: %s", resp.StatusCode, body)
	}
}

func TestLiteLLM_ChatCompletions(t *testing.T) {
	proxyURL, cleanup := setupLiteLLM(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}]}`
	resp, err := http.Post(proxyURL+"/litellm/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /litellm/v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		t.Error("expected non-empty choices[0].message.content")
	}
}

func TestLiteLLM_Completions(t *testing.T) {
	proxyURL, cleanup := setupLiteLLM(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}]}`
	resp, err := http.Post(proxyURL+"/litellm/v1/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /litellm/v1/completions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestLiteLLM_Embeddings(t *testing.T) {
	proxyURL, cleanup := setupLiteLLM(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","input":"Hello world"}`
	resp, err := http.Post(proxyURL+"/litellm/v1/embeddings", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /litellm/v1/embeddings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		t.Error("expected non-empty data[0].embedding")
	}
}
