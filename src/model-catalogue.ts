import { ModelInfo, ModelProvider } from './types/models';

export interface CatalogueEntry extends ModelInfo {
  sapName: string;
  usesConverseApi: boolean;
  anthropicAliases?: string[];
}

const CATALOGUE: CatalogueEntry[] = [
  // Claude 4.6
  { sapName: 'anthropic--claude-4.6-sonnet', provider: 'anthropic', maxTokens: 32768, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-sonnet-4-6'] },
  { sapName: 'anthropic--claude-4.6-opus',   provider: 'anthropic', maxTokens: 32768, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-opus-4-6'] },
  { sapName: 'anthropic--claude-4.6-haiku',  provider: 'anthropic', maxTokens: 32768, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-haiku-4-6'] },

  // Claude 4.5
  { sapName: 'anthropic--claude-4.5-sonnet', provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-sonnet-4-5'] },
  { sapName: 'anthropic--claude-4.5-opus',   provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-opus-4-5'] },
  { sapName: 'anthropic--claude-4.5-haiku',  provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-haiku-4-5'] },

  // Claude 4
  { sapName: 'anthropic--claude-4-sonnet', provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-sonnet-4'] },
  { sapName: 'anthropic--claude-4-opus',   provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-opus-4'] },

  // Claude 3.7
  { sapName: 'anthropic--claude-3.7-sonnet', provider: 'anthropic', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-3-7-sonnet-20250219', 'claude-3-7-sonnet-latest'] },

  // Claude 3.5
  { sapName: 'anthropic--claude-3.5-sonnet', provider: 'anthropic', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-3-5-sonnet-20241022', 'claude-3-5-sonnet-20240620', 'claude-3-5-sonnet-latest'] },
  { sapName: 'anthropic--claude-3.5-haiku',  provider: 'anthropic', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: true, anthropicAliases: ['claude-3-5-haiku-20241022', 'claude-3-5-haiku-latest'] },

  // Claude 3 (Invoke path — no Converse support)
  { sapName: 'anthropic--claude-3-opus',   provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: false, anthropicAliases: ['claude-3-opus-20240229', 'claude-3-opus-latest'] },
  { sapName: 'anthropic--claude-3-sonnet', provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: false, anthropicAliases: ['claude-3-sonnet-20240229'] },
  { sapName: 'anthropic--claude-3-haiku',  provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true, usesConverseApi: false, anthropicAliases: ['claude-3-haiku-20240307'] },

  // OpenAI
  { sapName: 'gpt-4o',      provider: 'openai', maxTokens: 16384,  contextWindow: 128000,  supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-4o-mini', provider: 'openai', maxTokens: 16384,  contextWindow: 128000,  supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-4',       provider: 'openai', maxTokens: 8192,   contextWindow: 8192,    supportsStreaming: true,                        usesConverseApi: false },
  { sapName: 'gpt-4.1',     provider: 'openai', maxTokens: 32768,  contextWindow: 1047576, supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-4.1-nano',provider: 'openai', maxTokens: 32768,  contextWindow: 1047576, supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-5',       provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-5-nano',  provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'gpt-5-mini',  provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true,  usesConverseApi: false },
  { sapName: 'o1',          provider: 'openai', maxTokens: 100000, contextWindow: 200000,  supportsStreaming: true,                        usesConverseApi: false },
  { sapName: 'o3-mini',     provider: 'openai', maxTokens: 100000, contextWindow: 200000,  supportsStreaming: false,                       usesConverseApi: false },
  { sapName: 'o3',          provider: 'openai', maxTokens: 100000, contextWindow: 200000,  supportsStreaming: true,                        usesConverseApi: false },
  { sapName: 'o4-mini',     provider: 'openai', maxTokens: 100000, contextWindow: 200000,  supportsStreaming: true,                        usesConverseApi: false },

  // Gemini
  { sapName: 'gemini-2.5-pro',   provider: 'gemini', maxTokens: 65536, contextWindow: 2097152, supportsStreaming: true, supportsVision: true, usesConverseApi: false },
  { sapName: 'gemini-2.5-flash', provider: 'gemini', maxTokens: 65536, contextWindow: 1048576, supportsStreaming: true, supportsVision: true, usesConverseApi: false },
  { sapName: 'gemini-1.5-pro',   provider: 'gemini', maxTokens: 8192,  contextWindow: 2097152, supportsStreaming: true, supportsVision: true, usesConverseApi: false },
  { sapName: 'gemini-1.5-flash', provider: 'gemini', maxTokens: 8192,  contextWindow: 1048576, supportsStreaming: true, supportsVision: true, usesConverseApi: false },

  // Perplexity
  { sapName: 'sonar-pro', provider: 'openai', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, usesConverseApi: false },
  { sapName: 'sonar',     provider: 'openai', maxTokens: 8192, contextWindow: 127072, supportsStreaming: true, usesConverseApi: false },

  // Meta (Llama)
  { sapName: 'meta--llama3-70b-instruct',    provider: 'meta', maxTokens: 8192, contextWindow: 8192,   supportsStreaming: true, usesConverseApi: false },
  { sapName: 'meta--llama3.1-70b-instruct',  provider: 'meta', maxTokens: 8192, contextWindow: 128000, supportsStreaming: true, usesConverseApi: false },

  // Mistral
  { sapName: 'mistralai--mixtral-8x7b-instruct-v01',     provider: 'mistral', maxTokens: 32768, contextWindow: 32768,  supportsStreaming: true, usesConverseApi: false },
  { sapName: 'mistralai--mistral-large-instruct-2407',   provider: 'mistral', maxTokens: 32768, contextWindow: 128000, supportsStreaming: true, usesConverseApi: false },
];

// O(1) lookup indices
const BY_SAP_NAME = new Map<string, CatalogueEntry>(CATALOGUE.map(e => [e.sapName, e]));
const BY_ANTHROPIC_ALIAS = new Map<string, CatalogueEntry>(
  CATALOGUE.flatMap(e => (e.anthropicAliases ?? []).map(a => [a, e] as [string, CatalogueEntry]))
);

// Returns undefined for unknown models (use for non-critical paths like model listing)
export function tryGetEntry(sapName: string): CatalogueEntry | undefined {
  return BY_SAP_NAME.get(sapName);
}

// Throws for unknown models — callers must keep the catalogue up to date
export function getEntry(sapName: string): CatalogueEntry {
  const entry = BY_SAP_NAME.get(sapName);
  if (!entry) {
    throw new Error(`Unknown model: "${sapName}". Add it to src/model-catalogue.ts to enable support.`);
  }
  return entry;
}

export function getModelInfo(sapName: string): ModelInfo {
  return getEntry(sapName);
}

export function getProvider(sapName: string): ModelProvider {
  return getEntry(sapName).provider;
}

export function usesConverseApi(sapName: string): boolean {
  return getEntry(sapName).usesConverseApi;
}

// For the OpenAI models API `owned_by` field (gemini → 'google' per convention)
export function getOwner(sapName: string): string {
  const provider = tryGetEntry(sapName)?.provider;
  if (!provider) return 'sap-ai-core';
  return provider === 'gemini' ? 'google' : String(provider);
}

// Maps an Anthropic SDK model name to its SAP AI Core name.
// Accepts SAP names (containing '--') as pass-through after catalogue validation.
export function mapFromAnthropic(name: string): string {
  if (name.includes('--')) {
    getEntry(name); // validate it exists
    return name;
  }
  const entry = BY_ANTHROPIC_ALIAS.get(name);
  if (entry) return entry.sapName;
  throw new Error(`Unknown Anthropic model: "${name}". Add it to src/model-catalogue.ts to enable support.`);
}
