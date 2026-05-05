// Anthropic Messages API Types (for Claude Code / native Anthropic API compatibility)
export interface AnthropicTextContent {
  type: 'text';
  text: string;
}

export interface AnthropicToolUseContent {
  type: 'tool_use';
  id: string;
  name: string;
  input: Record<string, unknown>;
}

export interface AnthropicToolResultContent {
  type: 'tool_result';
  tool_use_id: string;
  content: string | Array<AnthropicTextContent>;
  is_error?: boolean;
}

export interface AnthropicImageContent {
  type: 'image';
  source: {
    type: 'base64' | 'url';
    media_type?: string;
    data?: string;
    url?: string;
  };
}

export type AnthropicContentBlock =
  | AnthropicTextContent
  | AnthropicToolUseContent
  | AnthropicToolResultContent
  | AnthropicImageContent;

export interface AnthropicMessage {
  role: 'user' | 'assistant';
  content: string | AnthropicContentBlock[];
}

export interface AnthropicTool {
  name: string;
  description?: string;
  input_schema: Record<string, unknown>;
}

export interface AnthropicToolChoice {
  type: 'auto' | 'any' | 'tool' | 'none';
  name?: string; // for type: 'tool'
}

export interface AnthropicSystemContent {
  type: 'text';
  text: string;
}

export interface AnthropicMessagesRequest {
  model: string;
  messages: AnthropicMessage[];
  max_tokens: number;
  system?: string | AnthropicSystemContent[];
  temperature?: number;
  top_p?: number;
  top_k?: number;
  stop_sequences?: string[];
  stream?: boolean;
  tools?: AnthropicTool[];
  tool_choice?: AnthropicToolChoice;
  metadata?: Record<string, unknown>;
}

export interface AnthropicUsage {
  input_tokens: number;
  output_tokens: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
}

export interface AnthropicMessagesResponse {
  id: string;
  type: 'message';
  role: 'assistant';
  content: AnthropicContentBlock[];
  model: string;
  stop_reason: 'end_turn' | 'max_tokens' | 'stop_sequence' | 'tool_use' | null;
  stop_sequence: string | null;
  usage: AnthropicUsage;
}

// Anthropic streaming event types
export interface AnthropicMessageStartEvent {
  type: 'message_start';
  message: Omit<AnthropicMessagesResponse, 'content'> & { content: [] };
}

export interface AnthropicContentBlockStartEvent {
  type: 'content_block_start';
  index: number;
  content_block: AnthropicTextContent | { type: 'tool_use'; id: string; name: string; input: Record<string, unknown> };
}

export interface AnthropicContentBlockDeltaEvent {
  type: 'content_block_delta';
  index: number;
  delta: { type: 'text_delta'; text: string } | { type: 'input_json_delta'; partial_json: string };
}

export interface AnthropicContentBlockStopEvent {
  type: 'content_block_stop';
  index: number;
}

export interface AnthropicMessageDeltaEvent {
  type: 'message_delta';
  delta: { stop_reason: string; stop_sequence: string | null };
  usage: { output_tokens: number };
}

export interface AnthropicMessageStopEvent {
  type: 'message_stop';
}

export interface AnthropicPingEvent {
  type: 'ping';
}

export type AnthropicStreamEvent =
  | AnthropicMessageStartEvent
  | AnthropicContentBlockStartEvent
  | AnthropicContentBlockDeltaEvent
  | AnthropicContentBlockStopEvent
  | AnthropicMessageDeltaEvent
  | AnthropicMessageStopEvent
  | AnthropicPingEvent;

export interface AnthropicCountTokensRequest {
  model: string;
  messages: AnthropicMessage[];
  system?: string | AnthropicSystemContent[];
  tools?: AnthropicTool[];
}

export interface AnthropicCountTokensResponse {
  input_tokens: number;
}

export interface AnthropicModel {
  type: 'model';
  id: string;
  display_name: string;
  created_at: string;
}

export interface AnthropicModelsResponse {
  data: AnthropicModel[];
  has_more: boolean;
  first_id: string | null;
  last_id: string | null;
}
