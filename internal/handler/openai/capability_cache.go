package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

const capabilityTTL = time.Hour

type capabilityCache struct{ entries sync.Map }

func (c *capabilityCache) has(key string) bool {
	v, ok := c.entries.Load(key)
	if !ok {
		return false
	}
	if time.Now().After(v.(time.Time)) {
		c.entries.CompareAndDelete(key, v)
		return false
	}
	return true
}
func (c *capabilityCache) mark(key string) {
	now := time.Now()
	c.entries.Range(func(k, v any) bool {
		if now.After(v.(time.Time)) {
			c.entries.CompareAndDelete(k, v)
		}
		return true
	})
	c.entries.Store(key, now.Add(capabilityTTL))
}
func (c *capabilityCache) clear() {
	c.entries.Range(func(k, _ any) bool { c.entries.Delete(k); return true })
}

// CapabilityKey prevents observations from leaking between SAP environments,
// replaced models, resource groups, endpoints, or API versions.
func CapabilityKey(dep *sapclient.Deployment, endpoint string) string {
	b, _ := json.Marshal([]string{dep.ID, dep.DeployedURL, dep.EffectiveModelName(), dep.ResourceGroupID, endpoint})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// effortUnsupportedCache records deployments whose OpenAI-compatible
// /chat/completions endpoint does not know the reasoning_effort argument at all
// (gpt-4o answers 400 "Unrecognized request argument supplied").
var effortUnsupportedCache capabilityCache

// responsesRequiredCache records deployments that refuse function tools on
// /chat/completions and accept them only on /responses (gpt-6.x).
var responsesRequiredCache capabilityCache

// IsEffortUnsupported reports whether reasoning_effort must be stripped before
// forwarding to this deployment.
func IsEffortUnsupported(key string) bool {
	return effortUnsupportedCache.has(key)
}

// MarkEffortUnsupported remembers that the deployment rejects reasoning_effort.
func MarkEffortUnsupported(key string) {
	effortUnsupportedCache.mark(key)
}

// IsResponsesRequired reports whether tool-bearing requests to this deployment
// have to be bridged to /responses.
func IsResponsesRequired(key string) bool {
	return responsesRequiredCache.has(key)
}

// MarkResponsesRequired remembers that the deployment refuses tools on
// /chat/completions.
func MarkResponsesRequired(key string) {
	responsesRequiredCache.mark(key)
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
	effortUnsupportedCache.clear()
	responsesRequiredCache.clear()
}

// ClearEffortCache clears only the effort cache. For tests only.
func ClearEffortCache() {
	effortUnsupportedCache.clear()
}
