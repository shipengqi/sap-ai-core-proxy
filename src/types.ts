// SAP AI Core Types
export interface SapAiCoreCredentials {
  clientId: string;
  clientSecret: string;
  tokenUrl: string;
  baseUrl: string;
  resourceGroup?: string;
}

export interface Token {
  access_token: string;
  token_type: string;
  expires_in: number;
  expires_at: number;
}

export interface Deployment {
  id: string;
  configurationId: string;
  configurationName: string;
  scenarioId: string;
  status: string;
  targetStatus: string;
  createdAt: string;
  modifiedAt: string;
  submissionTime: string;
  startTime: string;
  deploymentUrl: string;
  details: {
    resources: {
      backend_details: {
        model: {
          name: string;
          version: string;
        };
      };
    };
    scaling?: {
      backend_details: Record<string, unknown>;
    };
  };
}

export interface DeploymentsResponse {
  count: number;
  resources: Deployment[];
}

// OpenAI Compatible Types
export interface OpenAIMessage {
  role: 'system' | 'user' | 'assistant' | 'function' | 'tool';
  content: string | null;
  name?: string;
  function_call?: {
    name: string;
    arguments: string;
  };
  tool_calls?: Array<{
    id: string;
    type: 'function';
    function: {
      name: string;
      arguments: string;
    };
  }>;
}

export interface OpenAIChatCompletionRequest {
  model: string;
  messages: OpenAIMessage[];
  temperature?: number;
  top_p?: number;
  n?: number;
  stream?: boolean;
  stop?: string | string[];
  max_tokens?: number;
  presence_penalty?: number;
  frequency_penalty?: number;
  logit_bias?: Record<string, number>;
  user?: string;
  functions?: Array<{
    name: string;
    description?: string;
    parameters: Record<string, unknown>;
  }>;
  function_call?: 'none' | 'auto' | { name: string };
  tools?: Array<{
    type: 'function';
    function: {
      name: string;
      description?: string;
      parameters: Record<string, unknown>;
    };
  }>;
  tool_choice?: 'none' | 'auto' | { type: 'function'; function: { name: string } };
}

export interface OpenAIChatCompletionChoice {
  index: number;
  message: OpenAIMessage;
  finish_reason: 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null;
}

export interface OpenAIChatCompletionResponse {
  id: string;
  object: 'chat.completion';
  created: number;
  model: string;
  choices: OpenAIChatCompletionChoice[];
  usage?: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
  };
  system_fingerprint?: string;
}

export interface OpenAIChatCompletionChunkChoice {
  index: number;
  delta: Partial<OpenAIMessage>;
  finish_reason: 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null;
}

export interface OpenAIChatCompletionChunk {
  id: string;
  object: 'chat.completion.chunk';
  created: number;
  model: string;
  choices: OpenAIChatCompletionChunkChoice[];
  usage?: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
  };
  system_fingerprint?: string;
}

export interface OpenAIModel {
  id: string;
  object: 'model';
  created: number;
  owned_by: string;
}

export interface OpenAIModelsResponse {
  object: 'list';
  data: OpenAIModel[];
}

export interface OpenAIErrorResponse {
  error: {
    message: string;
    type: string;
    param: string | null;
    code: string | null;
  };
}

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

// Model Provider Types
export type ModelProvider = 'openai' | 'anthropic' | 'gemini' | 'meta' | 'mistral' | 'amazon';

export interface ModelInfo {
  provider: ModelProvider;
  maxTokens: number;
  contextWindow: number;
  supportsStreaming: boolean;
  supportsVision?: boolean;
}

// Configuration
export interface ProxyConfig {
  port: number;
  sapAiCore: SapAiCoreCredentials;
  defaultResourceGroup: string;
  logRequests: boolean;
}