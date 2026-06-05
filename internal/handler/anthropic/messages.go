package anthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

var allowedFields = map[string]bool{
	"anthropic_version": true,
	"messages":          true,
	"system":            true,
	"max_tokens":        true,
	"temperature":       true,
	"top_p":             true,
	"top_k":             true,
	"stop_sequences":    true,
	"tools":             true,
	"tool_choice":       true,
	"metadata":          true,
	"thinking":          true,
}

// Messages proxies POST /anthropic/v1/messages to SAP AI Core.
func (h *Handler) Messages(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		c.JSON(http.StatusBadRequest, errorBody("invalid JSON: "+err.Error()))
		return
	}

	var modelStr string
	if m, ok := raw["model"]; ok {
		_ = json.Unmarshal(m, &modelStr)
	}
	if modelStr == "" {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: model"))
		return
	}
	if _, ok := raw["max_tokens"]; !ok {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: max_tokens"))
		return
	}
	modelName := modelStr

	var streaming bool
	if s, ok := raw["stream"]; ok {
		_ = json.Unmarshal(s, &streaming)
	}

	filtered := make(map[string]json.RawMessage)
	for k, v := range raw {
		if allowedFields[k] {
			filtered[k] = v
		}
	}

	if _, ok := filtered["anthropic_version"]; !ok {
		v, _ := json.Marshal("bedrock-2023-05-31")
		filtered["anthropic_version"] = v
	}

	filtered = promoteSystemMessages(filtered)
	filtered = stripCacheControl(filtered)

	filteredBody, err := json.Marshal(filtered)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("marshal filtered body: "+err.Error()))
		return
	}

	if streaming {
		// Streaming: single attempt (can't retry after body starts flowing).
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(filteredBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropic(c, upstream)
		return
	}

	// Non-streaming: retry on 404 / Gone across all matching deployments.
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelName, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				dep.DeployedURL+"/invoke", bytes.NewReader(filteredBody), nil)
			if err != nil {
				return 0, nil, err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, b, nil
		},
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	c.Data(status, "application/json", respBody)
}

func promoteSystemMessages(m map[string]json.RawMessage) map[string]json.RawMessage {
	msgsRaw, ok := m["messages"]
	if !ok {
		return m
	}

	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	var msgs []message
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return m
	}

	var systemParts []string
	var remaining []message
	for _, msg := range msgs {
		if msg.Role == "system" {
			var s string
			if err := json.Unmarshal(msg.Content, &s); err == nil {
				systemParts = append(systemParts, s)
			}
		} else {
			remaining = append(remaining, msg)
		}
	}

	if len(systemParts) == 0 {
		return m
	}

	combined := ""
	if existing, ok := m["system"]; ok {
		var s string
		if err := json.Unmarshal(existing, &s); err == nil && s != "" {
			combined = s + "\n"
		}
	}
	for i, p := range systemParts {
		if i > 0 {
			combined += "\n"
		}
		combined += p
	}

	sysJSON, _ := json.Marshal(combined)
	m["system"] = sysJSON
	newMsgs, _ := json.Marshal(remaining)
	m["messages"] = newMsgs
	return m
}

// stripCacheControl removes the cache_control field from system content blocks and
// from message content arrays. SAP AI Core (Bedrock format) does not support
// Anthropic's prompt caching extension and will reject requests containing it.
func stripCacheControl(m map[string]json.RawMessage) map[string]json.RawMessage {
	// Strip from system if it's an array of content blocks.
	if sys, ok := m["system"]; ok {
		if stripped, changed := removeFieldFromBlocks(sys); changed {
			m["system"] = stripped
		}
	}

	// Strip from each message's content if it's an array of content blocks.
	if msgsRaw, ok := m["messages"]; ok {
		var msgs []json.RawMessage
		if err := json.Unmarshal(msgsRaw, &msgs); err == nil {
			changed := false
			for i, msg := range msgs {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(msg, &obj); err != nil {
					continue
				}
				if content, ok := obj["content"]; ok {
					if stripped, c := removeFieldFromBlocks(content); c {
						obj["content"] = stripped
						b, _ := json.Marshal(obj)
						msgs[i] = b
						changed = true
					}
				}
			}
			if changed {
				b, _ := json.Marshal(msgs)
				m["messages"] = b
			}
		}
	}
	return m
}

// removeFieldFromBlocks removes "cache_control" from each element of a JSON array.
// Returns (result, changed). If the value is not an array, returns it unchanged.
func removeFieldFromBlocks(raw json.RawMessage) (json.RawMessage, bool) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return raw, false
	}
	changed := false
	for i, block := range blocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(block, &obj); err != nil {
			continue
		}
		if _, ok := obj["cache_control"]; ok {
			delete(obj, "cache_control")
			b, _ := json.Marshal(obj)
			blocks[i] = b
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	b, _ := json.Marshal(blocks)
	return b, true
}
