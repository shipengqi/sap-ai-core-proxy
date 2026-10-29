package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

func PipeGeminiToOpenAI(c *gin.Context, upstream *http.Response, id, model string, includeUsage ...bool) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	failed, writeFailed := false, false
	announced := map[int]bool{}
	finished := map[int]bool{}
	toolCounts := map[int]int{}
	sawTools := map[int]bool{}
	var last transform.GeminiResponse
	write := func(index int, delta any, reason any) bool {
		ok := emitSSE(c, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": index, "delta": delta, "finish_reason": reason}}})
		writeFailed = !ok
		return ok
	}
	consume := func(payload []byte) bool {
		var g transform.GeminiResponse
		if json.Unmarshal(payload, &g) != nil {
			streamError(c, "openai", "invalid upstream Gemini event")
			failed = true
			return false
		}
		usage.ObserveGeminiChunk(c, payload)
		// Gemini usage is cumulative and may be absent from the final packet.
		last.UsageMetadata.Input = max(last.UsageMetadata.Input, g.UsageMetadata.Input)
		last.UsageMetadata.Output = max(last.UsageMetadata.Output, g.UsageMetadata.Output)
		last.UsageMetadata.Total = max(last.UsageMetadata.Total, g.UsageMetadata.Total)
		last.UsageMetadata.Cached = max(last.UsageMetadata.Cached, g.UsageMetadata.Cached)
		last.UsageMetadata.Thinking = max(last.UsageMetadata.Thinking, g.UsageMetadata.Thinking)
		if len(g.Error) > 0 && string(g.Error) != "null" {
			streamError(c, "openai", "upstream Gemini error: "+string(g.Error))
			failed = true
			return false
		}
		if len(g.Candidates) == 0 && len(g.PromptFeedback) > 0 && string(g.PromptFeedback) != "null" {
			streamError(c, "openai", "Gemini produced no candidates: "+string(g.PromptFeedback))
			failed = true
			return false
		}
		for _, candidate := range g.Candidates {
			index := candidate.Index
			if !announced[index] {
				announced[index] = true
				if !write(index, map[string]any{"role": "assistant", "content": ""}, nil) {
					return false
				}
			}
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					field := "content"
					if part.Thought {
						field = "reasoning_content"
					}
					if !write(index, map[string]any{field: part.Text}, nil) {
						return false
					}
				}
				if part.FunctionCall != nil {
					call := transform.GeminiToolCall(part)
					call["index"] = toolCounts[index]
					toolCounts[index]++
					sawTools[index] = true
					if !write(index, map[string]any{"tool_calls": []any{call}}, nil) {
						return false
					}
				}
			}
			if candidate.FinishReason != "" {
				finished[index] = true
				if !write(index, map[string]any{}, transform.GeminiFinishReason(candidate.FinishReason, sawTools[index])) {
					return false
				}
			}
		}
		return true
	}
	var err error
	reader := bufio.NewReader(upstream.Body)
	for {
		b, e := reader.Peek(1)
		if e != nil {
			break
		}
		if b[0] != ' ' && b[0] != '\n' && b[0] != '\r' && b[0] != '\t' {
			break
		}
		_, _ = reader.ReadByte()
	}
	first, _ := reader.Peek(1)
	if len(first) > 0 && first[0] == '[' {
		bounded := &boundedJSONReader{r: reader, limit: maxSSEFrameBytes}
		decoder := json.NewDecoder(bounded)
		_, err = decoder.Token()
		if err == nil {
			for {
				bounded.limit = decoder.InputOffset() + maxSSEFrameBytes
				if !decoder.More() {
					break
				}
				var raw json.RawMessage
				err = decoder.Decode(&raw)
				if err != nil || !consume(raw) {
					break
				}
			}
			if err == nil && !failed && !writeFailed {
				_, err = decoder.Token()
			}
		}
	} else {
		err = readSSE(reader, func(frame sseFrame) bool {
			payload := strings.TrimSpace(frame.Data)
			if payload == "" {
				payload = strings.TrimSpace(frame.Raw)
			}
			if payload == "" || payload == "[DONE]" || strings.HasPrefix(payload, ":") {
				return true
			}
			return consume([]byte(payload))
		})
	}
	if failed || writeFailed {
		return
	}
	if err != nil {
		streamError(c, "openai", err.Error())
		return
	}
	if len(announced) == 0 || len(finished) != len(announced) {
		streamError(c, "openai", "upstream Gemini stream ended before completion")
		return
	}
	if len(includeUsage) > 0 && includeUsage[0] {
		if !emitSSE(c, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{}, "usage": transform.GeminiUsage(last)}) {
			return
		}
	}
	_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

// Limit decoder read-ahead to one bounded JSON value at a time.
type boundedJSONReader struct {
	r           io.Reader
	read, limit int64
}

func (r *boundedJSONReader) Read(p []byte) (int, error) {
	left := r.limit - r.read
	if left <= 0 {
		return 0, fmt.Errorf("upstream Gemini JSON value exceeds %d bytes", maxSSEFrameBytes)
	}
	if int64(len(p)) > left {
		p = p[:left]
	}
	n, err := r.r.Read(p)
	r.read += int64(n)
	return n, err
}
