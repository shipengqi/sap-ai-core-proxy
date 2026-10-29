package openai

import (
	"bytes"
)

// thinkingUnsupportedCache tracks deployments that don't support adaptive thinking.
// This cache is shared by both Claude (Anthropic) and Qwen handlers to avoid
// sending unsupported "thinking" parameters to upstream models.
// Keys come from CapabilityKey; values expire after capabilityTTL.
var thinkingUnsupportedCache capabilityCache

// IsThinkingUnsupported checks if a deployment is known to not support thinking.
func IsThinkingUnsupported(key string) bool {
	return thinkingUnsupportedCache.has(key)
}

// MarkThinkingUnsupported caches that a deployment doesn't support thinking.
// Future requests to this deployment will skip adding thinking parameters.
func MarkThinkingUnsupported(key string) {
	thinkingUnsupportedCache.mark(key)
}

// IsAdaptiveThinkingError checks if an error response indicates that
// adaptive thinking is not supported by the upstream model.
func IsAdaptiveThinkingError(body []byte) bool {
	return bytes.Contains(body, []byte("adaptive thinking is not supported"))
}

// ClearThinkingCache clears the thinking unsupported cache.
// This is primarily for testing purposes.
func ClearThinkingCache() {
	thinkingUnsupportedCache.clear()
}
