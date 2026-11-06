package stream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

func runOrchestrationPipe(t *testing.T, sse string, includeUsage bool) (string, []map[string]json.RawMessage, usage.Snapshot) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	col := usage.NewCollector()
	r := gin.New()
	r.Use(usage.Middleware(col))
	r.POST("/litellm/v1/chat/completions", func(c *gin.Context) {
		c.Set("model", "gpt-6-luna")
		PipeOrchestrationToOpenAI(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sse))}, includeUsage)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/litellm/v1/chat/completions", nil))
	var chunks []map[string]json.RawMessage
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("invalid client event %s: %v", line, err)
		}
		chunks = append(chunks, chunk)
	}
	return rec.Body.String(), chunks, col.Snapshot()
}

func orchestrationEvent(choices string, extra string) string {
	return `data: {"intermediate_results":{"llm":{"choices":[{"delta":{"content":"unfiltered output"}}]}},"final_result":{"id":"chatcmpl-orch","object":"chat.completion.chunk","created":1,"model":"gpt-6-luna","choices":` + choices + extra + "}}\n\n"
}

func TestOrchestrationToolDeltasAndUsage(t *testing.T) {
	sse := orchestrationEvent(`[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":""}]`, "") +
		orchestrationEvent(`[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]`, "") +
		orchestrationEvent(`[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":"tool_calls"}]`, `,"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}`) +
		"data: [DONE]\n\n"
	for _, includeUsage := range []bool{false, true} {
		body, chunks, snapshot := runOrchestrationPipe(t, sse, includeUsage)
		args, finish, usageChunks := "", "", 0
		for _, chunk := range chunks {
			if chunk["error"] != nil {
				t.Fatal(body)
			}
			var choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int
						Function struct{ Arguments string }
					} `json:"tool_calls"`
				}
				FinishReason string `json:"finish_reason"`
			}
			if err := json.Unmarshal(chunk["choices"], &choices); err != nil {
				t.Fatal(err)
			}
			for _, choice := range choices {
				for _, call := range choice.Delta.ToolCalls {
					args += call.Function.Arguments
				}
				if choice.FinishReason != "" {
					finish = choice.FinishReason
				}
			}
			if chunk["usage"] != nil {
				usageChunks++
				if !includeUsage || len(choices) != 0 || !strings.Contains(string(chunk["usage"]), `"reasoning_tokens":2`) {
					t.Fatalf("usage must be an opt-in, standalone chunk: %s", body)
				}
			}
		}
		if args != `{"path":"README.md"}` || finish != "tool_calls" || !strings.Contains(body, `"id":"call_1"`) || !strings.Contains(body, `"reasoning_content":"thinking"`) || strings.Contains(body, "unfiltered output") {
			t.Fatal(body)
		}
		if includeUsage && usageChunks != 1 || !includeUsage && usageChunks != 0 {
			t.Fatal("incorrect usage chunk count", usageChunks)
		}
		if snapshot.Totals.Tokens.Input != 10 || snapshot.Totals.Tokens.Output != 8 || snapshot.Totals.Tokens.CacheRead != 3 || snapshot.Totals.Requests != 1 || snapshot.Totals.Failures != 0 || snapshot.Totals.UsageMissing != 0 {
			t.Fatalf("usage was lost or counted twice: %+v", snapshot.Totals)
		}
	}
}

func TestOrchestrationPreservesStreamErrors(t *testing.T) {
	for _, errorPayload := range []string{
		`{"error":{"request_id":"req-error","code":400,"message":"model version not found","location":"LLM Module"}}`,
		`{"error":[{"code":400,"message":"model version not found","location":"LLM Module"}]}`,
	} {
		sse := orchestrationEvent(`[{"index":0,"delta":{"content":"partial"}}]`, "") + "data: " + errorPayload + "\n\ndata: [DONE]\n\n"
		body, chunks, snapshot := runOrchestrationPipe(t, sse, false)
		if len(chunks) != 2 || !strings.Contains(string(chunks[1]["error"]), "model version not found") || strings.Count(body, "data: [DONE]") != 1 || snapshot.Totals.Failures != 1 {
			t.Fatalf("Orchestration error lost or reported as success: %s; %+v", body, snapshot.Totals)
		}
	}
}

func TestOrchestrationRejectsIncompleteAndMalformedStreams(t *testing.T) {
	for name, sse := range map[string]string{
		"empty":             "data: [DONE]\n\n",
		"unfinished tools":  orchestrationEvent(`[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]`, "") + "data: [DONE]\n\n",
		"missing done":      orchestrationEvent(`[{"index":0,"delta":{},"finish_reason":"stop"}]`, ""),
		"invalid JSON":      "data: {oops}\n\n",
		"invalid result":    "data: {\"final_result\":42}\n\n",
		"null choices":      orchestrationEvent("null", "") + "data: [DONE]\n\n",
		"null choice":       orchestrationEvent("[null]", "") + "data: [DONE]\n\n",
		"invalid finish":    orchestrationEvent(`[{"index":0,"delta":{},"finish_reason":42}]`, "") + "data: [DONE]\n\n",
		"invalid delta":     orchestrationEvent(`[{"index":0,"delta":42}]`, "") + "data: [DONE]\n\n",
		"unfinished choice": orchestrationEvent(`[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{"content":"partial"}}]`, "") + "data: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			body, _, snapshot := runOrchestrationPipe(t, sse, false)
			if !strings.Contains(body, `"error"`) || strings.Count(body, "data: [DONE]") != 1 || snapshot.Totals.Failures != 1 {
				t.Fatalf("invalid stream declared successful: %s; %+v", body, snapshot.Totals)
			}
		})
	}
}

func TestOrchestrationUsageOnlyChunkAndMultilineFrames(t *testing.T) {
	sse := ": heartbeat\r\n\r\n" +
		"data: {\r\ndata: \"request_id\":\"req\",\"final_result\":null}\r\n\r\n" +
		orchestrationEvent(`[{"index":0,"delta":{"role":"","content":"hello"},"finish_reason":"stop"}]`, "") +
		orchestrationEvent("[]", `,"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18}`) +
		"data: [DONE]"
	body, chunks, snapshot := runOrchestrationPipe(t, sse, true)
	if len(chunks) != 2 || string(chunks[1]["choices"]) != "[]" || chunks[1]["usage"] == nil || strings.Contains(body, `"role":""`) || strings.Contains(body, `"error"`) || snapshot.Totals.Tokens.Input != 12 || snapshot.Totals.Tokens.Output != 6 {
		t.Fatalf("usage-only or multiline event corrupted: %s; %+v", body, snapshot.Totals)
	}
}

func TestOrchestrationRejectsOversizedFrame(t *testing.T) {
	body, _, snapshot := runOrchestrationPipe(t, "data: "+strings.Repeat("x", maxSSEFrameBytes)+"\n\n", false)
	if !strings.Contains(body, `"error"`) || strings.Count(body, "data: [DONE]") != 1 || snapshot.Totals.Failures != 1 {
		t.Fatalf("oversized stream declared successful: %s", body)
	}
}
