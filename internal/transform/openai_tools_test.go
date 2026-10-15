package transform

import (
	"encoding/json"
	"reflect"
	"testing"
)

func convertMessages(t *testing.T, messagesJSON string) []map[string]any {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"messages":`+messagesJSON+`}`), &m); err != nil {
		t.Fatalf("build input: %v", err)
	}
	ConvertOpenAIToolMessagesToAnthropic(m)

	var out []map[string]any
	if err := json.Unmarshal(m["messages"], &out); err != nil {
		t.Fatalf("decode converted messages: %v", err)
	}
	return out
}

func contentBlocksOf(t *testing.T, msg map[string]any) []map[string]any {
	t.Helper()
	raw, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content is not a block array: %v", msg["content"])
	}
	blocks := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		block, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("content block is not an object: %v", b)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// TestConvertOpenAIToolMessagesToAnthropic reproduces the reported failure: an
// OpenAI tool-calling turn must never reach Anthropic with a "tool" role.
func TestConvertOpenAIToolMessagesToAnthropic(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"system","content":"you are a helper"},
		{"role":"user","content":"what is the weather in Berlin?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"{\"temp_c\":14}"}
	]`)

	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(msgs), msgs)
	}
	for i, m := range msgs {
		if role := m["role"]; role == "tool" || role == "function" {
			t.Fatalf("messages[%d] still uses the OpenAI role %v", i, role)
		}
		if _, ok := m["tool_calls"]; ok {
			t.Fatalf("messages[%d] still carries the OpenAI tool_calls field", i)
		}
	}
	if msgs[0]["role"] != "system" {
		t.Errorf("system message was not preserved: %v", msgs[0])
	}
	if msgs[1]["content"] != "what is the weather in Berlin?" {
		t.Errorf("plain user message was rewritten: %v", msgs[1])
	}

	assistant := contentBlocksOf(t, msgs[2])
	if len(assistant) != 1 || assistant[0]["type"] != "tool_use" {
		t.Fatalf("expected a single tool_use block, got %v", assistant)
	}
	if assistant[0]["id"] != "call_1" || assistant[0]["name"] != "get_weather" {
		t.Errorf("unexpected tool_use block: %v", assistant[0])
	}
	input, _ := assistant[0]["input"].(map[string]any)
	if input["city"] != "Berlin" {
		t.Errorf("tool arguments were not parsed into input: %v", assistant[0]["input"])
	}

	if msgs[3]["role"] != "user" {
		t.Fatalf("expected the tool message to become a user message, got %v", msgs[3]["role"])
	}
	results := contentBlocksOf(t, msgs[3])
	if len(results) != 1 || results[0]["type"] != "tool_result" {
		t.Fatalf("expected a single tool_result block, got %v", results)
	}
	if results[0]["tool_use_id"] != "call_1" {
		t.Errorf("tool_result must reference the tool_use id, got %v", results[0]["tool_use_id"])
	}
	if results[0]["content"] != `{"temp_c":14}` {
		t.Errorf("unexpected tool_result content: %v", results[0]["content"])
	}
}

func TestConvertOpenAIToolMessages_MergesConsecutiveResults(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"user","content":"weather and time?"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}},
			{"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{\"city\":\"Berlin\"}"}}
		]},
		{"role":"tool","tool_call_id":"call_a","content":"14C"},
		{"role":"tool","tool_call_id":"call_b","content":"12:55"}
	]`)

	if len(msgs) != 3 {
		t.Fatalf("expected the two tool results to merge into one message, got %d: %v", len(msgs), msgs)
	}
	results := contentBlocksOf(t, msgs[2])
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %v", results)
	}
	if results[0]["tool_use_id"] != "call_a" || results[1]["tool_use_id"] != "call_b" {
		t.Errorf("tool_result ids are out of order: %v", results)
	}
}

func TestConvertOpenAIToolMessages_AssistantTextBeforeToolUse(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"assistant","content":"Let me check.","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"14C"}
	]`)

	blocks := contentBlocksOf(t, msgs[0])
	if len(blocks) != 2 {
		t.Fatalf("expected a text block followed by a tool_use block, got %v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "Let me check." {
		t.Errorf("assistant text must precede tool_use, got %v", blocks[0])
	}
	if blocks[1]["type"] != "tool_use" {
		t.Errorf("expected tool_use second, got %v", blocks[1])
	}
}

func TestConvertOpenAIToolMessages_NonObjectArguments(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"f","arguments":"not json"}}]},
		{"role":"tool","tool_call_id":"call_1"}
	]`)

	blocks := contentBlocksOf(t, msgs[0])
	input, ok := blocks[0]["input"].(map[string]any)
	if !ok || len(input) != 0 {
		t.Errorf("invalid arguments must degrade to an empty object, got %v", blocks[0]["input"])
	}

	results := contentBlocksOf(t, msgs[1])
	if results[0]["content"] != "" {
		t.Errorf("missing tool content must become an empty string, got %v", results[0]["content"])
	}
}

func TestConvertOpenAIToolMessages_LegacyFunctionRole(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"function","name":"get_weather","content":"14C"}
	]`)

	if msgs[1]["role"] != "user" {
		t.Fatalf("expected the function message to become a user message, got %v", msgs[1]["role"])
	}
	results := contentBlocksOf(t, msgs[1])
	if results[0]["tool_use_id"] != "call_1" {
		t.Errorf("function result must resolve to the tool_use id by name, got %v", results[0]["tool_use_id"])
	}
}

func TestConvertOpenAIToolMessages_UnresolvableToolIsDropped(t *testing.T) {
	msgs := convertMessages(t, `[
		{"role":"user","content":"hi"},
		{"role":"tool","tool_call_id":"call_missing","content":"orphan"}
	]`)

	if len(msgs) != 1 {
		t.Fatalf("expected the orphan tool message to be dropped, got %v", msgs)
	}
}

func TestConvertOpenAIToolMessages_NoToolCallsIsUntouched(t *testing.T) {
	input := `[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"messages":`+input+`}`), &m); err != nil {
		t.Fatalf("build input: %v", err)
	}
	ConvertOpenAIToolMessagesToAnthropic(m)

	if got := string(m["messages"]); got != input {
		t.Errorf("messages without tool calls must pass through byte-for-byte\n got: %s\nwant: %s", got, input)
	}
}

func TestConvertOpenAIToolsToAnthropic(t *testing.T) {
	const openAITool = `{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}`

	tests := []struct {
		name         string
		body         string
		wantToolName string
		wantChoice   string
	}{
		{
			name:         "function declaration and named choice",
			body:         `{"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`,
			wantToolName: "get_weather",
			wantChoice:   `{"type":"tool","name":"get_weather"}`,
		},
		{
			name:         "auto",
			body:         `{"tools":[` + openAITool + `],"tool_choice":"auto"}`,
			wantToolName: "f",
			wantChoice:   `{"type":"auto"}`,
		},
		{
			name:         "required becomes any",
			body:         `{"tools":[` + openAITool + `],"tool_choice":"required"}`,
			wantToolName: "f",
			wantChoice:   `{"type":"any"}`,
		},
		{
			name:         "none drops the declarations",
			body:         `{"tools":[` + openAITool + `],"tool_choice":"none"}`,
			wantToolName: "",
		},
		{
			name:         "parallel_tool_calls false folds into tool_choice",
			body:         `{"tools":[` + openAITool + `],"parallel_tool_calls":false}`,
			wantToolName: "f",
			wantChoice:   `{"type":"auto","disable_parallel_tool_use":true}`,
		},
		{
			name:         "tool_choice without tools is dropped",
			body:         `{"tool_choice":"auto"}`,
			wantToolName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatalf("build input: %v", err)
			}
			ConvertOpenAIToolsToAnthropic(m)

			if _, ok := m["parallel_tool_calls"]; ok {
				t.Error("parallel_tool_calls must not be forwarded upstream")
			}

			toolsRaw, hasTools := m["tools"]
			if tc.wantToolName == "" {
				if hasTools {
					t.Errorf("expected tools to be dropped, got %s", toolsRaw)
				}
			} else {
				if !hasTools {
					t.Fatal("expected tools to be preserved")
				}
				var tools []map[string]any
				if err := json.Unmarshal(toolsRaw, &tools); err != nil {
					t.Fatalf("decode tools: %v", err)
				}
				if len(tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(tools))
				}
				if tools[0]["name"] != tc.wantToolName {
					t.Errorf("expected Anthropic tool name %q, got %v", tc.wantToolName, tools[0]["name"])
				}
				if _, ok := tools[0]["input_schema"]; !ok {
					t.Errorf("expected input_schema, got %v", tools[0])
				}
				if _, ok := tools[0]["function"]; ok {
					t.Errorf("the OpenAI function wrapper must not survive: %v", tools[0])
				}
			}

			choiceRaw, hasChoice := m["tool_choice"]
			if tc.wantChoice == "" {
				if hasChoice {
					t.Errorf("expected no tool_choice, got %s", choiceRaw)
				}
				return
			}
			if !hasChoice {
				t.Fatalf("expected tool_choice %s, got none", tc.wantChoice)
			}
			if !jsonEqual(t, choiceRaw, tc.wantChoice) {
				t.Errorf("tool_choice mismatch\n got: %s\nwant: %s", choiceRaw, tc.wantChoice)
			}
		})
	}
}

func TestConvertOpenAITools_AlreadyAnthropicShaped(t *testing.T) {
	body := `{"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("build input: %v", err)
	}
	ConvertOpenAIToolsToAnthropic(m)

	if got := string(m["tools"]); got != `[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object"}}]` {
		t.Errorf("Anthropic-shaped tools must pass through untouched, got %s", got)
	}
	if !jsonEqual(t, m["tool_choice"], `{"type":"auto"}`) {
		t.Errorf("unexpected tool_choice: %s", m["tool_choice"])
	}
}

func jsonEqual(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}
