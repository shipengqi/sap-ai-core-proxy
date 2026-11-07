package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// Always use a local upstream: these protocol regressions must not depend on
// credentials, deployed models, or a particular SAP service version.
func orchestrationProxy(t *testing.T, completion http.HandlerFunc) string {
	t.Helper()
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":3600}`)
	})
	mux.HandleFunc("/v2/lm/deployments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resources": []map[string]any{{
			"id": "orch", "status": "RUNNING", "scenarioId": "orchestration",
			"modelName": "orchestration", "deploymentUrl": upstream.URL + "/orch",
		}}})
	})
	mux.HandleFunc("/orch/v2/completion", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("AI-Resource-Group") != "test-group" {
			t.Errorf("unsigned or invalid Orchestration request: %s %v", r.Method, r.Header)
		}
		completion(w, r)
	})
	proxy, err := testutil.NewTestProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Close)
	return proxy.URL
}

func postOrchestration(t *testing.T, proxyURL, body string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Post(proxyURL+"/litellm/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(data)
}

func TestOrchestrationStreamsFinalResult(t *testing.T) {
	for _, model := range []string{"gpt-6-luna", "gpt-6-astra"} {
		t.Run(model, func(t *testing.T) {
			proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Config struct {
						Stream  struct{ Enabled bool }
						Modules struct {
							PromptTemplating struct {
								Model  struct{ Name string }
								Prompt struct{ Template []json.RawMessage }
							} `json:"prompt_templating"`
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if !req.Config.Stream.Enabled || req.Config.Modules.PromptTemplating.Model.Name != model || len(req.Config.Modules.PromptTemplating.Prompt.Template) != 1 {
					t.Errorf("incorrect streaming Orchestration request: %+v", req)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"request_id\":\"req\",\"intermediate_results\":{\"templating\":[{\"content\":\"private prompt\"}]},\"final_result\":{\"id\":\"\",\"object\":\"\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"}}]}}\n\n")
				_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"chatcmpl-orch\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":\"\"}]}}\n\n")
				_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"chatcmpl-orch\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}}\n\ndata: [DONE]\n\n")
			})
			status, headers, body := postOrchestration(t, proxyURL, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":true}`, model))
			if status != 200 || !strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("expected SSE, status=%d headers=%v body=%s", status, headers, body)
			}
			if !strings.Contains(body, `"content":"Hello"`) || !strings.Contains(body, `"finish_reason":null`) || !strings.Contains(body, `"finish_reason":"stop"`) || strings.Count(body, "data: [DONE]") != 1 {
				t.Fatal(body)
			}
			for _, forbidden := range []string{"private prompt", "intermediate_results", "final_result", `"id":""`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("internal Orchestration data leaked: %s", body)
				}
			}
		})
	}
}

func TestOrchestrationForwardsOpenCodeParameters(t *testing.T) {
	proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Config struct {
				Modules struct {
					PromptTemplating struct {
						Model  struct{ Params map[string]json.RawMessage }
						Prompt struct {
							Tools          []json.RawMessage
							ResponseFormat json.RawMessage `json:"response_format"`
						}
					} `json:"prompt_templating"`
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		params := req.Config.Modules.PromptTemplating.Model.Params
		for key, want := range map[string]string{"max_completion_tokens": "64", "temperature": "0.2", "reasoning_effort": `"high"`, "tool_choice": `"auto"`, "parallel_tool_calls": "true", "stream_options": `{"include_usage":true}`} {
			if string(params[key]) != want {
				t.Errorf("params[%s]=%s, want %s", key, params[key], want)
			}
		}
		if _, ok := params["max_tokens"]; ok {
			t.Error("max_tokens must be translated to max_completion_tokens")
		}
		if len(req.Config.Modules.PromptTemplating.Prompt.Tools) != 1 || string(req.Config.Modules.PromptTemplating.Prompt.ResponseFormat) != `{"type":"text"}` {
			t.Errorf("lost prompt tools/response format: %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-luna\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}}\n\ndata: [DONE]\n\n")
	})
	status, _, body := postOrchestration(t, proxyURL, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],"response_format":{"type":"text"},"stream":true,"stream_options":{"include_usage":true},"max_tokens":32,"max_completion_tokens":64,"temperature":0.2,"reasoning_effort":"high","tool_choice":"auto","parallel_tool_calls":true}`)
	if status != 200 || strings.Contains(body, `"error"`) {
		t.Fatalf("status=%d body=%s", status, body)
	}
}

func TestOrchestrationFlushesBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first chunk\"}}]}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}}\n\ndata: [DONE]\n\n")
	})
	// Release before httptest cleanup, including on timeout/failure.
	defer close(release)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(proxyURL+"/litellm/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("proxy buffered the generation instead of flushing: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"content":"first chunk"`) {
		t.Fatalf("first delta unavailable before upstream finished: %s (%v)", scanner.Text(), scanner.Err())
	}
}

func TestOrchestrationPreservesHTTPFailures(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "7")
				w.Header().Set("X-Request-ID", "orch-error")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"upstream rejected request"}}`)
			})
			got, headers, body := postOrchestration(t, proxyURL, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			if got != status || headers.Get("Retry-After") != "7" || headers.Get("X-Request-ID") != "orch-error" || body != `{"error":{"message":"upstream rejected request"}}` || strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("HTTP error replaced by successful stream: %d %v %s", got, headers, body)
			}
		})
	}
}

func TestOrchestrationNonStreamingIsUnchanged(t *testing.T) {
	const result = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`
	proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Config struct{ Stream struct{ Enabled bool } }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Config.Stream.Enabled {
			t.Error("non-streaming call enabled upstream streaming")
		}
		_, _ = io.WriteString(w, `{"final_result":`+result+`}`)
	})
	status, headers, body := postOrchestration(t, proxyURL, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 || !strings.HasPrefix(headers.Get("Content-Type"), "application/json") || body != result {
		t.Fatalf("non-streaming completion changed: %d %v %s", status, headers, body)
	}
}

func TestOrchestrationRejectsInvalidStreamOptions(t *testing.T) {
	proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached SAP")
		_, _ = io.WriteString(w, `{"final_result":{}}`)
	})
	for _, extra := range []string{
		`"stream":null`,
		`"stream":"true"`,
		`"stream":true,"stream_options":null`,
		`"stream":true,"stream_options":[]`,
		`"stream":true,"stream_options":{"include_usage":null}`,
		`"stream":true,"stream_options":{"include_usage":"true"}`,
		`"stream":true,"stream_options":{"unsupported":true}`,
		`"stream":false,"stream_options":{"include_usage":true}`,
		`"stream":true,"unsupported":true`,
		`"stream":true,"prompt":"legacy"`,
	} {
		t.Run(extra, func(t *testing.T) {
			status, _, body := postOrchestration(t, proxyURL, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],`+extra+`}`)
			if status != 400 {
				t.Fatalf("invalid request accepted: %d %s", status, body)
			}
		})
	}
}

func TestOrchestrationCancelsUpstreamWhenClientDisconnects(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	proxyURL := orchestrationProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"final_result\":{\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-luna\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/litellm/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"content":"hello"`) {
		_ = resp.Body.Close()
		t.Fatalf("initial delta missing: %s (%v)", scanner.Text(), scanner.Err())
	}
	_ = resp.Body.Close()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-upstreamCanceled:
	case <-timer.C:
		t.Fatal("client disconnect did not cancel SAP generation")
	}
}
