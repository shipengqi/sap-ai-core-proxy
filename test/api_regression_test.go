package test

import (
	"net/http"
	"strings"
	"testing"
)

func TestProxyRegressionResponsesStream(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	up.responsesBody = []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n")
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()
	resp, err := http.Post(proxy.URL+"/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-4o","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Responses stream returned wrong content type: %s", resp.Header.Get("Content-Type"))
	}
}
func TestProxyRegressionStreamHTTPStatus(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	up.streamStatus = 429
	up.streamBody = []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"retry"}}`)
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()
	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", `{"model":"anthropic--claude-3.5-sonnet","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != 429 {
		t.Fatalf("upstream 429 became client %d: %s", status, body)
	}
}
func TestProxyRegressionQwenDuplicateGeneration(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()
	status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", `{"model":"qwen3-32b","stream":true,"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 {
		t.Fatalf("unexpected %d: %s", status, body)
	}
	if n := len(up.bodiesSnapshot()); n != 1 {
		t.Fatalf("one client request generated %d upstream inference requests", n)
	}
}
