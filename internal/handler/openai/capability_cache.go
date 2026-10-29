package openai

import (
	"bytes"
	"sync"
)

// Both caches below key on deployment ID, like the thinking cache: the
// capability belongs to the model behind the deployment, not to the client.

// effortUnsupportedCache records deployments whose OpenAI-compatible
// /chat/completions endpoint does not know the reasoning_effort argument at all
// (gpt-4o answers 400 "Unrecognized request argument supplied").
var effortUnsupportedCache sync.Map

// responsesRequiredCache records deployments that refuse function tools on
// /chat/completions and accept them only on /responses (gpt-6.x).
var responsesRequiredCache sync.Map

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

// IsResponsesRequired reports whether tool-bearing requests to this deployment
// have to be bridged to /responses.
func IsResponsesRequired(deploymentID string) bool {
	_, exists := responsesRequiredCache.Load(deploymentID)
	return exists
}

// MarkResponsesRequired remembers that the deployment refuses tools on
// /chat/completions.
func MarkResponsesRequired(deploymentID string) {
	responsesRequiredCache.Store(deploymentID, true)
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

// IsResponsesRequiredError matches the 400 a model returns when it accepts
// function tools only on the Responses API. The message names both the problem
// and the way out ("use /v1/responses"), which is exactly what the bridge does.
func IsResponsesRequiredError(body []byte) bool {
	return bytes.Contains(body, []byte("To use function tools, use /v1/responses")) ||
		bytes.Contains(body, []byte("Function tools with reasoning_effort are not supported"))
}

// ClearCapabilityCaches clears both caches. For tests only.
func ClearCapabilityCaches() {
	effortUnsupportedCache.Range(func(key, _ interface{}) bool {
		effortUnsupportedCache.Delete(key)
		return true
	})
	responsesRequiredCache.Range(func(key, _ interface{}) bool {
		responsesRequiredCache.Delete(key)
		return true
	})
}

// ClearEffortCache clears only the effort cache. For tests only.
func ClearEffortCache() {
	effortUnsupportedCache.Range(func(key, _ interface{}) bool {
		effortUnsupportedCache.Delete(key)
		return true
	})
}
