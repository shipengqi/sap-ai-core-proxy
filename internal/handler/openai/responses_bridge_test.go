package openai

import (
	"encoding/json"
	"testing"
)

func mustRaw(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return m
}

func TestChatToResponsesBody_ToolRoundTrip(t *testing.T) {
	raw := mustRaw(t, `{
		"model":"gpt-6-astra",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"what time is it"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"12:00"}
		],
		"tools":[{"type":"function","function":{"name":"get_time","description":"clock","parameters":{"type":"object"}}}],
		"tool_choice":"auto",
		"max_tokens":256,
		"reasoning_effort":"low"
	}`)

	var out struct {
		Instructions    string `json:"instructions"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		Store           bool   `json:"store"`
		Reasoning       struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Tools []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
		Input []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			CallID  string          `json:"call_id"`
			Name    string          `json:"name"`
			Output  string          `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(chatToResponsesBody(raw), &out); err != nil {
		t.Fatalf("decode converted body: %v", err)
	}

	if out.Instructions != "be brief" {
		t.Errorf("system message should become instructions, got %q", out.Instructions)
	}
	if out.MaxOutputTokens != 256 {
		t.Errorf("max_tokens should map to max_output_tokens, got %d", out.MaxOutputTokens)
	}
	if out.Store {
		t.Error("store must be false: a retry must not persist the caller's prompt")
	}
	if out.Reasoning.Effort != "low" {
		t.Errorf("reasoning_effort should map to reasoning.effort, got %q", out.Reasoning.Effort)
	}
	if len(out.Tools) != 1 || out.Tools[0].Type != "function" || out.Tools[0].Name != "get_time" {
		t.Errorf("tools should be flattened, got %+v", out.Tools)
	}

	// system is folded into instructions and the assistant turn carries no text,
	// so three items remain: user, function_call, function_call_output.
	if len(out.Input) != 3 {
		t.Fatalf("expected 3 input items, got %d: %+v", len(out.Input), out.Input)
	}
	if out.Input[0].Role != "user" || string(out.Input[0].Content) != `"what time is it"` {
		t.Errorf("user message not carried over: %+v", out.Input[0])
	}
	if out.Input[1].Type != "function_call" || out.Input[1].CallID != "call_1" || out.Input[1].Name != "get_time" {
		t.Errorf("tool call should become a function_call item: %+v", out.Input[1])
	}
	if out.Input[2].Type != "function_call_output" || out.Input[2].CallID != "call_1" || out.Input[2].Output != "12:00" {
		t.Errorf("tool result should become a function_call_output item: %+v", out.Input[2])
	}
}

func TestChatToResponsesBody_ImagesBecomeInputImage(t *testing.T) {
	raw := mustRaw(t, `{
		"model":"gpt-6-astra",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"what is this"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
		]}],
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]
	}`)

	var out struct {
		Input []struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(chatToResponsesBody(raw), &out); err != nil {
		t.Fatalf("decode converted body: %v", err)
	}
	if len(out.Input) != 1 || len(out.Input[0].Content) != 2 {
		t.Fatalf("expected one message with two parts, got %+v", out.Input)
	}
	if out.Input[0].Content[0].Type != "input_text" || out.Input[0].Content[0].Text != "what is this" {
		t.Errorf("text part not converted: %+v", out.Input[0].Content[0])
	}
	if out.Input[0].Content[1].Type != "input_image" || out.Input[0].Content[1].ImageURL != "data:image/png;base64,AAA" {
		t.Errorf("image part not converted: %+v", out.Input[0].Content[1])
	}
}

func TestResponsesToChatCompletion_ToolCall(t *testing.T) {
	body := []byte(`{
		"id":"resp_abc","status":"completed",
		"output":[{"type":"function_call","call_id":"call_9","name":"get_time","arguments":"{\"tz\":\"UTC\"}"}],
		"usage":{"input_tokens":45,"output_tokens":14,"total_tokens":59,
			"input_tokens_details":{"cached_tokens":20},
			"output_tokens_details":{"reasoning_tokens":5}}
	}`)

	var out struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responsesToChatCompletion(body, "gpt-6-astra"), &out); err != nil {
		t.Fatalf("decode converted response: %v", err)
	}

	if out.Object != "chat.completion" || out.Model != "gpt-6-astra" {
		t.Errorf("unexpected envelope: %+v", out)
	}
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %+v", out.Choices)
	}
	calls := out.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_9" || calls[0].Function.Name != "get_time" {
		t.Fatalf("tool call not converted: %+v", calls)
	}
	if calls[0].Function.Arguments != `{"tz":"UTC"}` {
		t.Errorf("arguments should be passed through verbatim, got %q", calls[0].Function.Arguments)
	}
	if out.Choices[0].Message.Content != nil {
		t.Error("content must be null on a pure tool-call turn")
	}
	if out.Usage.PromptTokens != 45 || out.Usage.CompletionTokens != 14 || out.Usage.TotalTokens != 59 {
		t.Errorf("usage not mapped: %+v", out.Usage)
	}
	if out.Usage.PromptTokensDetails.CachedTokens != 20 || out.Usage.CompletionTokensDetails.ReasoningTokens != 5 {
		t.Errorf("usage details not mapped: %+v", out.Usage)
	}
}

func TestResponsesToChatCompletion_Text(t *testing.T) {
	body := []byte(`{"id":"resp_1","status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],
		"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)

	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responsesToChatCompletion(body, "m"), &out); err != nil {
		t.Fatalf("decode converted response: %v", err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "hello" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("text response not converted: %+v", out.Choices)
	}
}

func TestIsResponsesRequiredError(t *testing.T) {
	tools := []byte(`{"error":{"message":"Function tools with reasoning_effort are not supported for this model in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","param":"reasoning_effort"}}`)
	if !IsResponsesRequiredError(tools) {
		t.Error("the tools-require-responses error should be recognised")
	}
	// The other reasoning_effort error must not trigger the bridge: dropping the
	// field cannot fix it, and rerouting would not either.
	unsupported := []byte(`{"error":{"code":"unsupported_value","message":"Unsupported value: 'reasoning_effort' does not support 'none' with this model."}}`)
	if IsResponsesRequiredError(unsupported) {
		t.Error("an unsupported effort value must not be treated as a tools error")
	}
	if IsResponsesRequiredError([]byte(`{"error":{"message":"nope"}}`)) {
		t.Error("unrelated errors must not be treated as a tools error")
	}
}

func TestResponsesToChatCompletion_IncompleteToolCall(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			body := []byte(`{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"output":[{"type":"function_call","call_id":"call_1","name":"f","arguments":"{\"x\":"}]}`)
			var out struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(responsesToChatCompletion(body, "m"), &out); err != nil {
				t.Fatal(err)
			}
			want := "length"
			if reason == "content_filter" {
				want = "content_filter"
			}
			if len(out.Choices) != 1 || out.Choices[0].FinishReason != want {
				t.Errorf("incomplete tool call must not be marked executable: %+v", out)
			}
		})
	}
}
