// Package testutil provides a mock SAP AI Core HTTP server and a test proxy helper
// for integration testing without real SAP credentials.
package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MockSAPServer is an httptest.Server that simulates SAP AI Core endpoints.
// DeploymentURLs are set to the mock server's own URL so the proxy can resolve them.
type MockSAPServer struct {
	*httptest.Server
	// RequestLog records the method+path of every request received.
	RequestLog []string
	baseURL    *string
}

// NewMockSAPServer starts a mock SAP AI Core server. Call Close() when done.
func NewMockSAPServer(t *testing.T) *MockSAPServer {
	t.Helper()
	m := &MockSAPServer{baseURL: new(string)}
	mux := http.NewServeMux()
	m.registerHandlers(t, mux)
	m.Server = httptest.NewServer(mux)
	*m.baseURL = m.Server.URL
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
		base := *m.baseURL
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

		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		streaming, _ := reqBody["stream"].(bool)

		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"index\":0}]}\n\n")
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"!\"},\"index\":0}]}\n\n")
			fmt.Fprintf(w, "data: [DONE]\n\n")
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
		})
	})

	// OpenAI responses POST (create)
	mux.HandleFunc("/v2/inference/deployments/dep-openai-001/responses", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     "resp-mock-001",
				"object": "response",
				"status": "completed",
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
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 8},
		})
	})

	// Anthropic invoke-with-response-stream (streaming)
	mux.HandleFunc("/v2/inference/deployments/dep-anthropic-001/invoke-with-response-stream", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"anthropic--claude-3.5-sonnet\",\"usage\":{\"input_tokens\":10}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello!\"}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})

	// Gemini generateContent / streamGenerateContent (wildcard via prefix)
	mux.HandleFunc("/v2/inference/deployments/dep-gemini-001/models/", func(w http.ResponseWriter, r *http.Request) {
		m.log(r)
		m.validateAuth(t, w, r)

		isStream := strings.Contains(r.URL.Path, "streamGenerateContent")
		w.Header().Set("Content-Type", "application/json")
		if isStream {
			w.Header().Set("Transfer-Encoding", "chunked")
			fmt.Fprintf(w, `[{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello from mock Gemini streaming!"}]},"finishReason":"STOP"}]}]`)
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
