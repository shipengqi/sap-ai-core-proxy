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

// runResponsesPipe feeds a canned Responses SSE body through the converter and
// returns the chunks the caller would have received.
func runResponsesPipe(t *testing.T, sse string, includeUsage bool) []map[string]interface{} {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	PipeResponsesToOpenAI(c, upstream, "chatcmpl-dep", "gpt-6-astra", includeUsage)

	var chunks []map[string]interface{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("emitted a non-JSON chunk %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Error("stream must end with [DONE]")
	}
	return chunks
}

func TestPipeResponsesToOpenAI_FunctionCall(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		``,
		`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_time","arguments":""},"output_index":0}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","delta":"{}","item_id":"fc_1","output_index":0}`,
		``,
		`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_time","arguments":"{}"},"output_index":0}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":45,"output_tokens":14,"total_tokens":59,"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":5}}}}`,
		``,
	}, "\n")

	chunks := runResponsesPipe(t, sse, true)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	first := chunks[0]["choices"].([]interface{})[0].(map[string]interface{})
	if delta := first["delta"].(map[string]interface{}); delta["role"] != "assistant" {
		t.Errorf("first chunk should open the assistant turn, got %+v", first)
	}

	// The tool call must be announced with its id and name, then streamed
	// argument fragments, exactly like the OpenAI chat stream does.
	announced := false
	args := ""
	finish := ""
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]interface{})
		if len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]interface{})
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			finish = fr
		}
		delta, _ := choice["delta"].(map[string]interface{})
		calls, _ := delta["tool_calls"].([]interface{})
		for _, raw := range calls {
			call := raw.(map[string]interface{})
			if id, ok := call["id"].(string); ok && id == "call_1" {
				announced = true
				if fn := call["function"].(map[string]interface{}); fn["name"] != "get_time" {
					t.Errorf("tool name not carried over: %+v", fn)
				}
			}
			if fn, ok := call["function"].(map[string]interface{}); ok {
				if fragment, ok := fn["arguments"].(string); ok {
					args += fragment
				}
			}
		}
	}
	if !announced {
		t.Error("tool call was never announced with its id")
	}
	if args != "{}" {
		t.Errorf("argument deltas should concatenate to {}, got %q", args)
	}
	if finish != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %q", finish)
	}

	usageChunk := chunks[len(chunks)-1]
	usage, ok := usageChunk["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a trailing usage chunk, got %+v", usageChunk)
	}
	if usage["prompt_tokens"].(float64) != 45 || usage["completion_tokens"].(float64) != 14 {
		t.Errorf("usage not mapped: %+v", usage)
	}
}

func TestPipeResponsesToOpenAI_Text(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hel","item_id":"msg_1"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"lo","item_id":"msg_1"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"type":"message"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		``,
	}, "\n")

	chunks := runResponsesPipe(t, sse, false)

	text := ""
	finish := ""
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]interface{})
		if len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]interface{})
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			finish = fr
		}
		if delta, ok := choice["delta"].(map[string]interface{}); ok {
			if s, ok := delta["content"].(string); ok {
				text += s
			}
		}
	}
	if text != "Hello" {
		t.Errorf("text deltas should concatenate to Hello, got %q", text)
	}
	if finish != "stop" {
		t.Errorf("expected finish_reason=stop, got %q", finish)
	}
	// include_usage was false, so no usage block should be emitted.
	for _, chunk := range chunks {
		if _, ok := chunk["usage"]; ok {
			t.Errorf("usage chunk emitted without include_usage: %+v", chunk)
		}
	}
}

func TestPipeResponsesToOpenAI_FailedEvent(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.failed","response":{"id":"resp_3","status":"failed"}}`,
		``,
	}, "\n")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	PipeResponsesToOpenAI(c, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, "chatcmpl-dep", "m", false)

	body := rec.Body.String()
	if !strings.Contains(body, "upstream_error") {
		t.Errorf("a failed response must surface an error chunk, got %q", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("a failed stream must still terminate with [DONE]")
	}
}

func TestPipeResponsesToOpenAI_Incomplete(t *testing.T) {
	// A truncated function call must not tell the client to execute it.
	sse := `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call"}],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"
	chunks := runResponsesPipe(t, sse, true)
	var finish string
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]interface{})
		if len(choices) > 0 {
			if reason, ok := choices[0].(map[string]interface{})["finish_reason"].(string); ok {
				finish = reason
			}
		}
	}
	if finish != "length" {
		t.Errorf("truncated tool call should finish with length, got %q", finish)
	}
	if _, ok := chunks[len(chunks)-1]["usage"]; !ok {
		t.Error("incomplete response must still report usage")
	}
}

func TestPipeResponsesToOpenAI_UnexpectedEOF(t *testing.T) {
	chunks := runResponsesPipe(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", false)
	if _, ok := chunks[len(chunks)-1]["error"]; !ok {
		t.Fatal("a stream cut off before a terminal event must surface an error")
	}
}

func TestPipeResponsesToOpenAI_UsageWithoutClientOptIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	col := usage.NewCollector()
	r := gin.New()
	r.Use(usage.Middleware(col))
	r.POST("/openai/v1/chat/completions", func(c *gin.Context) {
		sse := `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":30,"output_tokens":7,"input_tokens_details":{"cached_tokens":5}}}}` + "\n\n"
		PipeResponsesToOpenAI(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sse))}, "id", "m", false)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil))
	if strings.Contains(rec.Body.String(), `"usage"`) {
		t.Error("usage must not be sent to a client that did not request it")
	}
	stats := col.Snapshot()
	if stats.Totals.Tokens.Input != 30 || stats.Totals.Tokens.Output != 7 || stats.Totals.Tokens.CacheRead != 5 {
		t.Errorf("proxy must observe usage independently of client opt-in: %+v", stats.Totals)
	}
}
