package stream

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// ParseInvokeStream reads the Anthropic-native SSE stream from the SAP Invoke API.
func ParseInvokeStream(r io.Reader, handler func(InvokeEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := line[6:]

		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			slog.Debug("parseInvokeStream: skipping non-JSON line")
			continue
		}

		typ, _ := data["type"].(string)
		switch typ {
		case "message_start":
			msg, _ := data["message"].(map[string]any)
			usage, _ := msg["usage"].(map[string]any)
			msgID, _ := msg["id"].(string)
			if err := handler(InvokeEvent{
				Type:        InvokeMessageStart,
				MessageID:   msgID,
				InputTokens: intFrom(usage, "input_tokens"),
			}); err != nil {
				return err
			}

		case "content_block_start":
			cb, _ := data["content_block"].(map[string]any)
			if err := handler(InvokeEvent{
				Type:         InvokeBlockStart,
				Index:        intFrom(data, "index"),
				ContentBlock: cb,
			}); err != nil {
				return err
			}

		case "content_block_delta":
			delta, _ := data["delta"].(map[string]any)
			if err := handler(InvokeEvent{
				Type:  InvokeBlockDelta,
				Index: intFrom(data, "index"),
				Delta: delta,
			}); err != nil {
				return err
			}

		case "content_block_stop":
			if err := handler(InvokeEvent{
				Type:  InvokeBlockStop,
				Index: intFrom(data, "index"),
			}); err != nil {
				return err
			}

		case "message_delta":
			d, _ := data["delta"].(map[string]any)
			usage, _ := data["usage"].(map[string]any)
			stopReason, _ := d["stop_reason"].(string)
			stopSeq, _ := d["stop_sequence"].(string)
			if err := handler(InvokeEvent{
				Type:         InvokeMessageDelta,
				StopReason:   stopReason,
				StopSequence: stopSeq,
				OutputTokens: intFrom(usage, "output_tokens"),
			}); err != nil {
				return err
			}

		case "message_stop":
			if err := handler(InvokeEvent{Type: InvokeMessageStop}); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}
