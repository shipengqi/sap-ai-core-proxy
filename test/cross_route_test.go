package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// TestCrossRoute_ClaudeViaOpenAISurface tests that a Claude model can be used via
// POST /openai/v1/chat/completions, receiving an OpenAI-format response.
func TestCrossRoute_ClaudeViaOpenAISurface(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	var proxyURL string
	var cleanup func()
	if hasReal {
		proxy, err := testutil.NewTestProxy("")
		if err != nil {
			t.Fatalf("start real proxy: %v", err)
		}
		proxyURL, cleanup = proxy.URL, proxy.Close
	} else {
		mock := testutil.NewMockSAPServer(t)
		proxy, err := testutil.NewTestProxy(mock.URL)
		if err != nil {
			t.Fatalf("start mock proxy: %v", err)
		}
		proxyURL, cleanup = proxy.URL, func() { proxy.Close(); mock.Close() }
	}
	defer cleanup()

	// Use the mock's Claude deployment name (claude-3-5-sonnet-latest normalises to anthropic--claude-3.5-sonnet)
	reqBody := `{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"say hi"}],"max_tokens":20}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Response must be in OpenAI format.
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		t.Error("expected non-empty choices[0].message.content in OpenAI-format response")
	}
}

// TestCrossRoute_ClaudeViaOpenAISurface_StreamingRejected tests that streaming
// Claude via the OpenAI surface returns a clear 400.
func TestCrossRoute_ClaudeViaOpenAISurface_StreamingRejected(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping streaming-rejection test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	reqBody := `{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for streaming Claude via OpenAI surface, got %d: %s", resp.StatusCode, body)
	}
}

// TestCrossRoute_GPTViaAnthropicSurface tests that a GPT model can be used via
// POST /anthropic/v1/messages, receiving an Anthropic-format response.
func TestCrossRoute_GPTViaAnthropicSurface(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	var proxyURL string
	var cleanup func()
	if hasReal {
		proxy, err := testutil.NewTestProxy("")
		if err != nil {
			t.Fatalf("start real proxy: %v", err)
		}
		proxyURL, cleanup = proxy.URL, proxy.Close
	} else {
		mock := testutil.NewMockSAPServer(t)
		proxy, err := testutil.NewTestProxy(mock.URL)
		if err != nil {
			t.Fatalf("start mock proxy: %v", err)
		}
		proxyURL, cleanup = proxy.URL, func() { proxy.Close(); mock.Close() }
	}
	defer cleanup()

	reqBody := `{"model":"gpt-4o","max_tokens":20,"messages":[{"role":"user","content":"say hi"}]}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Response must be in Anthropic format.
	var result struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Type != "message" {
		t.Errorf("expected type=message in Anthropic-format response, got %q", result.Type)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty content[0].text in Anthropic-format response")
	}
}

// TestCrossRoute_GPTViaAnthropicSurface_StreamingRejected tests that streaming
// GPT via the Anthropic surface returns a clear 400.
func TestCrossRoute_GPTViaAnthropicSurface_StreamingRejected(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping streaming-rejection test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	reqBody := `{"model":"gpt-4o","max_tokens":20,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for streaming GPT via Anthropic surface, got %d: %s", resp.StatusCode, body)
	}
}
