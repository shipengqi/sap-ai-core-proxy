/**
 * Models that use the SAP AI Core Converse/Converse-Stream endpoint
 * instead of the Invoke endpoint. This is the single source of truth
 * used by both OpenAI-compatible and Anthropic-native proxy paths.
 */
export const CONVERSE_STREAM_MODELS: readonly string[] = [
  'anthropic--claude-4.6-opus',
  'anthropic--claude-4.6-sonnet',
  'anthropic--claude-4.6-haiku',
  'anthropic--claude-4.5-sonnet',
  'anthropic--claude-4.5-opus',
  'anthropic--claude-4.5-haiku',
  'anthropic--claude-4-sonnet',
  'anthropic--claude-4-opus',
  'anthropic--claude-3.7-sonnet',
  'anthropic--claude-3.5-sonnet',
  'anthropic--claude-3.5-haiku',
];

/**
 * Determines if a SAP AI Core model name should use the Converse API.
 */
export function useConverseApi(sapModelName: string): boolean {
  return CONVERSE_STREAM_MODELS.some(m =>
    sapModelName.toLowerCase().includes(m.toLowerCase()) ||
    m.toLowerCase().includes(sapModelName.toLowerCase())
  );
}
