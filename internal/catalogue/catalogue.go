// Package catalogue maps user-facing model names to SAP AI Core deployment names
// and provides provider detection helpers.
package catalogue

import "strings"

// aliases maps normalised user-facing model names to the SAP AI Core deployment
// model name used during deployment discovery.
// Source: sap_ai_core_helper.py _MODEL_ALIASES.
var aliases = map[string]string{
	// Claude 4 family — map to both versioned and latest SAP names
	"claude-opus-4-5":            "anthropic--claude-4.5-opus",
	"claude-opus-4.5":            "anthropic--claude-4.5-opus",
	"claude-opus-4-8":            "anthropic--claude-4.8-opus",
	"claude-opus-4.8":            "anthropic--claude-4.8-opus",
	"claude-opus-latest":         "anthropic--claude-4.7-opus",
	"claude-sonnet-4-5":          "anthropic--claude-4.5-sonnet",
	"claude-sonnet-4.5":          "anthropic--claude-4.5-sonnet",
	"claude-sonnet-4-5-20251022": "anthropic--claude-4.5-sonnet",
	"claude-sonnet-latest":       "anthropic--claude-4.5-sonnet",
	"claude-haiku-4-5":           "anthropic--claude-4.5-haiku",
	"claude-haiku-4.5":           "anthropic--claude-4.5-haiku",
	"claude-haiku-latest":        "anthropic--claude-4.5-haiku",

	// Claude 3.7 family
	"claude-3-7-sonnet-latest":            "anthropic--claude-3.7-sonnet",
	"claude-3-7-sonnet-20250219":          "anthropic--claude-3.7-sonnet",
	"claude-3-7-sonnet-20250219:thinking": "anthropic--claude-3.7-sonnet",

	// Claude 3.5 family
	"claude-3-5-sonnet-latest":   "anthropic--claude-3.5-sonnet",
	"claude-3-5-sonnet-20241022": "anthropic--claude-3.5-sonnet",
	"claude-3-5-sonnet-20240620": "anthropic--claude-3.5-sonnet",
	"claude-3-5-haiku-latest":    "anthropic--claude-3.5-haiku",
	"claude-3-5-haiku-20241022":  "anthropic--claude-3.5-haiku",

	// Claude 3 family
	"claude-3-opus-latest":     "anthropic--claude-3-opus",
	"claude-3-opus-20240229":   "anthropic--claude-3-opus",
	"claude-3-sonnet-20240229": "anthropic--claude-3-sonnet",
	"claude-3-haiku-20240307":  "anthropic--claude-3-haiku",
}

// Normalize translates a user-facing model name to its SAP AI Core deployment
// model name. If no alias exists the original name is returned unchanged.
func Normalize(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if v, ok := aliases[lower]; ok {
		return v
	}
	return model
}

// IsAnthropic returns true for Claude model names.
func IsAnthropic(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "claude-") || strings.HasPrefix(m, "anthropic--claude")
}

// IsGemini returns true for Gemini model names.
func IsGemini(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-")
}

// IsOpenAI returns true for GPT / O-series model names.
func IsOpenAI(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gpt-") ||
		strings.HasPrefix(m, "o1-") ||
		strings.HasPrefix(m, "o3-") ||
		strings.HasPrefix(m, "o4-") ||
		m == "o1" || m == "o3" || m == "o4"
}

// IsTitan returns true for Amazon Titan image model names.
func IsTitan(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "amazon--titan") || strings.HasPrefix(m, "titan-image")
}

// IsQwen returns true for Alibaba Qwen model names.
func IsQwen(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "qwen")
}

// IsPerplexity returns true for Perplexity Sonar model names.
func IsPerplexity(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "sonar")
}

// IsGlm returns true for Z.ai GLM model names.
func IsGlm(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "zai--")
}
