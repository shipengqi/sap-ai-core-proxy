// Package testutil provides a mock SAP AI Core HTTP server and a test proxy helper
// for integration testing without real SAP credentials.
package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// MockSAPServer is an httptest.Server that simulates SAP AI Core endpoints.
// DeploymentURLs are set to the mock server's own URL so the proxy can resolve them.
type MockSAPServer struct {
	*httptest.Server
	RequestLog []string
	baseURL    string

	// RejectThinking controls whether the mock rejects requests with thinking parameters.
	// When true, returns 400 with "adaptive thinking is not supported on this model".
	RejectThinking bool

	// LastThinkingParam tracks whether the last request contained a thinking parameter.
	LastThinkingParam bool

	// LastInvokeBody captures the most recent body posted to the Anthropic /invoke endpoint.
	LastInvokeBody map[string]json.RawMessage

	// LastInvokeStreamBody captures the most recent body posted to
	// /invoke-with-response-stream.
	LastInvokeStreamBody map[string]json.RawMessage

	// ToolCallResponse makes the Anthropic mock answer with a tool_use block
	// (or tool_use SSE events) instead of plain text.
	ToolCallResponse bool

	// StreamingErrorStatus, when non-zero, makes the Anthropic streaming endpoint
	// fail with that HTTP status and a JSON error body.
	StreamingErrorStatus int

	// LastOpenAIChatBody captures the most recent body posted to the OpenAI
	// compatible /chat/completions endpoint.
	LastOpenAIChatBody map[string]json.RawMessage

	// OpenAIToolCallResponse makes the OpenAI-compatible mock answer with tool_calls
	// instead of plain text.
	OpenAIToolCallResponse bool

	// mu guards the bookkeeping fields below. The mock is driven by concurrent
	// integration tests — the cache-diagnostics suite fires fifty parallel
	// requests at it — and -race flags the unsynchronised appends, so every
	// mutation of Last*/RequestLog goes through this mutex.
	mu sync.Mutex
}

// NewMockSAPServer starts a mock SAP AI Core server. Call Close() when done.
func NewMockSAPServer(t *testing.T) *MockSAPServer {
	t.Helper()
	m := &MockSAPServer{}
	// Create the server first so baseURL is available inside handlers.
	mux := http.NewServeMux()
	m.Server = httptest.NewServer(mux)
	m.baseURL = m.URL
	m.registerHandlers(t, mux)
	return m
}

func (m *MockSAPServer) registerHandlers(t *testing.T, mux *http.ServeMux) {
	// OAuth token endpoint
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "mock-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	})

	// Deployments list — returns deploymentUrl pointing to this mock server.
	mux.HandleFunc("/v2/lm/deployments", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		base := m.baseURL
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"resources": []map[string]interface{}{
				{
					"id":            "dep-openai-001",
					"status":        "RUNNING",
					"modelName":     "gpt-4o",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-openai-001",
				},
				{
					"id":            "dep-anthropic-001",
					"status":        "RUNNING",
					"modelName":     "anthropic--claude-3.5-sonnet",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-anthropic-001",
				},
				{
					"id":            "dep-anthropic-46-001",
					"status":        "RUNNING",
					"modelName":     "anthropic--claude-4.6-sonnet",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-anthropic-001",
				},
				{
					"id":            "dep-anthropic-48-001",
					"status":        "RUNNING",
					"modelName":     "anthropic--claude-4.8-opus",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-anthropic-001",
				},
				{
					"id":            "dep-anthropic-45-haiku-001",
					"status":        "RUNNING",
					"modelName":     "anthropic--claude-4.5-haiku",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-anthropic-001",
				},
				{
					"id":            "dep-gemini-001",
					"status":        "RUNNING",
					"modelName":     "gemini-1.5-pro",
					"scenarioId":    "foundation-models",
					"deploymentUrl": base + "/v2/inference/deployments/dep-gemini-001",
				},
				{
					"id":            "dep-orch-001",
					"status":        "RUNNING",
					"modelName":     "orchestration",
					"scenarioId":    "orchestration",
					"deploymentUrl": base + "/v2/inference/deployments/dep-orch-001",
				},
			},
		})
	})

	// OpenAI chat completions
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		m.validateResourceGroup(t, w, r)

		bodyBytes, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		_ = json.Unmarshal(bodyBytes, &reqBody)
		var rawBody map[string]json.RawMessage
		_ = json.Unmarshal(bodyBytes, &rawBody)
		m.mu.Lock()
		m.LastOpenAIChatBody = rawBody
		m.mu.Unlock()
		streaming, _ := reqBody["stream"].(bool)

		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"index\":0}]}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"!\"},\"index\":0}]}\n\n")
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			return
		}

		if m.OpenAIToolCallResponse {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     "chatcmpl-mock-tool",
				"object": "chat.completion",
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"message": map[string]interface{}{
							"role":    "assistant",
							"content": nil,
							"tool_calls": []map[string]interface{}{
								{
									"id":   "call_mock_1",
									"type": "function",
									"function": map[string]string{
										"name":      "get_weather",
										"arguments": `{"city":"Berlin"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-mock",
			"object": "chat.completion",
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"message":       map[string]string{"role": "assistant", "content": "Hello from mock!"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	})

	// OpenAI embeddings
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/embeddings", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"object": "embedding", "embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
			},
			"model": "gpt-4o",
			"usage": map[string]int{"prompt_tokens": 7, "total_tokens": 7},
		})
	})

	// OpenAI image generation. Like the real endpoint it reports no token usage:
	// image models are billed per image.
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/images/generations", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"created": 1,
			"data":    []map[string]string{{"url": "https://example.invalid/image.png"}},
		})
	})

	// OpenAI responses POST (create)
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/responses", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			// The Responses API reports usage as input_tokens/output_tokens, with
			// the cached part under input_tokens_details.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     "resp-mock-001",
				"object": "response",
				"status": "completed",
				"usage": map[string]interface{}{
					"input_tokens":         30,
					"output_tokens":        7,
					"total_tokens":         37,
					"input_tokens_details": map[string]int{"cached_tokens": 5},
				},
			})
			return
		}
		http.NotFound(w, r)
	})

	// OpenAI responses GET/DELETE by ID
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/responses/resp-mock-001", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "resp-mock-001",
			"object": "response",
			"status": "completed",
		})
	})

	// Anthropic invoke (non-streaming)
	mux.HandleFunc("/v2/inference/deployments/dep-anthropic-001/invoke", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		m.validateResourceGroup(t, w, r)

		var reqBody map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		if _, ok := reqBody["anthropic_version"]; !ok {
			t.Errorf("anthropic /invoke: missing anthropic_version in forwarded body")
		}

		// Track whether thinking parameter is present
		_, hasThinking := reqBody["thinking"]
		m.mu.Lock()
		m.LastInvokeBody = reqBody
		m.LastThinkingParam = hasThinking
		m.mu.Unlock()

		// If RejectThinking is true and thinking param exists, return 400
		if m.RejectThinking && hasThinking {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"message": "adaptive thinking is not supported on this model",
			})
			return
		}

		if m.ToolCallResponse {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":   "msg-mock-tool-001",
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "toolu_mock_1", "name": "get_weather", "input": map[string]string{"city": "Berlin"}},
				},
				"model":       "anthropic--claude-3.5-sonnet",
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 10, "output_tokens": 8},
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "msg-mock-001",
			"type": "message",
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello from mock Anthropic!"},
			},
			"model":       "anthropic--claude-3.5-sonnet",
			"stop_reason": "end_turn",
			// Cache counters are reported separately from input_tokens by
			// Anthropic; the usage tests rely on these exact numbers.
			"usage": map[string]int{
				"input_tokens":                10,
				"output_tokens":               8,
				"cache_read_input_tokens":     4,
				"cache_creation_input_tokens": 2,
			},
		})
	})

	// Anthropic invoke-with-response-stream (streaming)
	mux.HandleFunc("/v2/inference/deployments/dep-anthropic-001/invoke-with-response-stream", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)

		var reqBody map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		m.mu.Lock()
		m.LastInvokeStreamBody = reqBody
		m.mu.Unlock()

		if m.StreamingErrorStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(m.StreamingErrorStatus)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"message": "mock upstream streaming failure",
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"anthropic--claude-3.5-sonnet\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":4,\"cache_creation_input_tokens\":2}}}\n\n")

		if m.ToolCallResponse {
			_, _ = fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_mock_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n")
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n")
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Berlin\\\"}\"}}\n\n")
			_, _ = fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":15}}\n\n")
			_, _ = fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		_, _ = fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello!\"}}\n\n")
		_, _ = fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":3}}\n\n")
		_, _ = fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})

	// Gemini generateContent / streamGenerateContent (wildcard via prefix)
	mux.HandleFunc("/v2/inference/deployments/dep-gemini-001/models/", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)

		isStream := strings.Contains(r.URL.Path, "streamGenerateContent")
		w.Header().Set("Content-Type", "application/json")
		if isStream {
			w.Header().Set("Transfer-Encoding", "chunked")
			_, _ = fmt.Fprintf(w, `[{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello from mock Gemini streaming!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6,"totalTokenCount":18}}]`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"role":  "model",
						"parts": []map[string]string{{"text": "Hello from mock Gemini!"}},
					},
					"finishReason": "STOP",
				},
			},
			// Gemini reports prompt/candidate token counts under usageMetadata.
			"usageMetadata": map[string]int{
				"promptTokenCount":     12,
				"candidatesTokenCount": 6,
				"totalTokenCount":      18,
			},
		})
	})

	// SAP Orchestration /v2/completion
	mux.HandleFunc("/v2/inference/deployments/dep-orch-001/v2/completion", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)

		var reqBody map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		if _, ok := reqBody["config"]; !ok {
			t.Errorf("litellm /v2/completion: missing 'config' field in orchestration body")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"final_result": map[string]interface{}{
				"id":     "orch-mock-001",
				"object": "chat.completion",
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"message":       map[string]string{"role": "assistant", "content": "Hello from mock Orchestration!"},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 8, "total_tokens": 18},
			},
		})
	})
}

func (m *MockSAPServer) log(r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RequestLog = append(m.RequestLog, r.Method+" "+r.URL.Path)
}

func (m *MockSAPServer) validateAuth(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("missing or invalid Authorization header on %s %s: %q", r.Method, r.URL.Path, auth)
	}
}

func (m *MockSAPServer) validateResourceGroup(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if rg := r.Header.Get("AI-Resource-Group"); rg == "" {
		t.Errorf("missing AI-Resource-Group header on %s %s", r.Method, r.URL.Path)
	}
}
