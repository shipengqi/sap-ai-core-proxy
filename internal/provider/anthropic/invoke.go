package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// InvokeAnthropicProvider handles Claude 3 models via SAP Invoke API → Anthropic SSE.
type InvokeAnthropicProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewInvokeAnthropicProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *InvokeAnthropicProvider {
	return &InvokeAnthropicProvider{client: client, dm: dm}
}

func (p *InvokeAnthropicProvider) handle(c *gin.Context, req *MessagesRequest, chain []string) {
	ctx := bg()
	deploymentID, _, err := p.dm.GetDeploymentIDFromChain(ctx, chain)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	payload := p.buildPayload(req)

	if req.Stream {
		path := fmt.Sprintf("/v2/inference/deployments/%s/invoke-with-response-stream", deploymentID)
		resp, err := p.client.PostStream(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		doOrchestrateStream(c, resp, stream.StreamContext{
			APIFormat:      "invoke",
			ResponseFormat: "anthropic",
			Model:          req.Model,
			CompletionID:   genMsgID(),
		})
	} else {
		path := fmt.Sprintf("/v2/inference/deployments/%s/invoke", deploymentID)
		resp, err := p.client.Post(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := readJSONResponse(resp)
		if err != nil {
			sendAnthropicError(c, http.StatusInternalServerError, err.Error())
			return
		}
		// Invoke API returns native Anthropic format — pass through with model name fix
		data["model"] = req.Model
		if data["id"] == nil {
			data["id"] = genMsgID()
		}
		c.JSON(http.StatusOK, data)
	}
}

func (p *InvokeAnthropicProvider) buildPayload(req *MessagesRequest) map[string]any {
	sys := extractSystemPrompt(req.System)

	// Convert messages to simple string content
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		text := contentToText(msg.Content)
		msgs = append(msgs, map[string]any{
			"role":    msg.Role,
			"content": text,
		})
	}

	payload := map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        req.MaxTokens,
		"messages":          msgs,
	}
	if sys != "" {
		payload["system"] = sys
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		payload["stop_sequences"] = req.StopSequences
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			}
		}
		payload["tools"] = tools
	}
	return payload
}

// contentToText extracts a plain text string from message content.
func contentToText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	result := ""
	for _, b := range blocks {
		if b.Type == "text" {
			result += b.Text
		}
	}
	return result
}
