package stream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSSEMultilineAndFinalFrame(t *testing.T) {
	var frames []sseFrame
	err := readSSE(strings.NewReader(": heartbeat\r\n\r\nevent: chunk\r\ndata: {\r\ndata: \"text\":\"hello\"}\r\n\r\ndata: [DONE]"), func(f sseFrame) bool {
		frames = append(frames, f)
		return true
	})
	if err != nil || len(frames) != 3 || frames[1].Event != "chunk" || frames[1].Data != "{\n\"text\":\"hello\"}" || frames[2].Data != "[DONE]" {
		t.Fatalf("incorrect frame parsing: %+v, %v", frames, err)
	}
}

func TestSSERejectsOversizedFrame(t *testing.T) {
	line := "data: " + strings.Repeat("x", 4096) + "\n"
	err := readSSE(strings.NewReader(strings.Repeat(line, maxSSEFrameBytes/4096+1)), func(sseFrame) bool { t.Fatal("oversized frame emitted"); return false })
	if err == nil {
		t.Fatal("oversized frame accepted")
	}
}

type failingWriter struct{ headers http.Header }

func (w failingWriter) Header() http.Header       { return w.headers }
func (w failingWriter) WriteHeader(int)           {}
func (w failingWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }
func (w failingWriter) Flush()                    {}

type oneFrameBody struct {
	frame  string
	reads  int
	closed bool
}

func (b *oneFrameBody) Read(p []byte) (int, error) {
	b.reads++
	if b.frame == "" {
		return 0, errors.New("must not read after client write failure")
	}
	n := copy(p, b.frame)
	b.frame = b.frame[n:]
	return n, nil
}
func (b *oneFrameBody) Close() error { b.closed = true; return nil }

func TestPipesStopOnClientWriteFailure(t *testing.T) {
	for _, name := range []string{"openai", "responses", "bridge", "anthropic", "gemini"} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(failingWriter{headers: make(http.Header)})
			body := &oneFrameBody{frame: "data: {\"type\":\"response.created\"}\n\n"}
			resp := &http.Response{StatusCode: 200, Body: body}
			switch name {
			case "openai":
				PipeOpenAI(c, resp)
			case "responses":
				PipeResponses(c, resp, nil)
			case "bridge":
				PipeResponsesToOpenAI(c, resp, "id", "m", false)
			case "anthropic":
				body.frame = "data: {\"type\":\"message_start\"}\n\n"
				PipeAnthropicToOpenAI(c, resp, "id", "m")
			case "gemini":
				body.frame = "[{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}]"
				PipeGeminiToOpenAI(c, resp, "id", "m")
			}
			if !body.closed || body.reads > 1 {
				t.Fatalf("upstream continued after failed write: %+v", body)
			}
		})
	}
}

func TestResponsesBridgeMultilineRefusalAndLargeArguments(t *testing.T) {
	chunks := runResponsesPipe(t, "event: response.refusal.delta\ndata: {\ndata: \"delta\":\"cannot help\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}", false)
	if chunks[1]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["refusal"] != "cannot help" {
		t.Fatal(chunks)
	}
	args := `{"text":"` + strings.Repeat("x", 1100*1024) + `"}`
	quoted, _ := json.Marshal(args)
	sse := "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc\",\"type\":\"function_call\",\"call_id\":\"call\",\"name\":\"write_file\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc\",\"delta\":" + string(quoted) + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc\",\"type\":\"function_call\",\"arguments\":" + string(quoted) + "}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\"}]}}\n\n"
	for _, chunk := range runResponsesPipe(t, sse, false) {
		if chunk["error"] != nil {
			t.Fatal(chunk["error"])
		}
	}
}

func TestGeminiUsageSurvivesFinalPacket(t *testing.T) {
	first := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`
	final := `{"candidates":[{"finishReason":"STOP"}]}`
	for _, body := range []string{"[" + first + "," + final + "]", "data: " + first + "\n\ndata: " + final + "\n\n"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		PipeGeminiToOpenAI(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, "id", "m", true)
		if !strings.Contains(rec.Body.String(), `"completion_tokens":5`) || !strings.Contains(rec.Body.String(), `"prompt_tokens":9`) {
			t.Fatal(rec.Body.String())
		}
	}
}

func TestGeminiRejectsOversizedJSONValue(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `[{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", maxSSEFrameBytes) + `"}]},"finishReason":"STOP"}]}]`
	PipeGeminiToOpenAI(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, "id", "m")
	if !strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), `"finish_reason":"stop"`) {
		t.Fatal("oversized Gemini value declared successful")
	}
}
