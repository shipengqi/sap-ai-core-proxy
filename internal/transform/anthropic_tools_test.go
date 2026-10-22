package transform

import (
	"encoding/json"
	"testing"
)

func convertAnthropicMessages(t *testing.T, messagesJSON string) []map[string]any {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"messages":`+messagesJSON+`}`), &m); err != nil {
		t.Fatalf("build input: %v", err)
	}
	ConvertAnthropicToolMessagesToOpenAI(m)

	var out []map[string]any
	if err := json.Unmarshal(m["messages"], &out); err != nil {
		t.Fatalf("decode converted messages: %v", err)
	}
	return out
}

// TestConvertAnthropicToolMessagesToOpenAI covers the native Anthropic surface
// targeting a non-Claude model: Anthropic blocks must become OpenAI messages.
func TestConvertAnthropicToolMessagesToOpenAI(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"user","content":"what is the weather in Berlin?"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Berlin"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"{\"temp_c\":14}"}]}
	]`)

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["content"] != "what is the weather in Berlin?" {
		t.Errorf("plain user message was rewritten: %v", msgs[0])
	}

	assistant := msgs[1]
	if assistant["role"] != "assistant" {
		t.Fatalf("expected an assistant message, got %v", assistant["role"])
	}
	if _, ok := assistant["content"].([]any); ok {
		t.Errorf("assistant content must be flattened, got %v", assistant["content"])
	}
	calls, ok := assistant["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one tool_call, got %v", assistant["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	if call["id"] != "toolu_1" || call["type"] != "function" {
		t.Errorf("unexpected tool_call: %v", call)
	}
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Berlin"}` {
		t.Errorf("unexpected tool_call function: %v", fn)
	}

	tool := msgs[2]
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" {
		t.Fatalf("expected a tool message bound to toolu_1, got %v", tool)
	}
	if tool["content"] != `{"temp_c":14}` {
		t.Errorf("unexpected tool content: %v", tool["content"])
	}
}

func TestConvertAnthropicToolMessages_AssistantTextKept(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"assistant","content":[
			{"type":"text","text":"Let me check."},
			{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}
		]}
	]`)

	if msgs[0]["content"] != "Let me check." {
		t.Errorf("assistant text must be preserved, got %v", msgs[0]["content"])
	}
	if _, ok := msgs[0]["tool_calls"]; !ok {
		t.Errorf("expected tool_calls alongside the text, got %v", msgs[0])
	}
}

func TestConvertAnthropicToolMessages_MixedUserTurnIsSplit(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"14C"},
			{"type":"text","text":"thanks, and tomorrow?"}
		]}
	]`)

	if len(msgs) != 2 {
		t.Fatalf("expected a tool message followed by a user message, got %v", msgs)
	}
	if msgs[0]["role"] != "tool" || msgs[0]["tool_call_id"] != "toolu_1" {
		t.Errorf("tool results must come first, got %v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "thanks, and tomorrow?" {
		t.Errorf("remaining text must follow as a user message, got %v", msgs[1])
	}
}

func TestConvertAnthropicToolMessages_ToolResultBlockContent(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}
		]}
	]`)

	if msgs[0]["content"] != "line one\nline two" {
		t.Errorf("block tool_result content must flatten to a string, got %v", msgs[0]["content"])
	}
}

func TestConvertAnthropicToolMessages_ImageBecomesImageURL(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"user","content":[
			{"type":"text","text":"what is this?"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}
	]`)

	if len(msgs) != 1 {
		t.Fatalf("expected a single user message, got %v", msgs)
	}
	parts, ok := msgs[0]["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected a two-part content array, got %v", msgs[0]["content"])
	}
	image, _ := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("unexpected image part: %v", image)
	}
	imageURL, _ := image["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,AAAA" {
		t.Errorf("unexpected image url: %v", imageURL["url"])
	}
}

func TestConvertAnthropicToolMessages_ThinkingDropped(t *testing.T) {
	msgs := convertAnthropicMessages(t, `[
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"let me think"},
			{"type":"text","text":"hello"}
		]}
	]`)

	if len(msgs) != 1 {
		t.Fatalf("expected a single message, got %v", msgs)
	}
	if msgs[0]["content"] != "hello" {
		t.Errorf("thinking must be dropped and text kept, got %v", msgs[0]["content"])
	}
}

func TestConvertAnthropicToolMessages_PlainTextUntouched(t *testing.T) {
	for _, input := range []string{
		`[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]`,
		`[{"role":"user","content":[{"type":"text","text":"hi"}]}]`,
	} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(`{"messages":`+input+`}`), &m); err != nil {
			t.Fatalf("build input: %v", err)
		}
		ConvertAnthropicToolMessagesToOpenAI(m)

		if got := string(m["messages"]); got != input {
			t.Errorf("messages without Anthropic-only blocks must pass through byte-for-byte\n got: %s\nwant: %s", got, input)
		}
	}
}

func TestConvertAnthropicToolsToOpenAI(t *testing.T) {
	const anthropicTool = `{"name":"f","description":"d","input_schema":{"type":"object"}}`

	tests := []struct {
		name         string
		body         string
		wantToolName string
		wantChoice   string
		wantParallel string
	}{
		{
			name:         "declaration and named choice",
			body:         `{"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"tool","name":"get_weather"}}`,
			wantToolName: "get_weather",
			wantChoice:   `{"type":"function","function":{"name":"get_weather"}}`,
		},
		{
			name:         "auto",
			body:         `{"tools":[` + anthropicTool + `],"tool_choice":{"type":"auto"}}`,
			wantToolName: "f",
			wantChoice:   `"auto"`,
		},
		{
			name:         "any becomes required",
			body:         `{"tools":[` + anthropicTool + `],"tool_choice":{"type":"any"}}`,
			wantToolName: "f",
			wantChoice:   `"required"`,
		},
		{
			name:         "disable_parallel_tool_use folds into parallel_tool_calls",
			body:         `{"tools":[` + anthropicTool + `],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`,
			wantToolName: "f",
			wantChoice:   `"auto"`,
			wantParallel: "false",
		},
		{
			name:         "tool_choice without tools is dropped",
			body:         `{"tool_choice":{"type":"auto"}}`,
			wantToolName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatalf("build input: %v", err)
			}
			ConvertAnthropicToolsToOpenAI(m)

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
				if tools[0]["type"] != "function" {
					t.Errorf("expected an OpenAI function tool, got %v", tools[0])
				}
				fn, _ := tools[0]["function"].(map[string]any)
				if fn["name"] != tc.wantToolName {
					t.Errorf("expected tool name %q, got %v", tc.wantToolName, fn["name"])
				}
				if _, ok := fn["parameters"]; !ok {
					t.Errorf("expected parameters, got %v", fn)
				}
				if _, ok := tools[0]["input_schema"]; ok {
					t.Errorf("the Anthropic input_schema must not survive: %v", tools[0])
				}
			}

			choiceRaw, hasChoice := m["tool_choice"]
			if tc.wantChoice == "" {
				if hasChoice {
					t.Errorf("expected no tool_choice, got %s", choiceRaw)
				}
			} else if !hasChoice {
				t.Fatalf("expected tool_choice %s, got none", tc.wantChoice)
			} else if !jsonEqual(t, choiceRaw, tc.wantChoice) {
				t.Errorf("tool_choice mismatch\n got: %s\nwant: %s", choiceRaw, tc.wantChoice)
			}

			parallel, hasParallel := m["parallel_tool_calls"]
			if tc.wantParallel == "" {
				if hasParallel {
					t.Errorf("expected no parallel_tool_calls, got %s", parallel)
				}
			} else if string(parallel) != tc.wantParallel {
				t.Errorf("expected parallel_tool_calls %s, got %s", tc.wantParallel, parallel)
			}
		})
	}
}

func TestOpenAIToAnthropicResponse_ToolCalls(t *testing.T) {
	data := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	out := OpenAIToAnthropicResponse([]byte(data), "gpt-4o")

	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode converted response %s: %v", out, err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %q", resp.StopReason)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("expected a single tool_use block, got %s", out)
	}
	block := resp.Content[0]
	if block.Type != "tool_use" || block.ID != "call_1" || block.Name != "get_weather" {
		t.Errorf("unexpected content block: %+v", block)
	}
	var input map[string]any
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("tool_use input is not an object: %s", block.Input)
	}
	if input["city"] != "Berlin" {
		t.Errorf("expected the arguments to be parsed into input, got %s", block.Input)
	}
}

func TestOpenAIToAnthropicResponse_Text(t *testing.T) {
	data := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	out := OpenAIToAnthropicResponse([]byte(data), "gpt-4o")

	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode converted response %s: %v", out, err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %q", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "Hello" {
		t.Errorf("unexpected content: %s", out)
	}
}
