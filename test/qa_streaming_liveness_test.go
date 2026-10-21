package test

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// qaFirstEvent is the first SSE frame the gated upstream sends; qaRestEvents is
// everything after it.
const (
	qaFirstEvent = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n" +
		"\n"

	qaRestEvents = "event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":12}}` + "\n" +
		"\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n" +
		"\n"
)

// TestQA_StreamIsNotBufferedByAccounting is the production-incident test: the
// upstream sends one frame and then stalls. If the accounting path buffered the
// response (or forgot to flush), the client would see nothing until the upstream
// finished. The first frame must arrive while the upstream is still open.
func TestQA_StreamIsNotBufferedByAccounting(t *testing.T) {
	up := newQAUpstream(t)
	up.streamChunks = [][]byte{[]byte(qaFirstEvent), []byte(qaRestEvents)}
	up.streamGate = make(chan struct{})
	gateClosed := false
	defer func() {
		if !gateClosed {
			close(up.streamGate)
		}
	}()
	defer up.Close()

	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json",
		strings.NewReader(qaClientBodyStreaming))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}

	type restResult struct {
		body string
		err  error
	}
	firstCh := make(chan string, 1)
	restCh := make(chan restResult, 1)

	go func() {
		br := bufio.NewReader(resp.Body)
		var head strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				restCh <- restResult{err: err}
				return
			}
			head.WriteString(line)
			if line == "\n" {
				break
			}
		}
		firstCh <- head.String()

		rest, err := io.ReadAll(br)
		restCh <- restResult{body: string(rest), err: err}
	}()

	select {
	case got := <-firstCh:
		if got != qaFirstEvent {
			t.Errorf("first frame = %q, want %q", got, qaFirstEvent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first SSE frame did not reach the client while the upstream was still open: the response is being buffered")
	}

	// Release the rest of the stream.
	close(up.streamGate)
	gateClosed = true

	select {
	case res := <-restCh:
		if res.err != nil {
			t.Fatalf("read the rest of the stream: %v", res.err)
		}
		if res.body != qaRestEvents {
			t.Errorf("remaining frames = %q, want %q", res.body, qaRestEvents)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the remaining SSE frames never arrived")
	}

	// The stream was still accounted for in full.
	doc := qaGetStats(t, proxy.URL)
	want := qaTokens{Input: 10, Output: 12}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}
}

// TestQA_OpenAIStreamHeaders pins the SSE response headers on the OpenAI path;
// a missing header breaks Claude Code / SDK streaming just as badly as a body
// change.
func TestQA_OpenAIStreamHeaders(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	const req = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}
