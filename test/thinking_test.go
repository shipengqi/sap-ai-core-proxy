package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// TestThinking_AnthropicMessages_UnsupportedModel tests that when a model doesn't support
// thinking, the proxy automatically retries without the thinking parameter and caches the result.
func TestThinking_AnthropicMessages_UnsupportedModel(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()

	// Configure mock to reject thinking parameter
	mock.RejectThinking = true

	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// First request with thinking parameter should succeed after retry
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hi"}],
		"thinking": {"type": "enabled", "budget_tokens": 5000}
	}`

	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after retry, got %d: %s", resp.StatusCode, body)
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
		t.Error("expected non-empty content after retry")
	}

	// Second request should succeed immediately (cached)
	resp2, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages (2nd): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200 on cached request, got %d: %s", resp2.StatusCode, body)
	}

	// Verify no thinking parameter was sent in the second request
	if mock.LastThinkingParam {
		t.Error("expected thinking parameter to be skipped on second request (cached)")
	}
}

// TestThinking_OpenAIChat_UnsupportedModel tests the same for OpenAI chat completions endpoint.
func TestThinking_OpenAIChat_UnsupportedModel(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()

	// Configure mock to reject thinking parameter
	mock.RejectThinking = true

	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// Request with reasoning_effort (converts to thinking)
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hi"}],
		"reasoning_effort": "medium"
	}`

	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after retry, got %d: %s", resp.StatusCode, body)
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
		t.Error("expected non-empty content after retry")
	}

	// Second request should succeed immediately (cached)
	resp2, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions (2nd): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200 on cached request, got %d: %s", resp2.StatusCode, body)
	}

	// Verify no thinking parameter was sent in the second request
	if mock.LastThinkingParam {
		t.Error("expected thinking parameter to be skipped on second request (cached)")
	}
}

// TestThinking_SupportedModel tests that models supporting thinking work normally.
func TestThinking_SupportedModel(t *testing.T) {
	// Clear cache from previous tests
	openai.ClearThinkingCache()

	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()

	// Configure mock to accept thinking parameter
	mock.RejectThinking = false
	mock.LastThinkingParam = false // Reset state

	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// Request with thinking parameter should succeed on first try
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hi"}],
		"thinking": {"type": "enabled", "budget_tokens": 5000}
	}`

	// Need to send request to a fresh proxy instance to avoid cached state
	// from previous tests in the same process
	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	// Verify thinking parameter was sent
	if !mock.LastThinkingParam {
		t.Error("expected thinking parameter to be sent to supporting model")
	}
}

// TestThinking_Streaming_AutoProbe tests that streaming requests automatically probe
// thinking support before starting the stream.
func TestThinking_Streaming_AutoProbe(t *testing.T) {
	// Clear cache from previous tests
	openai.ClearThinkingCache()

	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()

	// Configure mock to reject thinking parameter
	mock.RejectThinking = true

	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	// First streaming request with thinking parameter should:
	// 1. Send probe request (non-streaming) → fail with 400
	// 2. Cache the result
	// 3. Start streaming without thinking → succeed
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hi"}],
		"thinking": {"type": "enabled", "budget_tokens": 5000},
		"stream": true
	}`

	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages (streaming): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after auto-probe, got %d: %s", resp.StatusCode, body)
	}

	// Read streaming response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read streaming response: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty streaming response")
	}

	// Verify thinking is now cached as unsupported
	if !openai.IsThinkingUnsupported("dep-anthropic-001") {
		t.Error("expected deployment to be cached as unsupported after probe")
	}

	// Second streaming request should succeed immediately without probe
	resp2, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages (2nd streaming): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200 on cached streaming request, got %d: %s", resp2.StatusCode, body)
	}
}
