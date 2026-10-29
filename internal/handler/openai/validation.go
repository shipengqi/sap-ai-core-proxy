package openai

import (
	"encoding/json"
	"fmt"
)

func validateChat(raw map[string]json.RawMessage) error {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw["messages"], &messages) != nil || len(messages) == 0 {
		return fmt.Errorf("messages must be a non-empty array")
	}
	for _, m := range messages {
		var role string
		if json.Unmarshal(m["role"], &role) != nil || role == "" {
			return fmt.Errorf("each message requires a role")
		}
	}
	if v, ok := raw["stream"]; ok {
		var stream bool
		if json.Unmarshal(v, &stream) != nil {
			return fmt.Errorf("stream must be a boolean")
		}
	}
	if v, ok := raw["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(v, &tools) != nil {
			return fmt.Errorf("tools must be an array")
		}
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens", "n"} {
		if v, ok := raw[field]; ok {
			var n int
			if json.Unmarshal(v, &n) != nil || n <= 0 {
				return fmt.Errorf("%s must be a positive integer", field)
			}
		}
	}
	return nil
}

func validateClaudeChat(raw map[string]json.RawMessage, deploymentID string) error {
	for _, field := range []string{"n", "logprobs", "top_logprobs", "presence_penalty", "frequency_penalty", "logit_bias", "seed", "response_format"} {
		if v, ok := raw[field]; ok && string(v) != "null" {
			if field == "n" && string(v) == "1" {
				continue
			}
			return fmt.Errorf("%s cannot be represented by the Claude bridge; use the native Anthropic API", field)
		}
	}
	var stop string
	if v, ok := raw["stop"]; ok {
		if json.Unmarshal(v, &stop) != nil {
			var list []string
			if json.Unmarshal(v, &list) != nil {
				return fmt.Errorf("stop must be a string or string array")
			}
		}
	}
	if IsThinkingUnsupported(deploymentID) {
		return nil
	}
	var effort string
	if v, ok := raw["reasoning_effort"]; ok {
		if json.Unmarshal(v, &effort) != nil {
			return fmt.Errorf("reasoning_effort must be a string")
		}
		if effort != "" && effort != "none" {
			budget, ok := reasoningBudgets[effort]
			if !ok {
				return fmt.Errorf("unsupported reasoning_effort %q", effort)
			}
			v := raw["max_completion_tokens"]
			if v == nil {
				v = raw["max_tokens"]
			}
			if v != nil {
				var limit int
				_ = json.Unmarshal(v, &limit)
				if limit <= budget {
					return fmt.Errorf("token limit %d must exceed the reasoning budget %d; increase the limit or lower reasoning_effort", limit, budget)
				}
			}
		}
	}
	return nil
}

func validateResponsesBridge(raw map[string]json.RawMessage) error {
	var effort string
	if json.Unmarshal(raw["reasoning_effort"], &effort) == nil && effort == "none" {
		return fmt.Errorf("reasoning_effort:none cannot be preserved by this Responses bridge; use the Chat endpoint or the native Responses API")
	}
	for _, field := range []string{"n", "stop", "logprobs", "top_logprobs", "presence_penalty", "frequency_penalty", "logit_bias", "seed"} {
		if v, ok := raw[field]; ok && string(v) != "null" {
			if field == "n" && string(v) == "1" {
				continue
			}
			return fmt.Errorf("%s cannot be represented by the Responses bridge; use the native Responses API", field)
		}
	}
	var tools []struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw["tools"], &tools)
	for _, t := range tools {
		if t.Type != "function" {
			return fmt.Errorf("only function tools can be bridged to Responses")
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(raw["messages"], &messages)
	for _, m := range messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type != "text" && p.Type != "image_url" {
					return fmt.Errorf("content type %q cannot be bridged to Responses", p.Type)
				}
			}
		}
	}
	return nil
}

func validateGLMChat(raw map[string]json.RawMessage) error {
	for _, field := range []string{"reasoning_effort", "n", "logprobs", "top_logprobs", "presence_penalty", "frequency_penalty", "logit_bias", "seed", "response_format"} {
		if v, ok := raw[field]; ok && string(v) != "null" {
			if field == "n" && string(v) == "1" {
				continue
			}
			return fmt.Errorf("%s cannot be represented by the GLM bridge; use the native provider API", field)
		}
	}
	return nil
}
