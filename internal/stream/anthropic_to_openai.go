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
//	content_block_start (tool_use block)     → chunk with a tool_calls delta (index/id/name)
//	content_block_delta (text_delta)         → chunk with content delta
//	content_block_delta (input_json_delta)   → chunk with a tool_calls arguments delta
//	content_block_stop  (thinking block)     → chunk with "</think>" content delta
//	message_delta                            → records stop_reason for the final chunk
//	message_stop                             → final chunk with finish_reason, then data: [DONE]
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

	writeRaw := func(chunk string) {
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	writeChunk := func(deltaJSON string) {
		writeRaw(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":null}]}`,
			completionID, model, deltaJSON))
	}

	writeFinish := func(reason string) {
		writeRaw(fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":%q}]}`,
			completionID, model, reason))
	}

	writeDone := func() {
		writeRaw("[DONE]")
	}

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var currentEventType string
	var inThinkingBlock bool
	var stopReason string
	var sawToolUse bool
	// currentToolIndex is the OpenAI tool_calls index of the block currently being
	// streamed, or -1 when the active content block is not a tool_use.
	currentToolIndex := -1
	toolCount := 0

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
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(payload), &blockStart); err != nil {
				continue
			}
			switch blockStart.ContentBlock.Type {
			case "thinking":
				inThinkingBlock = true
				writeChunk(`{"content":"<think>"}`)
			case "tool_use":
				sawToolUse = true
				currentToolIndex = toolCount
				toolCount++
				writeChunk(toolCallStartDelta(currentToolIndex, blockStart.ContentBlock.ID, blockStart.ContentBlock.Name))
			}

		case "content_block_delta":
			var delta struct {
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &delta); err != nil {
				continue
			}
			switch delta.Delta.Type {
			case "text_delta":
				writeChunk(fmt.Sprintf(`{"content":%s}`, mustJSON(delta.Delta.Text)))
			case "input_json_delta":
				if currentToolIndex >= 0 {
					writeChunk(fmt.Sprintf(`{"tool_calls":[{"index":%d,"function":{"arguments":%s}}]}`,
						currentToolIndex, mustJSON(delta.Delta.PartialJSON)))
				}
			}

		case "content_block_stop":
			if inThinkingBlock {
				inThinkingBlock = false
				writeChunk(`{"content":"</think>"}`)
			}
			currentToolIndex = -1

		case "message_delta":
			var messageDelta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &messageDelta); err == nil && messageDelta.Delta.StopReason != "" {
				stopReason = messageDelta.Delta.StopReason
			}

		case "message_stop":
			writeFinish(openAIFinishReason(stopReason, sawToolUse))
			writeDone()
			return
		}
	}

	// If upstream ended without message_stop, still close the stream properly.
	writeFinish(openAIFinishReason(stopReason, sawToolUse))
	writeDone()
}

// toolCallStartDelta builds the first OpenAI delta for a tool call, which carries
// the index, id and function name.
func toolCallStartDelta(index int, id, name string) string {
	return fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]}`,
		index, mustJSON(id), mustJSON(name))
}

// openAIFinishReason maps an Anthropic stop_reason to an OpenAI finish_reason.
func openAIFinishReason(stopReason string, sawToolUse bool) string {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence", "end_turn":
		return "stop"
	}
	// Some upstreams omit message_delta entirely; fall back to what we observed.
	if sawToolUse {
		return "tool_calls"
	}
	return "stop"
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}
