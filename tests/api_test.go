package apitest

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// fakeSAPMux builds an http.Handler that mimics the SAP AI Core endpoints needed by tests.
func fakeSAPMux() http.Handler {
	mux := http.NewServeMux()

	// OAuth token
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":3600,"token_type":"bearer"}`)
	})

	// Deployments list — two running deployments: claude-4.6-sonnet and claude-4.7-opus
	mux.HandleFunc("/v2/lm/deployments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"resources":[
			{"id":"d-sonnet","status":"RUNNING","targetStatus":"RUNNING",
			 "details":{"resources":{"backend_details":{"model":{"name":"anthropic--claude-4.6-sonnet","version":"1"}}}}},
			{"id":"d-opus47","status":"RUNNING","targetStatus":"RUNNING",
			 "details":{"resources":{"backend_details":{"model":{"name":"anthropic--claude-4.7-opus","version":"1"}}}}}
		]}`)
	})

	// Converse non-stream — shared by both deployments
	for _, id := range []string{"d-sonnet", "d-opus47"} {
		id := id
		mux.HandleFunc("/v2/inference/deployments/"+id+"/converse", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"Hello!"}]}},"usage":{"inputTokens":10,"outputTokens":5},"stopReason":"end_turn"}`)
		})
		mux.HandleFunc("/v2/inference/deployments/"+id+"/converse-stream", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			lines := []string{
				`{"messageStart":{"usage":{"inputTokens":10}}}`,
				`{"contentBlockStart":{"contentBlockIndex":0,"start":{}}}`,
				`{"contentBlockDelta":{"contentBlockIndex":0,"delta":{"text":"Hello!"}}}`,
				`{"contentBlockStop":{"contentBlockIndex":0}}`,
				`{"messageStop":{"stopReason":"end_turn"}}`,
				`{"metadata":{"usage":{"inputTokens":10,"outputTokens":5}}}`,
			}
			for _, l := range lines {
				_, _ = io.WriteString(w, l+"\n")
				if ok {
					flusher.Flush()
				}
			}
		})
	}

	return mux
}

// newTestProxy creates a fully wired proxy server backed by a fake SAP server.
func newTestProxy(t *testing.T) *httptest.Server {
	t.Helper()

	fakeSAP := httptest.NewServer(fakeSAPMux())
	t.Cleanup(fakeSAP.Close)

	auth := sapclient.NewAuthManager("client-id", "client-secret", fakeSAP.URL, fakeSAP.URL, "default")
	client := sapclient.NewSapClient(auth)
	dm := sapclient.NewDeploymentManager(auth)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.RegisterAll(r, &router.Deps{
		SapClient:         client,
		DeploymentManager: dm,
	})

	proxy := httptest.NewServer(r)
	t.Cleanup(proxy.Close)
	return proxy
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func mustReadBody(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ---- Tests ----

func TestHealth(t *testing.T) {
	proxy := newTestProxy(t)

	resp, err := http.Get(proxy.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", m["status"])
	}
}

func TestAnthropicMessages_UnknownModel(t *testing.T) {
	proxy := newTestProxy(t)

	body := `{"model":"unknown-model-xyz","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`
	resp := post(t, proxy.URL+"/anthropic/v1/messages", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b := mustReadBody(t, resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, b)
	}
}

func TestAnthropicMessages_NonStream(t *testing.T) {
	proxy := newTestProxy(t)

	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`
	resp := post(t, proxy.URL+"/anthropic/v1/messages", body)
	defer resp.Body.Close()

	b := mustReadBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, b)
	}
	if m["type"] != "message" {
		t.Errorf("expected type=message, got %v", m["type"])
	}
	content, _ := m["content"].([]any)
	if len(content) == 0 {
		t.Errorf("expected non-empty content, got %v", m["content"])
	}
}

func TestAnthropicMessages_Stream(t *testing.T) {
	proxy := newTestProxy(t)

	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":10,"stream":true}`
	resp := post(t, proxy.URL+"/anthropic/v1/messages", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b := mustReadBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "message_start") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected SSE event containing 'message_start', got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestOpenAIChatCompletions_NonStream(t *testing.T) {
	tests := []struct {
		name  string
		model string
	}{
		{"alias", "claude-sonnet-4-6"},
		{"sap-name", "anthropic--claude-4.6-sonnet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTestProxy(t)

			body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"hello"}]}`
			resp := post(t, proxy.URL+"/openai/v1/chat/completions", body)
			defer resp.Body.Close()

			b := mustReadBody(t, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
			}

			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("parse response: %v\nbody: %s", err, b)
			}
			choices, _ := m["choices"].([]any)
			if len(choices) == 0 {
				t.Fatalf("expected choices, got none: %s", b)
			}
			choice, _ := choices[0].(map[string]any)
			msg, _ := choice["message"].(map[string]any)
			if msg["content"] == "" || msg["content"] == nil {
				t.Errorf("expected non-empty message.content, got %v", msg["content"])
			}
			usage, _ := m["usage"].(map[string]any)
			if usage["prompt_tokens"] == nil {
				t.Errorf("expected usage.prompt_tokens, got nil")
			}
		})
	}
}

func TestOpenAIChatCompletions_Stream(t *testing.T) {
	proxy := newTestProxy(t)

	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"stream":true}`
	resp := post(t, proxy.URL+"/openai/v1/chat/completions", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b := mustReadBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	scanner := bufio.NewScanner(resp.Body)
	var dataLines []string
	for scanner.Scan() {
		l := scanner.Text()
		if strings.HasPrefix(l, "data: ") && l != "data: [DONE]" {
			dataLines = append(dataLines, l[6:])
		}
	}
	if len(dataLines) == 0 {
		t.Fatal("expected SSE data lines, got none")
	}

	found := false
	for _, raw := range dataLines {
		var chunk map[string]any
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c, _ := choices[0].(map[string]any)
		delta, _ := c["delta"].(map[string]any)
		if content, ok := delta["content"].(string); ok && content != "" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one SSE chunk with delta.content, got:\n%s", strings.Join(dataLines, "\n"))
	}
}

func TestOpenAIChatCompletions_Claude47OpusAlias(t *testing.T) {
	proxy := newTestProxy(t)

	body := `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`
	resp := post(t, proxy.URL+"/openai/v1/chat/completions", body)
	defer resp.Body.Close()

	b := mustReadBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, b)
	}
	choices, _ := m["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected choices, got none: %s", b)
	}
}

func TestModelsList(t *testing.T) {
	proxy := newTestProxy(t)

	resp, err := http.Get(proxy.URL + "/openai/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	b := mustReadBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("parse models list: %v\nbody: %s", err, b)
	}

	wantModels := []string{"anthropic--claude-4.6-sonnet", "anthropic--claude-4.7-opus"}
	idSet := make(map[string]bool, len(result.Data))
	for _, m := range result.Data {
		idSet[m.ID] = true
	}
	for _, want := range wantModels {
		if !idSet[want] {
			t.Errorf("expected %q in models list, got: %v", want, result.Data)
		}
	}
}
