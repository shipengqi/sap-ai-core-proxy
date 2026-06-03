package anthropic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// MessagesRequest mirrors the Anthropic /v1/messages request body.
type MessagesRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
}

type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ClaudeAnthropicProvider is the entry point for /anthropic/v1/messages.
type ClaudeAnthropicProvider struct {
	converse *ConverseAnthropicProvider
	invoke   *InvokeAnthropicProvider
}

func NewClaudeAnthropicProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *ClaudeAnthropicProvider {
	return &ClaudeAnthropicProvider{
		converse: NewConverseAnthropicProvider(client, dm),
		invoke:   NewInvokeAnthropicProvider(client, dm),
	}
}

func (p *ClaudeAnthropicProvider) HandleMessages(c *gin.Context) {
	var req MessagesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": "invalid request body: " + err.Error()},
		})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": "Missing required parameter: model"},
		})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": "Missing required parameter: messages"},
		})
		return
	}

	sapName, err := catalogue.MapFromAnthropic(req.Model)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": err.Error()},
		})
		return
	}

	slog.Info("Anthropic Messages API", "model", req.Model, "sapModel", sapName, "stream", req.Stream)

	if catalogue.UsesConverseAPI(sapName) {
		p.converse.handle(c, &req, sapName)
	} else {
		p.invoke.handle(c, &req, sapName)
	}
}

func (p *ClaudeAnthropicProvider) HandleCountTokens(c *gin.Context) {
	var req MessagesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var total int
	if req.System != nil {
		total += len(req.System)
	}
	for _, msg := range req.Messages {
		total += len(msg.Content)
	}
	for _, t := range req.Tools {
		b, _ := json.Marshal(t)
		total += len(b)
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": total / 4})
}

// ---- shared helpers ----

func extractSystemPrompt(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		result := ""
		for i, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				if i > 0 {
					result += "\n"
				}
				result += b.Text
			}
		}
		return result
	}
	return ""
}

func sendAnthropicError(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": "api_error", "message": message},
	})
}

func handleUpstreamError(c *gin.Context, err error) {
	if sapErr, ok := err.(*sapclient.SapAPIError); ok {
		sendAnthropicError(c, sapErr.StatusCode, sapErr.Message)
		return
	}
	sendAnthropicError(c, http.StatusInternalServerError, err.Error())
}

func readJSONResponse(resp *http.Response) (map[string]any, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func doOrchestrateStream(c *gin.Context, resp *http.Response, ctx stream.StreamContext) {
	if err := stream.OrchestrateStream(resp, ctx, c.Writer); err != nil {
		slog.Error("stream error", "error", err)
	}
}

func genMsgID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "msg_" + hex.EncodeToString(b)
}

func bg() context.Context { return context.Background() }
