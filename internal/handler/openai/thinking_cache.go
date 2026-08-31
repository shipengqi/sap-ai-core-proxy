package openai

import (
	"bytes"
	"sync"
)

// thinkingUnsupportedCache tracks deployments that don't support adaptive thinking.
// This cache is shared by both Claude (Anthropic) and Qwen handlers to avoid
// sending unsupported "thinking" parameters to upstream models.
// Key: deployment ID, Value: true (if thinking is unsupported)
var thinkingUnsupportedCache sync.Map

// IsThinkingUnsupported checks if a deployment is known to not support thinking.
func IsThinkingUnsupported(deploymentID string) bool {
	_, exists := thinkingUnsupportedCache.Load(deploymentID)
	return exists
}

// MarkThinkingUnsupported caches that a deployment doesn't support thinking.
// Future requests to this deployment will skip adding thinking parameters.
func MarkThinkingUnsupported(deploymentID string) {
	thinkingUnsupportedCache.Store(deploymentID, true)
}

// IsAdaptiveThinkingError checks if an error response indicates that
// adaptive thinking is not supported by the upstream model.
func IsAdaptiveThinkingError(body []byte) bool {
	return bytes.Contains(body, []byte("adaptive thinking is not supported"))
}

// ClearThinkingCache clears the thinking unsupported cache.
// This is primarily for testing purposes.
func ClearThinkingCache() {
	thinkingUnsupportedCache.Range(func(key, value interface{}) bool {
		thinkingUnsupportedCache.Delete(key)
		return true
	})
}
