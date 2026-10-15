package test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupAnthropic(t *testing.T) (proxyURL string, cleanup func()) {
	t.Helper()
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		proxy, err := testutil.NewTestProxy("")
		if err != nil {
			t.Fatalf("start real proxy: %v", err)
		}
		return proxy.URL, proxy.Close
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	return proxy.URL, func() {
		proxy.Close()
		mock.Close()
	}
}

func TestAnthropic_ListModels(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/anthropic/v1/models")
	if err != nil {
		t.Fatalf("GET /anthropic/v1/models: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Data []interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) == 0 {
		t.Error("expected non-empty model list")
	}
}

func TestAnthropic_Messages(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	// claude-3-5-sonnet-latest aliases to anthropic--claude-3.5-sonnet which matches the mock deployment
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Say hi"}]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty content[0].text")
	}
}

func TestAnthropic_Messages_Stream(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"stream": true,
		"messages": [{"role": "user", "content": "Say hi"}]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "event: message_start") {
		t.Errorf("expected 'event: message_start' in stream, got: %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Errorf("expected message_stop event in stream")
	}
}

// TestAnthropic_RequestedClaudeModels verifies the model names Claude Code is
// configured with resolve to a running SAP deployment end to end.
func TestAnthropic_RequestedClaudeModels(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping mock-only test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	models := []string{"claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			reqBody := `{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
			resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
			if err != nil {
				t.Fatalf("POST /anthropic/v1/messages: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("model %s: expected 200, got %d: %s", model, resp.StatusCode, body)
			}

			var result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(result.Content) == 0 || result.Content[0].Text == "" {
				t.Errorf("model %s: expected non-empty content[0].text", model)
			}
		})
	}
}

// TestAnthropic_LatestAliases verifies the bare "-latest" aliases track the
// newest running deployment of each family rather than a pinned version.
func TestAnthropic_LatestAliases(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping mock-only test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	// The mock runs claude-4.8-opus, claude-4.6-sonnet and claude-4.5-haiku.
	// claude-opus-latest used to resolve to a pinned 4.7 that has no
	// deployment here, so this also guards against re-pinning the table.
	for _, model := range []string{"claude-opus-latest", "claude-sonnet-latest", "claude-haiku-latest"} {
		t.Run(model, func(t *testing.T) {
			reqBody := `{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
			resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
			if err != nil {
				t.Fatalf("POST /anthropic/v1/messages: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("model %s: expected 200, got %d: %s", model, resp.StatusCode, body)
			}
		})
	}
}

func TestAnthropic_SystemPromotion(t *testing.T) {
	proxyURL, cleanup := setupAnthropic(t)
	defer cleanup()

	// Send system as a message role — proxy should promote it to top-level system field
	// and inject anthropic_version. The mock validates both.
	reqBody := `{
		"model": "claude-3-5-sonnet-latest",
		"max_tokens": 100,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages system: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	// anthropic_version injection and system promotion are validated inside the mock handler.
}

const nativeToolMessages = `[
	{"role":"user","content":"what is the weather in Berlin?"},
	{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Berlin"}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"{\"temp_c\":14}"}]}
]`

const nativeTools = `[{"name":"get_weather","description":"Get the weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`

// TestAnthropic_NativeToolMessagesUntouched guards the native Anthropic surface:
// Anthropic-shaped tool_use / tool_result blocks must be forwarded verbatim and
// never run through the OpenAI tool conversion used by /openai/v1.
func TestAnthropic_NativeToolMessagesUntouched(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping mock-only test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	reqBody := `{"model":"claude-3-5-sonnet-latest","max_tokens":64,"messages":` + nativeToolMessages + `,"tools":` + nativeTools + `}`
	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if mock.LastInvokeBody == nil {
		t.Fatal("nothing was forwarded to the Anthropic /invoke endpoint")
	}
	assertSameJSON(t, "messages", mock.LastInvokeBody["messages"], nativeToolMessages)
	assertSameJSON(t, "tools", mock.LastInvokeBody["tools"], nativeTools)
}

// assertSameJSON compares two JSON documents structurally, so formatting differences
// do not matter but any content change does.
func assertSameJSON(t *testing.T, label string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decode forwarded %s: %v", label, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decode expected %s: %v", label, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("native Anthropic %s must be forwarded untouched\n got: %s\nwant: %s", label, got, want)
	}
}

// TestAnthropic_ToolCallingViaOpenAIEndpoint covers the mirror image of the OpenAI
// surface bug: the native Anthropic surface targeting a non-Claude model must
// translate Anthropic tool blocks into the OpenAI wire format, and translate the
// tool_calls response back into tool_use blocks.
func TestAnthropic_ToolCallingViaOpenAIEndpoint(t *testing.T) {
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping mock-only test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	mock.OpenAIToolCallResponse = true
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	defer proxy.Close()
	defer mock.Close()

	reqBody := `{"model":"gpt-4o","max_tokens":64,"messages":` + nativeToolMessages +
		`,"tools":` + nativeTools + `,"tool_choice":{"type":"auto"}}`
	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}

	var result struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	if result.StopReason != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %q", result.StopReason)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one tool_use block, got %s", raw)
	}
	block := result.Content[0]
	if block.Type != "tool_use" || block.ID != "call_mock_1" || block.Name != "get_weather" {
		t.Errorf("unexpected content block: %+v", block)
	}
	var input map[string]any
	if err := json.Unmarshal(block.Input, &input); err != nil || input["city"] != "Berlin" {
		t.Errorf("expected the tool arguments to be parsed into input, got %s", block.Input)
	}

	assertForwardedOpenAIToolRequest(t, mock.LastOpenAIChatBody)
}

// assertForwardedOpenAIToolRequest checks that the body reaching SAP's OpenAI
// compatible endpoint uses OpenAI message and tool shapes.
func assertForwardedOpenAIToolRequest(t *testing.T, body map[string]json.RawMessage) {
	t.Helper()
	if body == nil {
		t.Fatal("nothing was forwarded to the OpenAI-compatible /chat/completions endpoint")
	}

	var msgs []map[string]any
	if err := json.Unmarshal(body["messages"], &msgs); err != nil {
		t.Fatalf("decode forwarded messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 forwarded messages, got %d: %v", len(msgs), msgs)
	}

	assistant := msgs[1]
	calls, ok := assistant["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected the assistant turn to carry tool_calls, got %v", assistant)
	}
	call, _ := calls[0].(map[string]any)
	if call["id"] != "toolu_1" {
		t.Errorf("expected the tool_use id to become the tool_call id, got %v", call["id"])
	}
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Berlin"}` {
		t.Errorf("unexpected tool_call function: %v", fn)
	}

	tool := msgs[2]
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" {
		t.Fatalf("expected the tool_result block to become a tool message, got %v", tool)
	}
	if tool["content"] != `{"temp_c":14}` {
		t.Errorf("unexpected tool content: %v", tool["content"])
	}

	var tools []map[string]any
	if err := json.Unmarshal(body["tools"], &tools); err != nil {
		t.Fatalf("decode forwarded tools: %v", err)
	}
	if len(tools) != 1 || tools[0]["type"] != "function" {
		t.Fatalf("tools were not converted to the OpenAI shape: %v", tools)
	}
	toolFn, _ := tools[0]["function"].(map[string]any)
	if toolFn["name"] != "get_weather" {
		t.Errorf("unexpected tool name: %v", toolFn["name"])
	}
	if _, ok := toolFn["parameters"]; !ok {
		t.Errorf("expected the input_schema to become parameters: %v", toolFn)
	}
	if _, ok := tools[0]["input_schema"]; ok {
		t.Errorf("the Anthropic input_schema must not be forwarded: %v", tools[0])
	}

	var choice any
	if err := json.Unmarshal(body["tool_choice"], &choice); err != nil {
		t.Fatalf("decode forwarded tool_choice: %v", err)
	}
	if choice != "auto" {
		t.Errorf("expected tool_choice \"auto\", got %v", choice)
	}
}
