package transform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeminiParallelToolHistoryAndSignature(t *testing.T) {
	input := []byte(`{"messages":[{"role":"system","content":"first"},{"role":"developer","content":"second"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"x\":1}"},"extra_content":{"google":{"thought_signature":"signed"}}},{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c2","content":"two"},{"role":"tool","tool_call_id":"c1","content":"{\"value\":1}"}]}`)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(input, &raw)
	body, err := OpenAIToGeminiBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Contents []GeminiContent `json:"contents"`
		System   GeminiContent   `json:"systemInstruction"`
	}
	if json.Unmarshal(body, &out) != nil {
		t.Fatal(string(body))
	}
	if len(out.System.Parts) != 2 || len(out.Contents) != 2 || len(out.Contents[1].Parts) != 2 {
		t.Fatalf("history lost: %s", body)
	}
	if out.Contents[0].Parts[0].ThoughtSignature != "signed" {
		t.Fatal("signature lost")
	}
	results := out.Contents[1].Parts
	if results[0].FunctionResponse.ID != "c2" || results[1].FunctionResponse.ID != "c1" {
		t.Fatal("parallel call IDs were reordered or lost")
	}
}
func TestGeminiResponseToolsReasoningAndLimits(t *testing.T) {
	body := GeminiToOpenAIResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"reason","thought":true},{"functionCall":{"name":"f","args":{"x":1}},"thoughtSignature":"signed"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4}}`), "m")
	for _, want := range []string{`"finish_reason":"length"`, `"reasoning_content":"reason"`, `"thought_signature":"signed"`, `"completion_tokens":6`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %s: %s", want, body)
		}
	}
}
