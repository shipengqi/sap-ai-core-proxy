package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func TestAnthropicAutoCacheMarkerNativeAndOpenAI(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		enabled  bool
		wantMark bool
	}{
		{
			name:     "native disabled by default",
			path:     "/anthropic/v1/messages",
			body:     `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			wantMark: false,
		},
		{
			name:     "native enabled",
			path:     "/anthropic/v1/messages",
			body:     `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			enabled:  true,
			wantMark: true,
		},
		{
			name:     "native existing marker untouched",
			path:     "/anthropic/v1/messages",
			body:     `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":null}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			enabled:  true,
			wantMark: false,
		},
		{
			name:     "openai enabled",
			path:     "/openai/v1/chat/completions",
			body:     `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
			enabled:  true,
			wantMark: true,
		},
		{
			name:     "openai existing marker untouched",
			path:     "/openai/v1/chat/completions",
			body:     `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":null}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
			enabled:  true,
			wantMark: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, tc.enabled)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if mock.LastInvokeBody == nil {
				t.Fatal("expected an invoke request")
			}
			var tools []map[string]json.RawMessage
			if err := json.Unmarshal(mock.LastInvokeBody["tools"], &tools); err != nil {
				t.Fatalf("decode upstream tools: %v", err)
			}
			marker, gotMark := tools[len(tools)-1]["cache_control"]
			if gotMark != tc.wantMark {
				t.Errorf("cache marker = %v, want %v; tools=%s", gotMark, tc.wantMark, mock.LastInvokeBody["tools"])
			}
			if gotMark {
				var value map[string]string
				if err := json.Unmarshal(marker, &value); err != nil {
					t.Fatalf("decode cache marker: %v", err)
				}
				if value["type"] != "ephemeral" || len(value) != 1 {
					t.Errorf("cache marker = %s, want {type: ephemeral}", marker)
				}
			}
		})
	}
}

func TestAnthropicAutoCacheMarkerSkipsRequestsWithoutToolsAndNonClaude(t *testing.T) {
	t.Run("enabled Claude request without tools remains unmarked", func(t *testing.T) {
		mock := testutil.NewMockSAPServer(t)
		defer mock.Close()
		proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
		if err != nil {
			t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
		}
		defer proxy.Close()

		body := `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"system":"keep this string","messages":[{"role":"user","content":"keep this message"}]}`
		response, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		if _, ok := mock.LastInvokeBody["tools"]; ok {
			t.Errorf("tools unexpectedly added: %s", mock.LastInvokeBody["tools"])
		}
		if got := string(mock.LastInvokeBody["system"]); got != `"keep this string"` {
			t.Errorf("system = %s, want unchanged string", got)
		}
		if got := string(mock.LastInvokeBody["messages"]); got != `[{"role":"user","content":"keep this message"}]` {
			t.Errorf("messages = %s, want unchanged message", got)
		}
	})

	t.Run("enabled non Claude OpenAI request remains unmarked", func(t *testing.T) {
		mock := testutil.NewMockSAPServer(t)
		defer mock.Close()
		proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
		if err != nil {
			t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
		}
		defer proxy.Close()

		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
		response, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(mock.LastOpenAIChatBody["tools"], &tools); err != nil {
			t.Fatalf("decode upstream tools: %v", err)
		}
		if _, marked := tools[len(tools)-1]["cache_control"]; marked {
			t.Errorf("non-Claude tool unexpectedly marked: %s", mock.LastOpenAIChatBody["tools"])
		}
	})
}
