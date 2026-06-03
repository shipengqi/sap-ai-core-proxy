package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// ConverseAnthropicProvider handles Claude 3.5+ models via SAP Converse API → Anthropic SSE.
type ConverseAnthropicProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewConverseAnthropicProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *ConverseAnthropicProvider {
	return &ConverseAnthropicProvider{client: client, dm: dm}
}

func (p *ConverseAnthropicProvider) handle(c *gin.Context, req *MessagesRequest, sapName string) {
	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, sapName)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	payload := p.buildPayload(req)

	if req.Stream {
		path := fmt.Sprintf("/v2/inference/deployments/%s/converse-stream", deploymentID)
		resp, err := p.client.PostStream(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		doOrchestrateStream(c, resp, stream.StreamContext{
			APIFormat:      "converse",
			ResponseFormat: "anthropic",
			Model:          req.Model,
			CompletionID:   genMsgID(),
		})
	} else {
		path := fmt.Sprintf("/v2/inference/deployments/%s/converse", deploymentID)
		resp, err := p.client.Post(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		data, err := readJSONResponse(resp)
		if err != nil {
			sendAnthropicError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, p.convertResponse(data, req.Model))
	}
}

func (p *ConverseAnthropicProvider) buildPayload(req *MessagesRequest) map[string]any {
	sys := extractSystemPrompt(req.System)

	messages := p.convertMessages(req.Messages)
	// Apply prompt caching to last 2 user messages
	messages = applyPromptCaching(messages)

	inferenceConfig := map[string]any{
		"maxTokens":   req.MaxTokens,
		"temperature": floatOrDefault(req.Temperature, 0.0),
	}
	if req.TopP != nil {
		inferenceConfig["topP"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		inferenceConfig["stopSequences"] = req.StopSequences
	}

	payload := map[string]any{
		"inferenceConfig": inferenceConfig,
		"messages":        messages,
	}
	if sys != "" {
		payload["system"] = []any{
			map[string]any{"text": sys},
			map[string]any{"cachePoint": map[string]any{"type": "default"}},
		}
	}
	if len(req.Tools) > 0 {
		payload["toolConfig"] = p.convertTools(req.Tools, req.ToolChoice)
	}
	return payload
}

func (p *ConverseAnthropicProvider) convertMessages(msgs []Message) []map[string]any {
	var result []map[string]any
	for _, msg := range msgs {
		content := p.convertContent(msg.Content)
		if len(content) > 0 {
			result = append(result, map[string]any{
				"role":    msg.Role,
				"content": content,
			})
		}
	}
	return result
}

func (p *ConverseAnthropicProvider) convertContent(raw json.RawMessage) []any {
	// Try string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []any{map[string]any{"text": s}}
	}
	// Try array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var result []any
	for _, block := range blocks {
		switch block.Type {
		case "text":
			result = append(result, map[string]any{"text": block.Text})
		case "tool_use":
			var input any
			json.Unmarshal(block.Input, &input)
			result = append(result, map[string]any{
				"toolUse": map[string]any{
					"toolUseId": block.ID,
					"name":      block.Name,
					"input":     input,
				},
			})
		case "tool_result":
			var resultContent []any
			var strContent string
			if err := json.Unmarshal(block.Content, &strContent); err == nil {
				resultContent = []any{map[string]any{"text": strContent}}
			} else {
				var blocks2 []ContentBlock
				if err := json.Unmarshal(block.Content, &blocks2); err == nil {
					for _, b := range blocks2 {
						if b.Type == "text" {
							resultContent = append(resultContent, map[string]any{"text": b.Text})
						}
					}
				}
			}
			status := "success"
			if block.IsError {
				status = "error"
			}
			result = append(result, map[string]any{
				"toolResult": map[string]any{
					"toolUseId": block.ToolUseID,
					"content":   resultContent,
					"status":    status,
				},
			})
		}
	}
	return result
}

func (p *ConverseAnthropicProvider) convertTools(tools []Tool, choice *ToolChoice) map[string]any {
	converseTools := make([]any, len(tools))
	for i, t := range tools {
		converseTools[i] = map[string]any{
			"toolSpec": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": map[string]any{"json": t.InputSchema},
			},
		}
	}
	toolConfig := map[string]any{"tools": converseTools}
	if choice != nil {
		switch choice.Type {
		case "auto":
			toolConfig["toolChoice"] = map[string]any{"auto": map[string]any{}}
		case "any":
			toolConfig["toolChoice"] = map[string]any{"any": map[string]any{}}
		case "tool":
			toolConfig["toolChoice"] = map[string]any{"tool": map[string]any{"name": choice.Name}}
		}
	}
	return toolConfig
}

func (p *ConverseAnthropicProvider) convertResponse(data map[string]any, originalModel string) map[string]any {
	output, _ := data["output"].(map[string]any)
	msg, _ := output["message"].(map[string]any)
	converseContent, _ := msg["content"].([]any)

	var content []any
	for _, item := range converseContent {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := m["text"].(string); ok {
			content = append(content, map[string]any{"type": "text", "text": text})
		} else if toolUse, ok := m["toolUse"].(map[string]any); ok {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    toolUse["toolUseId"],
				"name":  toolUse["name"],
				"input": toolUse["input"],
			})
		}
	}
	if content == nil {
		content = []any{}
	}

	usage, _ := data["usage"].(map[string]any)
	inputTokens := intFromAny(usage, "inputTokens") + intFromAny(usage, "cacheReadInputTokens") + intFromAny(usage, "cacheWriteInputTokens")
	outputTokens := intFromAny(usage, "outputTokens")

	return map[string]any{
		"id":            genMsgID(),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         originalModel,
		"stop_reason":   mapConverseStopReason(strFromAny(data, "stopReason")),
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
	}
}

func applyPromptCaching(messages []map[string]any) []map[string]any {
	// Find indices of user messages
	var userIndices []int
	for i, m := range messages {
		if m["role"] == "user" {
			userIndices = append(userIndices, i)
		}
	}
	cachePoint := map[string]any{"cachePoint": map[string]any{"type": "default"}}
	last := -1
	secondLast := -1
	if len(userIndices) > 0 {
		last = userIndices[len(userIndices)-1]
	}
	if len(userIndices) > 1 {
		secondLast = userIndices[len(userIndices)-2]
	}
	result := make([]map[string]any, len(messages))
	copy(result, messages)
	for i := range result {
		if i == last || i == secondLast {
			content, _ := result[i]["content"].([]any)
			newContent := make([]any, len(content)+1)
			copy(newContent, content)
			newContent[len(content)] = cachePoint
			newMsg := map[string]any{}
			for k, v := range result[i] {
				newMsg[k] = v
			}
			newMsg["content"] = newContent
			result[i] = newMsg
		}
	}
	return result
}

func mapConverseStopReason(r string) string {
	switch r {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_tokens"
	case "stop_sequence":
		return "stop_sequence"
	case "tool_use":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func floatOrDefault(f *float64, def float64) float64 {
	if f != nil {
		return *f
	}
	return def
}

func intFromAny(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func strFromAny(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
