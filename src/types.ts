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