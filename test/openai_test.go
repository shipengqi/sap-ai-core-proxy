package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupOpenAI(t *testing.T) (proxyURL string, cleanup func()) {
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

func TestOpenAI_ListModels(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/openai/v1/models")
	if err != nil {
		t.Fatalf("GET /openai/v1/models: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Object string        `json:"object"`
		Data   []interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Object != "list" {
		t.Errorf("expected object=list, got %q", body.Object)
	}
	if len(body.Data) == 0 {
		t.Error("expected non-empty model list")
	}
}

func TestOpenAI_ChatCompletions(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	model := "gpt-4o"
	reqBody := `{"model":"` + model + `","messages":[{"role":"user","content":"Say hi"}]}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		t.Error("expected non-empty choices[0].message.content")
	}
}

// reasoning_effort used to be swallowed by the field whitelist. For models that
// only accept function tools on /responses (gpt-6.x), an explicit "none" is the
// only way a tool-bearing chat request can succeed, so dropping the field turned
// every such request into a 400 the client had no way to fix.
func TestOpenAI_ReasoningEffortIsForwarded(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	// The capability cache is keyed by deployment ID, which the mock reuses
	// across tests; a stale entry would strip the field before it is sent.
	openai.ClearEffortCache()
	defer openai.ClearEffortCache()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}],` +
		`"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],` +
		`"reasoning_effort":"none"}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// The tools array is what makes upstream demand an explicit effort; assert it
	// survived too so the test cannot pass on a request that never had tools.
	if _, ok := mock.LastOpenAIChatBody["tools"]; !ok {
		t.Fatalf("tools missing from the forwarded body: %v", mock.LastOpenAIChatBody)
	}
	if got := string(mock.LastOpenAIChatBody["reasoning_effort"]); got != `"none"` {
		t.Errorf("reasoning_effort not forwarded to SAP, got %q (body: %v)", got, mock.LastOpenAIChatBody)
	}
}

// Models without a reasoning_effort argument (gpt-4o) answer 400 when the field
// is sent. Forwarding it must not turn those models into failures: the proxy
// retries without the field and remembers the capability per deployment.
func TestOpenAI_ReasoningEffortFallbackWhenArgumentUnknown(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	mock.RejectReasoningEffort = true
	openai.ClearEffortCache()
	defer openai.ClearEffortCache()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}],"reasoning_effort":"low"}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after dropping reasoning_effort, got %d: %s", resp.StatusCode, body)
	}
	if _, still := mock.LastOpenAIChatBody["reasoning_effort"]; still {
		t.Errorf("retry still carried reasoning_effort: %v", mock.LastOpenAIChatBody)
	}
	if !openai.IsEffortUnsupported("dep-openai-001") {
		t.Error("deployment should be remembered as not supporting reasoning_effort")
	}
}

// gpt-6.x refuses function tools on /chat/completions and accepts them only on
// /responses. The proxy must bridge the request there and hand the caller back
// the chat.completion it asked for.
func TestOpenAI_ToolsBridgeToResponses(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	mock.ToolsRequireResponses = true
	openai.ClearCapabilityCaches()
	defer openai.ClearCapabilityCaches()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"call get_time"}],` +
		`"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],"max_tokens":64}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 via the responses bridge, got %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode chat.completion: %v", err)
	}
	if out.Object != "chat.completion" {
		t.Errorf("caller must still receive a chat.completion, got %q", out.Object)
	}
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason=tool_calls, got %+v", out.Choices)
	}
	calls := out.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_mock_1" || calls[0].Function.Name != "get_time" {
		t.Errorf("tool call not carried back: %+v", calls)
	}
	if out.Usage.PromptTokens != 30 {
		t.Errorf("usage should be restated as prompt_tokens, got %+v", out.Usage)
	}

	// The upstream must have received a Responses body, not the chat body.
	if _, ok := mock.LastResponsesBody["messages"]; ok {
		t.Errorf("chat messages leaked into the responses request: %v", mock.LastResponsesBody)
	}
	if _, ok := mock.LastResponsesBody["input"]; !ok {
		t.Errorf("responses request has no input: %v", mock.LastResponsesBody)
	}
	if _, ok := mock.LastResponsesBody["tools"]; !ok {
		t.Errorf("tools were not carried over: %v", mock.LastResponsesBody)
	}
	if !openai.IsResponsesRequired("dep-openai-001") {
		t.Error("deployment should be remembered as tools-on-responses only")
	}
}

func TestOpenAI_ToolsBridgeToResponses_Stream(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	mock.ToolsRequireResponses = true
	openai.ClearCapabilityCaches()
	defer openai.ClearCapabilityCaches()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"call get_time"}],` +
		`"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],` +
		`"stream":true,"stream_options":{"include_usage":true}}`
	resp, err := http.Post(proxy.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	stream := string(body)

	for _, want := range []string{"chat.completion.chunk", "call_mock_1", "tool_calls", `"usage"`, "[DONE]"} {
		if !strings.Contains(stream, want) {
			t.Errorf("stream is missing %q: %s", want, stream)
		}
	}
	if strings.Contains(stream, `"type":"response.`) {
		t.Errorf("raw responses events leaked to the caller: %s", stream)
	}
}

func TestOpenAI_ChatCompletions_Stream(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}],"stream":true}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions stream: %v", err)
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

	if !strings.Contains(bodyStr, "data: ") {
		t.Errorf("expected SSE data: lines in stream response, got: %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "[DONE]") {
		t.Errorf("expected [DONE] in stream response")
	}
}

func TestOpenAI_Embeddings(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","input":"Hello world"}`
	resp, err := http.Post(proxyURL+"/openai/v1/embeddings", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/embeddings: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		t.Error("expected non-empty data[0].embedding")
	}
}

func TestOpenAI_Responses_CRUD(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	// POST — create
	createBody := `{"model":"gpt-4o","input":"Hello"}`
	resp, err := http.Post(proxyURL+"/openai/v1/responses", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/responses: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create response: expected 200/201, got %d: %s", resp.StatusCode, body)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty response ID")
	}

	// GET
	getResp, err := http.Get(proxyURL + "/openai/v1/responses/" + created.ID)
	if err != nil {
		t.Fatalf("GET /openai/v1/responses/%s: %v", created.ID, err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET response: expected 200, got %d", getResp.StatusCode)
	}

	// DELETE
	req, _ := http.NewRequest(http.MethodDelete, proxyURL+"/openai/v1/responses/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /openai/v1/responses/%s: %v", created.ID, err)
	}
	defer func() { _ = delResp.Body.Close() }()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE response: expected 200/204, got %d", delResp.StatusCode)
	}
}

// setupOpenAIMock starts the mock SAP server plus a proxy in front of it, returning
// both so tests can inspect the body the proxy forwarded upstream.
func setupOpenAIMock(t *testing.T) (proxyURL string, mock *testutil.MockSAPServer) {
	t.Helper()
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		t.Skip("skipping mock-only test in real mode")
	}
	mock = testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	t.Cleanup(func() {
		proxy.Close()
		mock.Close()
	})
	return proxy.URL, mock
}

// claudeToolRequest drives a Claude model through the OpenAI surface with a full
// tool-calling round trip in the message history.
const claudeToolRequest = `{
	"model": "claude-4.6-sonnet",
	"messages": [
		{"role":"system","content":"you are a helper"},
		{"role":"user","content":"what is the weather in Berlin?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"{\"temp_c\":14}"}
	],
	"tools": [{"type":"function","function":{"name":"get_weather","description":"Get the weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
	"tool_choice": {"type":"function","function":{"name":"get_weather"}}
}`

// assertForwardedToolRequest checks that the body reaching Bedrock uses Anthropic
// content blocks. A surviving "tool" role is exactly the upstream 400 this guards.
func assertForwardedToolRequest(t *testing.T, body map[string]json.RawMessage) {
	t.Helper()
	if body == nil {
		t.Fatal("nothing was forwarded to the Anthropic /invoke endpoint")
	}

	var msgs []map[string]any
	if err := json.Unmarshal(body["messages"], &msgs); err != nil {
		t.Fatalf("decode forwarded messages: %v", err)
	}
	// The system message is promoted out of the array into the top-level field.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 forwarded messages, got %d: %v", len(msgs), msgs)
	}
	var system string
	if err := json.Unmarshal(body["system"], &system); err != nil || system != "you are a helper" {
		t.Errorf("expected the system message to be promoted, got %s", body["system"])
	}
	for i, m := range msgs {
		if role, _ := m["role"].(string); role == "tool" || role == "function" {
			t.Fatalf("forwarded messages[%d] still uses the OpenAI role %q", i, role)
		}
		if _, ok := m["tool_calls"]; ok {
			t.Fatalf("forwarded messages[%d] still carries the OpenAI tool_calls field", i)
		}
	}

	blocks, ok := msgs[1]["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected the assistant turn to carry one tool_use block, got %v", msgs[1]["content"])
	}
	toolUse, _ := blocks[0].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "call_1" || toolUse["name"] != "get_weather" {
		t.Errorf("unexpected tool_use block: %v", toolUse)
	}
	if input, _ := toolUse["input"].(map[string]any); input["city"] != "Berlin" {
		t.Errorf("tool_use input was not parsed from the arguments string: %v", toolUse["input"])
	}

	if msgs[2]["role"] != "user" {
		t.Fatalf("expected the tool message to become a user message, got %v", msgs[2]["role"])
	}
	resultBlocks, ok := msgs[2]["content"].([]any)
	if !ok || len(resultBlocks) != 1 {
		t.Fatalf("expected one tool_result block, got %v", msgs[2]["content"])
	}
	toolResult, _ := resultBlocks[0].(map[string]any)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_1" {
		t.Errorf("unexpected tool_result block: %v", toolResult)
	}

	var tools []map[string]any
	if err := json.Unmarshal(body["tools"], &tools); err != nil {
		t.Fatalf("decode forwarded tools: %v", err)
	}
	if len(tools) != 1 || tools[0]["name"] != "get_weather" {
		t.Fatalf("tools were not converted to the Anthropic shape: %v", tools)
	}
	if _, ok := tools[0]["input_schema"]; !ok {
		t.Errorf("expected input_schema on the forwarded tool: %v", tools[0])
	}
	if _, ok := tools[0]["function"]; ok {
		t.Errorf("the OpenAI function wrapper must not reach Bedrock: %v", tools[0])
	}

	var choice map[string]any
	if err := json.Unmarshal(body["tool_choice"], &choice); err != nil {
		t.Fatalf("decode forwarded tool_choice: %v", err)
	}
	if choice["type"] != "tool" || choice["name"] != "get_weather" {
		t.Errorf("tool_choice was not converted: %v", choice)
	}
}

// TestOpenAI_ClaudeToolCalling covers the reported failure: a tool-calling history
// sent to the OpenAI surface with a Claude model must be translated before it
// reaches Bedrock, and the tool_use response must come back as OpenAI tool_calls.
func TestOpenAI_ClaudeToolCalling(t *testing.T) {
	proxyURL, mock := setupOpenAIMock(t)
	mock.ToolCallResponse = true

	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(claudeToolRequest))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Choices []struct {
			Message struct {
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
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
	if len(result.Choices) == 0 {
		t.Fatalf("expected at least one choice, got %s", body)
	}
	choice := result.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool_call in the response, got %s", body)
	}
	call := choice.Message.ToolCalls[0]
	if call.ID != "toolu_mock_1" || call.Type != "function" || call.Function.Name != "get_weather" {
		t.Errorf("unexpected tool_call: %+v", call)
	}
	if call.Function.Arguments != `{"city":"Berlin"}` {
		t.Errorf("expected the tool input to round-trip as JSON arguments, got %q", call.Function.Arguments)
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		t.Errorf("expected null or empty content on a tool-call turn, got %q", *choice.Message.Content)
	}

	assertForwardedToolRequest(t, mock.LastInvokeBody)
}

// TestOpenAI_ClaudeToolCalling_Stream verifies tool calls are reassembled from
// Anthropic input_json_delta events into OpenAI tool_calls deltas.
func TestOpenAI_ClaudeToolCalling_Stream(t *testing.T) {
	proxyURL, mock := setupOpenAIMock(t)
	mock.ToolCallResponse = true

	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json",
		strings.NewReader(strings.Replace(claudeToolRequest, "{", `{"stream":true,`, 1)))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}
	streamBody := string(raw)
	if !strings.Contains(streamBody, "[DONE]") {
		t.Errorf("expected [DONE] in the stream, got: %q", streamBody)
	}

	name, args, finishReason := collectToolCallDeltas(t, streamBody)
	if name != "get_weather" {
		t.Errorf("expected the tool name to be streamed, got %q", name)
	}
	if args != `{"city":"Berlin"}` {
		t.Errorf("tool arguments did not reassemble from input_json_delta events: %q", args)
	}
	if finishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %q", finishReason)
	}

	assertForwardedToolRequest(t, mock.LastInvokeStreamBody)
}

// TestOpenAI_ClaudeStreaming_UpstreamErrorIsPropagated guards against masking an
// upstream failure as HTTP 200 with an empty stream.
func TestOpenAI_ClaudeStreaming_UpstreamErrorIsPropagated(t *testing.T) {
	proxyURL, mock := setupOpenAIMock(t)
	mock.StreamingErrorStatus = http.StatusBadRequest

	reqBody := `{"model":"claude-4.6-sonnet","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the upstream 400 to reach the client, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "mock upstream streaming failure") {
		t.Errorf("expected the upstream error body to be forwarded, got %s", body)
	}
}

// collectToolCallDeltas walks an OpenAI SSE stream and reassembles the streamed
// tool call name, arguments and finish_reason.
func collectToolCallDeltas(t *testing.T, streamBody string) (name, args, finishReason string) {
	t.Helper()
	var arguments strings.Builder

	for _, line := range strings.Split(streamBody, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode stream chunk %q: %v", payload, err)
		}
		for _, c := range chunk.Choices {
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
			for _, call := range c.Delta.ToolCalls {
				if call.Function.Name != "" {
					name = call.Function.Name
				}
				arguments.WriteString(call.Function.Arguments)
			}
		}
	}
	return name, arguments.String(), finishReason
}
