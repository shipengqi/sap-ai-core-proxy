package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// ---- Shared helpers ----

func genChatCmplID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

func created() int { return int(time.Now().Unix()) }

func sendOpenAIError(c *gin.Context, statusCode int, message string, errType ...string) {
	t := "api_error"
	if len(errType) > 0 {
		t = errType[0]
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": message,
			"type":    t,
			"param":   nil,
			"code":    fmt.Sprintf("%d", statusCode),
		},
	})
}

func handleUpstreamError(c *gin.Context, err error) {
	if sapErr, ok := err.(*sapclient.SapAPIError); ok {
		sendOpenAIError(c, sapErr.StatusCode, sapErr.Message)
		return
	}
	sendOpenAIError(c, http.StatusInternalServerError, err.Error())
}

func doOrchestrate(c *gin.Context, resp *http.Response, ctx stream.StreamContext) {
	if err := stream.OrchestrateStream(resp, ctx, c.Writer); err != nil {
		slog.Error("stream error", "error", err)
	}
}

func bg() context.Context { return context.Background() }

func intFromAny(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// extractTextContent extracts plain text from an OpenAI message content field.
func extractTextContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		result := ""
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "text" {
					if text, ok := m["text"].(string); ok {
						result += text
					}
				}
			}
		}
		return result
	}
	return ""
}

// ---- OpenAI chat request type ----

type ChatRequest struct {
	Model            string           `json:"model"`
	Messages         []ChatMessage    `json:"messages"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	N                *int             `json:"n,omitempty"`
	Stream           bool             `json:"stream,omitempty"`
	Stop             json.RawMessage  `json:"stop,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	PresencePenalty  *float64         `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64         `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int   `json:"logit_bias,omitempty"`
	User             string           `json:"user,omitempty"`
	Tools            []any            `json:"tools,omitempty"`
	ToolChoice       any              `json:"tool_choice,omitempty"`
	Functions        []any            `json:"functions,omitempty"`
	FunctionCall     any              `json:"function_call,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ConverseOpenAIProvider handles Claude 3.5+ → OpenAI format via Converse API.
type ConverseOpenAIProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewConverseOpenAIProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *ConverseOpenAIProvider {
	return &ConverseOpenAIProvider{client: client, dm: dm}
}

func (p *ConverseOpenAIProvider) Handle(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, req.Model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	payload := p.buildPayload(&req)
	completionID := genChatCmplID()

	if req.Stream {
		path := fmt.Sprintf("/v2/inference/deployments/%s/converse-stream", deploymentID)
		resp, err := p.client.PostStream(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		doOrchestrate(c, resp, stream.StreamContext{
			APIFormat:      "converse",
			ResponseFormat: "openai",
			Model:          req.Model,
			CompletionID:   completionID,
		})
	} else {
		path := fmt.Sprintf("/v2/inference/deployments/%s/converse", deploymentID)
		resp, err := p.client.Post(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var data map[string]any
		json.Unmarshal(body, &data)

		content := ""
		if output, ok := data["output"].(map[string]any); ok {
			if msg, ok := output["message"].(map[string]any); ok {
				if contents, ok := msg["content"].([]any); ok && len(contents) > 0 {
					if first, ok := contents[0].(map[string]any); ok {
						content, _ = first["text"].(string)
					}
				}
			}
		}

		usage, _ := data["usage"].(map[string]any)
		c.JSON(http.StatusOK, gin.H{
			"id":      completionID,
			"object":  "chat.completion",
			"created": created(),
			"model":   req.Model,
			"choices": []any{gin.H{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": gin.H{
				"prompt_tokens":     intFromAny(usage, "inputTokens"),
				"completion_tokens": intFromAny(usage, "outputTokens"),
				"total_tokens":      intFromAny(usage, "inputTokens") + intFromAny(usage, "outputTokens"),
			},
		})
	}
}

func (p *ConverseOpenAIProvider) buildPayload(req *ChatRequest) map[string]any {
	modelInfo, _ := catalogue.Get(req.Model)
	maxTokens := 4096
	if modelInfo != nil {
		maxTokens = modelInfo.MaxTokens
	}
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	var systemPrompt string
	var msgs []map[string]any
	for _, msg := range req.Messages {
		text := extractTextContent(msg.Content)
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += text
		} else {
			role := "user"
			if msg.Role == "assistant" {
				role = "assistant"
			}
			msgs = append(msgs, map[string]any{
				"role":    role,
				"content": []any{map[string]any{"text": text}},
			})
		}
	}

	temp := 0.0
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	inferenceConfig := map[string]any{
		"maxTokens":   maxTokens,
		"temperature": temp,
	}

	payload := map[string]any{
		"inferenceConfig": inferenceConfig,
		"messages":        msgs,
	}
	if systemPrompt != "" {
		payload["system"] = []any{
			map[string]any{"text": systemPrompt},
			map[string]any{"cachePoint": map[string]any{"type": "default"}},
		}
	}
	return payload
}

// InvokeOpenAIProvider handles Claude 3 → OpenAI format via Invoke API.
type InvokeOpenAIProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewInvokeOpenAIProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *InvokeOpenAIProvider {
	return &InvokeOpenAIProvider{client: client, dm: dm}
}

func (p *InvokeOpenAIProvider) Handle(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, req.Model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	payload := p.buildPayload(&req)
	completionID := genChatCmplID()

	if req.Stream {
		path := fmt.Sprintf("/v2/inference/deployments/%s/invoke-with-response-stream", deploymentID)
		resp, err := p.client.PostStream(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		doOrchestrate(c, resp, stream.StreamContext{
			APIFormat:      "invoke",
			ResponseFormat: "openai",
			Model:          req.Model,
			CompletionID:   completionID,
		})
	} else {
		path := fmt.Sprintf("/v2/inference/deployments/%s/invoke", deploymentID)
		resp, err := p.client.Post(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var data map[string]any
		json.Unmarshal(body, &data)

		// Invoke returns native Anthropic format — convert to OpenAI
		content := ""
		if contents, ok := data["content"].([]any); ok {
			for _, item := range contents {
				if m, ok := item.(map[string]any); ok {
					if t, _ := m["type"].(string); t == "text" {
						if text, ok := m["text"].(string); ok {
							content += text
						}
					}
				}
			}
		}

		usage, _ := data["usage"].(map[string]any)
		c.JSON(http.StatusOK, gin.H{
			"id":      completionID,
			"object":  "chat.completion",
			"created": created(),
			"model":   req.Model,
			"choices": []any{gin.H{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": gin.H{
				"prompt_tokens":     intFromAny(usage, "input_tokens"),
				"completion_tokens": intFromAny(usage, "output_tokens"),
				"total_tokens":      intFromAny(usage, "input_tokens") + intFromAny(usage, "output_tokens"),
			},
		})
	}
}

func (p *InvokeOpenAIProvider) buildPayload(req *ChatRequest) map[string]any {
	modelInfo, _ := catalogue.Get(req.Model)
	maxTokens := 4096
	if modelInfo != nil {
		maxTokens = modelInfo.MaxTokens
	}
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	var systemPrompt string
	var msgs []map[string]any
	for _, msg := range req.Messages {
		text := extractTextContent(msg.Content)
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += text
		} else {
			role := "user"
			if msg.Role == "assistant" {
				role = "assistant"
			}
			msgs = append(msgs, map[string]any{"role": role, "content": text})
		}
	}

	payload := map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        maxTokens,
		"messages":          msgs,
	}
	if systemPrompt != "" {
		payload["system"] = systemPrompt
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.Stop != nil {
		var stop any
		json.Unmarshal(req.Stop, &stop)
		switch v := stop.(type) {
		case string:
			payload["stop_sequences"] = []string{v}
		case []any:
			payload["stop_sequences"] = v
		}
	}
	return payload
}

// GeminiProvider handles Gemini models via SAP AI Core → OpenAI format.
type GeminiProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewGeminiProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *GeminiProvider {
	return &GeminiProvider{client: client, dm: dm}
}

func (p *GeminiProvider) HandleChatCompletion(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, req.Model)
	if err != nil {
		if sapErr, ok := err.(*sapclient.SapAPIError); ok {
			sendOpenAIError(c, sapErr.StatusCode, sapErr.Message)
		} else {
			sendOpenAIError(c, http.StatusInternalServerError, err.Error())
		}
		return
	}

	payload := p.buildPayload(&req)
	completionID := genChatCmplID()
	basePath := fmt.Sprintf("/v2/inference/deployments/%s/models/%s", deploymentID, req.Model)

	if req.Stream {
		resp, err := p.client.PostStream(ctx, basePath+":streamGenerateContent", payload)
		if err != nil {
			if sapErr, ok := err.(*sapclient.SapAPIError); ok {
				if sapErr.StatusCode == 429 {
					sendOpenAIError(c, 429, "Rate limit exceeded: "+sapErr.Message+". Please wait and try again later.", "rate_limit_error")
				} else {
					sendOpenAIError(c, sapErr.StatusCode, sapErr.Message)
				}
			} else {
				sendOpenAIError(c, http.StatusInternalServerError, err.Error())
			}
			return
		}
		doOrchestrate(c, resp, stream.StreamContext{
			APIFormat:      "gemini",
			ResponseFormat: "openai",
			Model:          req.Model,
			CompletionID:   completionID,
		})
	} else {
		resp, err := p.client.Post(ctx, basePath+":generateContent", payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var data map[string]any
		json.Unmarshal(body, &data)

		content := p.extractContent(data)
		usage := p.extractUsage(data)

		c.JSON(http.StatusOK, gin.H{
			"id":      completionID,
			"object":  "chat.completion",
			"created": created(),
			"model":   req.Model,
			"choices": []any{gin.H{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": usage,
		})
	}
}

func (p *GeminiProvider) buildPayload(req *ChatRequest) map[string]any {
	modelInfo, _ := catalogue.Get(req.Model)
	maxTokens := 8192
	if modelInfo != nil {
		maxTokens = modelInfo.MaxTokens
	}
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	var systemInstruction map[string]any
	var contents []map[string]any

	for _, msg := range req.Messages {
		text := extractTextContent(msg.Content)
		if msg.Role == "system" {
			if systemInstruction == nil {
				systemInstruction = map[string]any{"parts": []any{}}
			}
			parts, _ := systemInstruction["parts"].([]any)
			systemInstruction["parts"] = append(parts, map[string]any{"text": text})
		} else {
			role := "user"
			if msg.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": []any{map[string]any{"text": text}},
			})
		}
	}

	temp := 0.0
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	generationConfig := map[string]any{
		"maxOutputTokens": maxTokens,
		"temperature":     temp,
	}
	if req.TopP != nil {
		generationConfig["topP"] = *req.TopP
	}
	if req.Stop != nil {
		var stop any
		json.Unmarshal(req.Stop, &stop)
		switch v := stop.(type) {
		case string:
			generationConfig["stopSequences"] = []string{v}
		case []any:
			generationConfig["stopSequences"] = v
		}
	}

	payload := map[string]any{
		"contents":         contents,
		"generationConfig": generationConfig,
	}
	if systemInstruction != nil {
		payload["systemInstruction"] = systemInstruction
	}
	return payload
}

func (p *GeminiProvider) extractContent(data map[string]any) string {
	candidates, _ := data["candidates"].([]any)
	if len(candidates) == 0 {
		return ""
	}
	cand, _ := candidates[0].(map[string]any)
	content, _ := cand["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	result := ""
	for _, part := range parts {
		m, _ := part.(map[string]any)
		if text, ok := m["text"].(string); ok {
			result += text
		}
	}
	return result
}

func (p *GeminiProvider) extractUsage(data map[string]any) map[string]any {
	usage, _ := data["usageMetadata"].(map[string]any)
	if usage == nil {
		return nil
	}
	prompt := intFromAny(usage, "promptTokenCount")
	completion := intFromAny(usage, "candidatesTokenCount")
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      prompt + completion,
	}
}

// OpenAIChatProvider proxies OpenAI-compatible models directly.
type OpenAIChatProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewOpenAIChatProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *OpenAIChatProvider {
	return &OpenAIChatProvider{client: client, dm: dm}
}

func (p *OpenAIChatProvider) HandleChatCompletion(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, req.Model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	path := fmt.Sprintf("/v2/inference/deployments/%s/chat/completions?api-version=2024-06-01", deploymentID)
	payload := buildOpenAIPayload(&req)

	if req.Stream {
		resp, err := p.client.PostStream(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		// Set SSE headers and proxy the stream directly
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := c.Writer.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				c.Writer.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
	} else {
		resp, err := p.client.Post(ctx, path, payload)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		c.Data(resp.StatusCode, "application/json", body)
	}
}

func buildOpenAIPayload(req *ChatRequest) map[string]any {
	payload := map[string]any{
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.N != nil {
		payload["n"] = *req.N
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	if req.PresencePenalty != nil {
		payload["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		payload["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.Stop != nil {
		payload["stop"] = req.Stop
	}
	if req.LogitBias != nil {
		payload["logit_bias"] = req.LogitBias
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if req.Tools != nil {
		payload["tools"] = req.Tools
	}
	if req.ToolChoice != nil {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Functions != nil {
		payload["functions"] = req.Functions
	}
	if req.FunctionCall != nil {
		payload["function_call"] = req.FunctionCall
	}
	if req.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	return payload
}

// EmbeddingsProvider proxies embedding requests.
type EmbeddingsProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewEmbeddingsProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *EmbeddingsProvider {
	return &EmbeddingsProvider{client: client, dm: dm}
}

func (p *EmbeddingsProvider) HandleEmbeddings(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		sendOpenAIError(c, http.StatusBadRequest, "Missing required parameter: model", "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	path := fmt.Sprintf("/v2/inference/deployments/%s/embeddings?api-version=2024-12-01-preview", deploymentID)
	resp, err := p.client.Post(ctx, path, body)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json", respBody)
}

// ResponsesProvider handles the Responses API with an in-memory FIFO cache.
type ResponsesProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
	cache  responseCache
}

type responseCache struct {
	mu    http.RoundTripper
	data  map[string]string // responseId → deploymentId
	order []string
}

func NewResponsesProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *ResponsesProvider {
	return &ResponsesProvider{
		client: client,
		dm:     dm,
		cache: responseCache{
			data: make(map[string]string),
		},
	}
}

const maxCacheSize = 10_000

// HandleCreate creates a new response via SAP and caches the responseId.
func (p *ResponsesProvider) HandleCreate(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		sendOpenAIError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		sendOpenAIError(c, http.StatusBadRequest, "Missing required parameter: model", "invalid_request_error")
		return
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	path := fmt.Sprintf("/v2/inference/deployments/%s/responses", deploymentID)
	isStream, _ := body["stream"].(bool)

	if isStream {
		resp, err := p.client.PostStream(ctx, path, body)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		flusher, _ := c.Writer.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				c.Writer.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr != nil {
				break
			}
		}
	} else {
		resp, err := p.client.Post(ctx, path, body)
		if err != nil {
			handleUpstreamError(c, err)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)

		var data map[string]any
		json.Unmarshal(respBody, &data)
		if id, ok := data["id"].(string); ok && id != "" {
			p.cacheStore(id, deploymentID)
		}
		c.Data(resp.StatusCode, "application/json", respBody)
	}
}

func (p *ResponsesProvider) HandleGet(c *gin.Context) {
	responseID := c.Param("responseId")
	deploymentID, ok := p.cacheLookup(responseID)
	if !ok {
		sendOpenAIError(c, http.StatusNotFound, "Response not found: "+responseID)
		return
	}

	ctx := bg()
	path := fmt.Sprintf("/v2/inference/deployments/%s/responses/%s", deploymentID, responseID)
	resp, err := p.client.Get(ctx, path)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json", body)
}

func (p *ResponsesProvider) HandleDelete(c *gin.Context) {
	responseID := c.Param("responseId")
	deploymentID, ok := p.cacheLookup(responseID)
	if !ok {
		sendOpenAIError(c, http.StatusNotFound, "Response not found: "+responseID)
		return
	}

	ctx := bg()
	path := fmt.Sprintf("/v2/inference/deployments/%s/responses/%s", deploymentID, responseID)
	resp, err := p.client.Delete(ctx, path)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}
	defer resp.Body.Close()
	p.cacheDelete(responseID)
	c.Status(resp.StatusCode)
}

func (p *ResponsesProvider) cacheStore(id, deploymentID string) {
	if len(p.cache.data) >= maxCacheSize {
		// Evict oldest
		if len(p.cache.order) > 0 {
			oldest := p.cache.order[0]
			delete(p.cache.data, oldest)
			p.cache.order = p.cache.order[1:]
		}
	}
	p.cache.data[id] = deploymentID
	p.cache.order = append(p.cache.order, id)
}

func (p *ResponsesProvider) cacheLookup(id string) (string, bool) {
	v, ok := p.cache.data[id]
	return v, ok
}

func (p *ResponsesProvider) cacheDelete(id string) {
	delete(p.cache.data, id)
	for i, v := range p.cache.order {
		if v == id {
			p.cache.order = append(p.cache.order[:i], p.cache.order[i+1:]...)
			break
		}
	}
}

// AudioProvider handles audio transcription.
type AudioProvider struct {
	client *sapclient.SapClient
	dm     *sapclient.DeploymentManager
}

func NewAudioProvider(client *sapclient.SapClient, dm *sapclient.DeploymentManager) *AudioProvider {
	return &AudioProvider{client: client, dm: dm}
}

func (p *AudioProvider) HandleTranscription(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		sendOpenAIError(c, http.StatusBadRequest, "Missing required file upload", "invalid_request_error")
		return
	}
	defer file.Close()

	model := c.Request.FormValue("model")
	if model == "" {
		model = "whisper"
	}

	ctx := bg()
	deploymentID, err := p.dm.GetDeploymentID(ctx, model)
	if err != nil {
		handleUpstreamError(c, err)
		return
	}

	path := fmt.Sprintf("/v2/inference/deployments/%s/audio/transcriptions?api-version=2024-06-01", deploymentID)

	// Build multipart body
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", header.Filename)
	if err != nil {
		sendOpenAIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	io.Copy(fw, file)

	// Copy other form fields
	c.Request.ParseMultipartForm(32 << 20)
	if c.Request.MultipartForm != nil {
		for key, vals := range c.Request.MultipartForm.Value {
			if key != "file" {
				for _, v := range vals {
					mw.WriteField(key, v)
				}
			}
		}
	}
	mw.Close()

	resp, err := p.client.PostForm(ctx, path, &buf, mw.FormDataContentType())
	if err != nil {
		handleUpstreamError(c, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json", body)
}
