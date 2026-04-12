import { SapAiCoreCredentials } from '../sap-ai-core/types';

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
