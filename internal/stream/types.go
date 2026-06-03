package stream

// ConverseEvent type constants.
const (
	ConverseMetadata       = "metadata"
	ConverseTextBlockStart = "textBlockStart"
	ConverseTextDelta      = "textDelta"
	ConverseReasoningDelta = "reasoningDelta"
	ConverseTextBlockStop  = "textBlockStop"
	ConverseToolBlockStart = "toolBlockStart"
	ConverseToolInputDelta = "toolInputDelta"
	ConverseToolBlockStop  = "toolBlockStop"
	ConverseMessageStop    = "messageStop"
)

// ConverseEvent carries a typed event from the SAP Converse API stream.
type ConverseEvent struct {
	Type        string
	Index       int
	Text        string
	InputTokens int
	OutputTokens int
	ID          string // tool use id
	Name        string // tool name
	PartialJSON string
	StopReason  string
}

// InvokeEvent type constants.
const (
	InvokeMessageStart = "messageStart"
	InvokeBlockStart   = "blockStart"
	InvokeBlockDelta   = "blockDelta"
	InvokeBlockStop    = "blockStop"
	InvokeMessageDelta = "messageDelta"
	InvokeMessageStop  = "messageStop"
)

// InvokeEvent carries a typed event from the SAP Invoke API stream.
type InvokeEvent struct {
	Type         string
	MessageID    string
	InputTokens  int
	Index        int
	ContentBlock map[string]any
	Delta        map[string]any
	StopReason   string
	StopSequence string
	OutputTokens int
}

// GeminiEvent type constants.
const (
	GeminiTextDelta      = "textDelta"
	GeminiReasoningDelta = "reasoningDelta"
	GeminiMetadata       = "metadata"
)

// GeminiEvent carries a typed event from the SAP Gemini API stream.
type GeminiEvent struct {
	Type         string
	Text         string
	PromptTokens int
	OutputTokens int
}

// StreamContext describes how to parse and format a streaming response.
type StreamContext struct {
	APIFormat      string // "converse", "invoke", "gemini"
	ResponseFormat string // "anthropic", "openai"
	Model          string
	CompletionID   string // messageId for anthropic, chatcmpl-xxx for openai
}
