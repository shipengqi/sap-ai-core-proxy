package anthropic

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// CountTokens implements the Anthropic token-counting endpoint used by Claude
// Code. SAP AI Core does not expose Anthropic's tokenizer, so this endpoint
// returns a conservative local estimate. Claude Code uses the value for context
// budgeting; generation still goes through /messages and SAP reports exact usage.
func (h *Handler) CountTokens(c *gin.Context) {
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

	var model string
	if value, ok := raw["model"]; ok {
		_ = json.Unmarshal(value, &model)
	}
	if model == "" {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: model"))
		return
	}
	c.Set("model", model)
	usage.MarkNoUsageExpected(c)

	// Count prompt-bearing fields rather than JSON syntax and transport fields.
	// Four UTF-8 bytes per token is a useful English/code approximation; add a
	// small per-message allowance for role and content framing.
	bytes := countJSONText(raw["system"]) + countJSONText(raw["messages"]) + countJSONText(raw["tools"])
	messages := countJSONArray(raw["messages"])
	tokens := (bytes + 3) / 4
	tokens += messages * 4
	if tokens < 1 {
		tokens = 1
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": tokens})
}

func countJSONText(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	return countTextValue(value)
}

func countTextValue(value any) int {
	switch v := value.(type) {
	case string:
		return len(v)
	case []any:
		total := 0
		for _, item := range v {
			total += countTextValue(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range v {
			if key == "type" || key == "role" || key == "cache_control" {
				continue
			}
			total += countTextValue(item)
		}
		return total
	default:
		return 0
	}
}

func countJSONArray(raw json.RawMessage) int {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return 0
	}
	return len(values)
}
