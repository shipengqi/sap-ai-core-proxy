package test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// TestQA_MetricsDisabled asserts the kill switch: with Server.Metrics=false the
// two observation endpoints are not registered, and inference is unaffected.
func TestQA_MetricsDisabled(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()

	proxy, err := newQAProxy(up.URL, false)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	for _, path := range []string{"/metrics", "/stats"} {
		resp, err := http.Get(proxy.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s with metrics disabled: expected 404, got %d", path, resp.StatusCode)
		}
	}

	// Inference must still work, byte for byte.
	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming)
	if status != http.StatusOK {
		t.Fatalf("inference with metrics disabled: expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicBody)) {
		t.Errorf("response bytes changed when metrics were disabled\nwant: %s\ngot:  %s", qaAnthropicBody, body)
	}

	status, body = qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusOK {
		t.Fatalf("streaming with metrics disabled: expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicSSE)) {
		t.Errorf("stream bytes changed when metrics were disabled\nwant: %q\ngot:  %q", qaAnthropicSSE, body)
	}
}

// TestQA_MetricsEnabledByDefault guards the opposite mistake: the endpoints must
// be reachable when the toggle is on, which is the production default.
func TestQA_MetricsEnabledByDefault(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	for _, path := range []string{"/metrics", "/stats"} {
		resp, err := http.Get(proxy.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: expected 200, got %d", path, resp.StatusCode)
		}
	}
}
