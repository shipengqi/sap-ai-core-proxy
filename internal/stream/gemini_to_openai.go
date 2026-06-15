package stream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PipeGeminiToOpenAI converts a Gemini streamGenerateContent SSE response to
// OpenAI SSE format. SAP AI Core returns Gemini streaming as SSE lines
// ("data: {...}"), so each line must be stripped of its "data: " prefix before
// JSON parsing.
func PipeGeminiToOpenAI(c *gin.Context, upstream *http.Response, id, model string) {
	defer func() { _ = upstream.Body.Close() }()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}

	writeChunk := func(deltaJSON string, finishReason interface{}) {
		var fr string
		if finishReason == nil {
			fr = "null"
		} else {
			fr = fmt.Sprintf("%q", finishReason)
		}
		chunk := fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
			id, model, deltaJSON, fr)
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	// Send initial role delta, matching OpenAI streaming convention.
	writeChunk(`{"role":"assistant","content":""}`, nil)

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		// Strip SSE "data: " prefix.
		payload := line
		if bytes.HasPrefix(payload, []byte("data:")) {
			payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
		}
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		var gr struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal(payload, &gr); err != nil || len(gr.Candidates) == 0 {
			continue
		}

		text := ""
		for _, p := range gr.Candidates[0].Content.Parts {
			text += p.Text
		}
		finishReason := gr.Candidates[0].FinishReason

		textJSON, _ := json.Marshal(text)

		switch finishReason {
		case "STOP":
			writeChunk(fmt.Sprintf(`{"content":%s}`, textJSON), nil)
			writeChunk(`{}`, "stop")
		case "MAX_TOKENS":
			writeChunk(fmt.Sprintf(`{"content":%s}`, textJSON), nil)
			writeChunk(`{}`, "length")
		default:
			writeChunk(fmt.Sprintf(`{"content":%s}`, textJSON), nil)
		}
	}

	_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()
}
