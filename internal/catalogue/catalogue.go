// Package catalogue maps user-facing model names to SAP AI Core deployment names
// and provides provider detection helpers.
package catalogue

import (
	"regexp"
	"strings"
)

// aliases maps normalised user-facing model names to the SAP AI Core deployment
// model name used during deployment discovery.
// Source: sap_ai_core_helper.py _MODEL_ALIASES.
var aliases = map[string]string{
	// Claude 4 family. Versioned names are pinned here; the bare family-level
	// "-latest" aliases (claude-opus-latest, claude-sonnet-latest,
	// claude-haiku-latest) are deliberately absent. DeploymentManager resolves
	// those against the live deployment list so they always mean the newest
	// running version — pinning them here would freeze them at whatever release
	// happened to be current when this table was last edited.
	"claude-opus-4-5":            "anthropic--claude-4.5-opus",
	"claude-opus-4.5":            "anthropic--claude-4.5-opus",
	"claude-opus-4-8":            "anthropic--claude-4.8-opus",
	"claude-opus-4.8":            "anthropic--claude-4.8-opus",
	"claude-sonnet-4-6":          "anthropic--claude-4.6-sonnet",
	"claude-sonnet-4.6":          "anthropic--claude-4.6-sonnet",
	"claude-sonnet-4-5":          "anthropic--claude-4.5-sonnet",
	"claude-sonnet-4.5":          "anthropic--claude-4.5-sonnet",
	"claude-sonnet-4-5-20251022": "anthropic--claude-4.5-sonnet",
	"claude-haiku-4-5":           "anthropic--claude-4.5-haiku",
	"claude-haiku-4.5":           "anthropic--claude-4.5-haiku",

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
// model name. Resolution order:
//
//  1. strip context-window suffixes like [1m] or [200k]
//  2. the explicit alias table above
//  3. a generated SAP-style name (see sapStyleName) for Claude models that are
//     not in the table yet
//  4. the original name (with suffix removed for non-Claude names)
func Normalize(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	stripped := contextSuffixPattern.ReplaceAllString(lower, "")

	if v, ok := aliases[stripped]; ok {
		return v
	}
	if v := sapStyleName(stripped); v != "" {
		return v
	}
	// For non-Claude models, strip the suffix anyway — it is meaningless
	// to upstream providers and would prevent deployment matching.
	if stripped != lower {
		return stripped
	}
	return model
}

// contextSuffixPattern matches bracket suffixes like [1m], [200k] used by
// Claude Code to request specific context window sizes.
var contextSuffixPattern = regexp.MustCompile(`\[[^\]]+\]$`)

// sapStylePatterns recognise Anthropic's user-facing Claude naming and capture
// the pieces needed to rebuild SAP AI Core's "<major>.<minor>-<family>" form.
var (
	// New convention (Claude 4.x): claude-<family>-<major>-<minor>[-<snapshot>]
	//   claude-sonnet-4-6          -> anthropic--claude-4.6-sonnet
	//   claude-sonnet-4-6-20260115 -> anthropic--claude-4.6-sonnet
	reFamilyFirst = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d+)-(\d+)(?:-\d{8})?$`)
	// Legacy convention: claude-<major>-<minor>-<family>[-latest|-<snapshot>]
	//   claude-3-5-sonnet-latest   -> anthropic--claude-3.5-sonnet
	reVersionFirst = regexp.MustCompile(`^claude-(\d+)-(\d+)-(opus|sonnet|haiku)(?:-(?:latest|\d{8}))?$`)
	// Legacy convention, single version: claude-<major>-<family>[-latest|-<snapshot>]
	//   claude-3-opus-latest       -> anthropic--claude-3-opus
	reMajorOnly = regexp.MustCompile(`^claude-(\d+)-(opus|sonnet|haiku)(?:-(?:latest|\d{8}))?$`)
	// Bare family-level alias, e.g. claude-opus-latest. These are resolved
	// against the live deployment list (see DeploymentManager.findDeployment)
	// so they always mean the newest version actually running.
	reLatestFamily = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-latest$`)
)

// sapStyleName rebuilds SAP AI Core's deployment name from an Anthropic-style
// model name. It is the fallback that keeps newly released Claude versions
// working without an alias entry.
//
// This translation cannot be left to the fuzzy matchers in DeploymentManager:
// SAP writes the version first ("anthropic--claude-4.6-sonnet") while Anthropic
// writes the family first ("claude-sonnet-4-6"), so neither substring nor
// family-prefix matching can bridge the two orderings.
//
// Returns "" when the name matches no known shape.
func sapStyleName(name string) string {
	if m := reFamilyFirst.FindStringSubmatch(name); m != nil {
		return "anthropic--claude-" + m[2] + "." + m[3] + "-" + m[1]
	}
	if m := reVersionFirst.FindStringSubmatch(name); m != nil {
		return "anthropic--claude-" + m[1] + "." + m[2] + "-" + m[3]
	}
	if m := reMajorOnly.FindStringSubmatch(name); m != nil {
		return "anthropic--claude-" + m[1] + "-" + m[2]
	}
	return ""
}

// LatestFamily reports whether model is a bare family-level "-latest" alias
// (e.g. "claude-opus-latest") and, if so, which family it names.
//
// Version-pinned legacy aliases such as "claude-3-5-sonnet-latest" are
// deliberately not matched: those name one specific version, not a moving
// target. Bare family-level aliases have no entry in the table above at all —
// this is the only path that resolves them, against the live deployment list.
func LatestFamily(model string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(model))
	stripped := contextSuffixPattern.ReplaceAllString(lower, "")
	m := reLatestFamily.FindStringSubmatch(stripped)
	if m == nil {
		return "", false
	}
	return m[1], true
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
