package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

func TestProxyRegressionGeminiToolDefinitions(t *testing.T) {
	raw := mustRaw(t, `{"messages":[{"role":"user","content":"call f"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`)
	body := openAIToGeminiBody(raw)
	if !strings.Contains(string(body), `"functionDeclarations"`) {
		t.Fatalf("tool definitions disappeared: %s", body)
	}
}

func TestGLMToolDefinitionsAndChoiceSurviveConversion(t *testing.T) {
	raw := mustRaw(t, `{"model":"zai--glm-4.6","messages":[{"role":"user","content":"use f"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"f"}},"max_tokens":50,"max_completion_tokens":70}`)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(openAIToGLMBody(raw, "zai--glm-4.6", false), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body["tools"]), `"input_schema"`) || strings.Contains(string(body["tools"]), `"function"`) || !strings.Contains(string(body["tool_choice"]), `"type":"tool"`) || string(body["max_tokens"]) != "70" {
		t.Fatal(string(body["tools"]), string(body["tool_choice"]), string(body["max_tokens"]))
	}
}

func TestCapabilityCacheScopeAndExpiry(t *testing.T) {
	ClearCapabilityCaches()
	t.Cleanup(ClearCapabilityCaches)
	a := &sapclient.Deployment{ID: "shared-id", DeployedURL: "https://a/deployment", ModelName: "gpt-6.1-sol"}
	b := *a
	b.DeployedURL = "https://b/deployment"
	key := CapabilityKey(a, chatPath)
	MarkResponsesRequired(key)
	if !IsResponsesRequired(key) || IsResponsesRequired(CapabilityKey(&b, chatPath)) || IsResponsesRequired(CapabilityKey(a, "/chat/completions?api-version=other")) {
		t.Fatal("capability escaped its deployment/endpoint scope")
	}
	responsesRequiredCache.entries.Store(key, time.Now().Add(-time.Second))
	if IsResponsesRequired(key) {
		t.Fatal("expired capability remained active")
	}
}

func TestResponseAffinityExpires(t *testing.T) {
	r := responseRegistry{routes: make(map[string]responseRoute)}
	r.remember("r", &sapclient.Deployment{ID: "a"})
	if _, ok := r.lookup("r"); !ok {
		t.Fatal("missing newly stored route")
	}
	v := r.routes["r"]
	v.Expires = time.Now().Add(-time.Second)
	r.routes["r"] = v
	if _, ok := r.lookup("r"); ok {
		t.Fatal("expired affinity route accepted")
	}
}

func TestResponseAffinityUpdateAtCapacityRetainsOtherIDs(t *testing.T) {
	r := responseRegistry{routes: make(map[string]responseRoute)}
	for i := 0; i < maxResponseRoutes; i++ {
		r.routes[fmt.Sprint(i)] = responseRoute{Deployment: sapclient.Deployment{ID: "a"}, Expires: time.Now().Add(time.Hour)}
	}
	r.remember(fmt.Sprint(maxResponseRoutes-1), &sapclient.Deployment{ID: "a"})
	if len(r.routes) != maxResponseRoutes {
		t.Fatal("updating an existing response evicted a different response")
	}
}
func TestProxyRegressionGeminiToolOutput(t *testing.T) {
	body := geminiToOpenAIResponse([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{"x":1}}}]},"finishReason":"STOP"}]}`), "gemini-pro")
	if !strings.Contains(string(body), `"tool_calls"`) {
		t.Fatalf("tool call became empty text completion: %s", body)
	}
}
func TestProxyRegressionClaudeTemperature(t *testing.T) {
	raw := mustRaw(t, `{"messages":[{"role":"user","content":"hi"}],"temperature":0.2}`)
	body, _ := openAIToBedrockBody(raw, "audit", false, false)
	if !strings.Contains(string(body), `"temperature"`) {
		t.Fatalf("temperature disappeared: %s", body)
	}
}
func TestProxyRegressionClaudeTokenLimit(t *testing.T) {
	raw := mustRaw(t, `{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":64,"reasoning_effort":"high"}`)
	body, _ := openAIToBedrockBody(raw, "audit", false, false)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["max_tokens"]) != "64" {
		t.Fatalf("client token limit 64 silently became %s", m["max_tokens"])
	}
}
func TestProxyRegressionGLMStopString(t *testing.T) {
	raw := mustRaw(t, `{"model":"zai--glm-4.6","messages":[{"role":"user","content":"hi"}],"stop":"END"}`)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(openAIToGLMBody(raw, "zai--glm-4.6", false), &m); err != nil {
		t.Fatal(err)
	}
	var stop []string
	if err := json.Unmarshal(m["stop_sequences"], &stop); err != nil {
		t.Fatalf("stop_sequences must be array, got %s", m["stop_sequences"])
	}
}
func TestProxyRegressionFailedResponsesCompletion(t *testing.T) {
	body := responsesToChatCompletion([]byte(`{"id":"resp_fail","status":"failed","error":{"code":"server_error","message":"failed"},"output":[]}`), "m")
	if strings.Contains(string(body), `"finish_reason":"stop"`) {
		t.Fatalf("upstream failed response became successful completion: %s", body)
	}
}

func TestBridgeRejectsUnrepresentableReasoning(t *testing.T) {
	raw := mustRaw(t, `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if validateResponsesBridge(raw) == nil {
		t.Fatal("explicit none would be replaced by the Responses default")
	}
	raw["reasoning_effort"] = json.RawMessage(`"high"`)
	if validateGLMChat(raw) == nil {
		t.Fatal("GLM would silently discard reasoning effort")
	}
}

func TestUnknownIncompleteReasonCannotExecuteTools(t *testing.T) {
	body := responsesToChatCompletion([]byte(`{"status":"incomplete","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"{"}]}`), "m")
	if !strings.Contains(string(body), `"finish_reason":"length"`) {
		t.Fatal(string(body))
	}
}
