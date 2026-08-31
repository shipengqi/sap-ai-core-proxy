package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// PipeAnthropicToOpenAI reads an Anthropic-format SSE stream from upstream (SAP
// /invoke-with-response-stream) and writes OpenAI-format SSE chunks to the caller.
//
// Event mapping:
//
//	message_start                            → chunk with role:"assistant" delta
//	content_block_start (thinking block)     → chunk with "<think>" content delta
//	content_block_delta (text_delta only)    → chunk with content delta
//	content_block_stop  (thinking block)     → chunk with "</think>" content delta
//	message_stop                             → data: [DONE]
//	all other event types                    → skipped
func PipeAnthropicToOpenAI(c *gin.Context, upstream *http.Response, completionID, model string) {
	defer func() { _ = upstream.Body.Close() }()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}

	writeChunk := func(deltaJSON string) {
		chunk := fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":null}]}`,
			completionID, model, deltaJSON)
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	writeDone := func() {
		_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		flusher.Flush()
	}

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var currentEventType string
	var inThinkingBlock bool

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			currentEventType = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		// Infer event type from payload "type" field if not set by event: line.
		eventType := currentEventType
		if eventType == "" {
			eventType = extractEventType(payload)
		}

		switch eventType {
		case "message_start":
			writeChunk(`{"role":"assistant","content":""}`)

		case "content_block_start":
			var blockStart struct {
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(payload), &blockStart); err != nil {
				continue
			}
			if blockStart.ContentBlock.Type == "thinking" {
				inThinkingBlock = true
				writeChunk(`{"content":"<think>"}`)
			}

		case "content_block_delta":
			var delta struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &delta); err != nil {
				continue
			}
			if delta.Delta.Type != "text_delta" {
				continue
			}
			textJSON, _ := json.Marshal(delta.Delta.Text)
			writeChunk(fmt.Sprintf(`{"content":%s}`, textJSON))

		case "content_block_stop":
			if inThinkingBlock {
				inThinkingBlock = false
				writeChunk(`{"content":"</think>"}`)
			}

		case "message_stop":
			stopChunk := fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				completionID, model)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", stopChunk)
			flusher.Flush()
			writeDone()
			return
		}
	}

	// If upstream ended without message_stop, still send [DONE].
	writeDone()
}
