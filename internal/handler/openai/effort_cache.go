package openai

import (
	"bytes"
	"sync"
)

// effortUnsupportedCache records deployments whose OpenAI-compatible
// /chat/completions endpoint does not know the reasoning_effort argument at all
// (gpt-4o and friends answer 400 "Unrecognized request argument supplied").
// Keyed by deployment ID, like the thinking cache: the capability belongs to the
// model behind the deployment, not to the client.
var effortUnsupportedCache sync.Map

// IsEffortUnsupported reports whether reasoning_effort must be stripped before
// forwarding to this deployment.
func IsEffortUnsupported(deploymentID string) bool {
	_, exists := effortUnsupportedCache.Load(deploymentID)
	return exists
}

// MarkEffortUnsupported remembers that the deployment rejects reasoning_effort.
func MarkEffortUnsupported(deploymentID string) {
	effortUnsupportedCache.Store(deploymentID, true)
}

// IsUnsupportedEffortError matches the 400 a model returns when reasoning_effort
// is not a known argument. It deliberately does not match the other
// reasoning_effort error — the one that rejects "none" for models which do
// support the field — because stripping the field cannot fix that case, and
// marking the deployment would silently disable effort everywhere else.
func IsUnsupportedEffortError(body []byte) bool {
	return bytes.Contains(body, []byte("Unrecognized request argument")) &&
		bytes.Contains(body, []byte("reasoning_effort"))
}

// ClearEffortCache clears the cache. For tests only.
func ClearEffortCache() {
	effortUnsupportedCache.Range(func(key, _ interface{}) bool {
		effortUnsupportedCache.Delete(key)
		return true
	})
}
