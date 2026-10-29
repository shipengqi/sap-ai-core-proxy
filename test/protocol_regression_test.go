package test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func protocolProxy(t *testing.T, inference http.HandlerFunc) (*httptest.Server, *sapclient.DeploymentManager) {
	t.Helper()
	var base string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			writeJSON(w, 200, map[string]any{"access_token": "test", "expires_in": 3600})
		case "/v2/lm/deployments":
			writeJSON(w, 200, map[string]any{"resources": []any{
				map[string]any{"id": "a", "status": "RUNNING", "modelName": "gpt-4o", "deploymentUrl": base + "/a"},
				map[string]any{"id": "b", "status": "RUNNING", "modelName": "gpt-4o", "deploymentUrl": base + "/b"},
			}})
		default:
			inference(w, r)
		}
	}))
	base = up.URL
	t.Cleanup(up.Close)
	auth := sapclient.NewAuthManager(base, "test", "test")
	client := sapclient.NewClient(base, "test", auth)
	deps := sapclient.NewDeploymentManager(client)
	proxy := httptest.NewServer(router.New(client, deps, false, true, false))
	t.Cleanup(proxy.Close)
	return proxy, deps
}

func TestResponsesStreamingFirstFrameAndUsage(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	proxy, _ := protocolProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":4}}}\n\n")
	})
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(proxy.URL+"/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-4o","stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatal("first frame blocked until completion:", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(resp.Header)
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "response.created") {
		t.Fatalf("first event missing: %q %v", line, err)
	}
	release()
	_, _ = io.ReadAll(reader)
	// The request record commits as the server handler finishes; GET /stats follows it.
	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Tokens.Input != 9 || doc.Totals.Tokens.Output != 4 {
		t.Fatalf("Responses streaming usage lost: %+v", doc.Totals)
	}
}

func TestResponsesAffinityAndFailedDelete(t *testing.T) {
	var deleteAttempts atomic.Int32
	var previousPath atomic.Value
	proxy, deps := protocolProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if deleteAttempts.Add(1) == 1 {
				writeJSON(w, 503, map[string]any{"error": "temporary"})
				return
			}
			writeJSON(w, 200, map[string]any{"deleted": true})
			return
		}
		if r.Method == http.MethodPost {
			var raw map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&raw)
			if raw["previous_response_id"] != nil {
				previousPath.Store(r.URL.Path)
			}
		}
		writeJSON(w, 200, map[string]any{"id": "r1", "object": "response", "status": "completed", "output": []any{}})
	})
	status, body := qaPost(t, proxy.URL+"/openai/v1/responses", `{"model":"gpt-4o","input":"hello"}`)
	if status != 200 {
		t.Fatal(status, string(body))
	}
	// Discovery now selects b, but a owns the previous response.
	all, err := deps.ListAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deps.MarkUnhealthy(all[0].DeployedURL)
	status, body = qaPost(t, proxy.URL+"/openai/v1/responses", `{"model":"gpt-4o","previous_response_id":"r1","input":"continue"}`)
	if status != 200 || previousPath.Load() != "/a/responses" {
		t.Fatalf("lost affinity: %d %s path=%v", status, body, previousPath.Load())
	}
	req, _ := http.NewRequest(http.MethodDelete, proxy.URL+"/openai/v1/responses/r1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal(resp.StatusCode)
	}
	resp, err = http.Get(proxy.URL + "/openai/v1/responses/r1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("failed DELETE removed route: %d", resp.StatusCode)
	}
}

func TestResponsesBridgeFailureAndUnsupportedParameters(t *testing.T) {
	var responses atomic.Int32
	proxy, _ := protocolProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "To use function tools, use /v1/responses"}})
			return
		}
		responses.Add(1)
		writeJSON(w, 200, map[string]any{"id": "r", "status": "failed", "error": map[string]any{"message": "generation failed"}})
	})
	request := `{"model":"gpt-4o","messages":[{"role":"user","content":"use f"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", request)
	if status != 502 || !strings.Contains(string(body), `"error"`) {
		t.Fatalf("failed response masked: %d %s", status, body)
	}
	status, body = qaPost(t, proxy.URL+"/openai/v1/chat/completions", strings.TrimSuffix(request, "}")+`,"stop":"END"}`)
	if status != 400 || responses.Load() != 1 {
		t.Fatalf("unsupported stop silently dropped: %d %s calls=%d", status, body, responses.Load())
	}
}

func TestAnthropicEntryUsesResponsesFallback(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	t.Cleanup(mock.Close)
	mock.ToolsRequireResponses = true
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Close)
	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", `{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"use f"}],"tools":[{"name":"get_time","input_schema":{"type":"object"}}]}`)
	if status != 200 || !strings.Contains(string(body), `"type":"tool_use"`) {
		t.Fatalf("surface bypassed fallback: %d %s", status, body)
	}
}

func TestClaudeRejectsConflictingTokenBudget(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	t.Cleanup(mock.Close)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Close)
	status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", `{"model":"claude-3-5-sonnet-latest","max_completion_tokens":64,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if status != 400 || mock.LastInvokeBody != nil {
		t.Fatalf("explicit limit was overridden: %d %s upstream=%v", status, body, mock.LastInvokeBody)
	}
}

func TestRateLimitHeadersSurviveBothResponseModes(t *testing.T) {
	proxy, _ := protocolProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("X-Request-ID", "upstream-request")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "0")
		writeJSON(w, 429, map[string]any{"error": map[string]string{"message": "rate limited"}})
	})
	for _, streaming := range []bool{false, true} {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(streaming) + `}`
		resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "7" || resp.Header.Get("X-Request-ID") != "upstream-request" || len(resp.Header.Values("Retry-After")) != 1 {
			t.Fatal(resp.StatusCode, resp.Header)
		}
	}
}

func TestChatStreamingDeploymentFailoverBeforeFirstFrame(t *testing.T) {
	var calls atomic.Int32
	proxy, _ := protocolProxy(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasPrefix(r.URL.Path, "/a/") {
			w.Header().Set("Retry-After", "30")
			writeJSON(w, 410, map[string]string{"error": "Gone"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"b\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || calls.Load() != 2 || !strings.Contains(string(body), "hello") || strings.Contains(string(body), "Gone") || resp.Header.Get("Retry-After") != "" {
		t.Fatal(resp.StatusCode, calls.Load(), string(body), resp.Header)
	}
}
