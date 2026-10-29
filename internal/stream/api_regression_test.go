package stream

import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func auditAnthropicPipe(body string) string {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	PipeAnthropicToOpenAI(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, "id", "m")
	return rec.Body.String()
}
func TestProxyRegressionAnthropicStreamError(t *testing.T) {
	got := auditAnthropicPipe("data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"retry\"}}\n\n")
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("error disappeared and stream completed successfully: %s", got)
	}
}
func TestProxyRegressionAnthropicLargeToolDelta(t *testing.T) {
	start := "data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call1\",\"name\":\"write_file\"}}\n\n"
	large := strings.Repeat("x", 70*1024)
	got := auditAnthropicPipe(start + "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"" + large + "\"}}\n\n" + "data: {\"type\":\"message_stop\"}\n\n")
	if !strings.Contains(got, large) && !strings.Contains(got, `"error"`) {
		t.Fatalf("70 KiB argument delta lost, but client was told tool_calls completed: %s", got)
	}
}
