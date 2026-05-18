import { AnthropicContentBlock, AnthropicTextContent } from '../types/anthropic';
import { applyPromptCaching } from './converse-stream';

export function extractSystemPrompt(
  system: string | Array<{ type: string; text: string }> | undefined
): string {
  if (!system) return '';
  if (typeof system === 'string') return system;
  return system.filter(s => s.type === 'text').map(s => s.text).join('\n');
}

export function contentBlockToText(content: string | AnthropicContentBlock[]): string {
  if (typeof content === 'string') return content;
  return content.filter(b => b.type === 'text').map(b => (b as AnthropicTextContent).text).join('');
}

export function mapConverseStopReasonToOpenAI(
  stopReason: string | undefined
): 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null {
  if (!stopReason) return null;
  switch (stopReason) {
    case 'end_turn':
    case 'stop_sequence': return 'stop';
    case 'max_tokens':    return 'length';
    case 'tool_use':      return 'tool_calls';
    default:              return 'stop';
  }
}

export function assembleConversePayload(params: {
  maxTokens: number;
  temperature: number;
  messages: Array<{ role: 'user' | 'assistant'; content: unknown[] }>;
  system?: string;
  topP?: number;
  stopSequences?: string[];
  toolConfig?: Record<string, unknown>;
}): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    inferenceConfig: {
      maxTokens: params.maxTokens,
      temperature: params.temperature,
      ...(params.topP !== undefined && { topP: params.topP }),
      ...(params.stopSequences?.length && { stopSequences: params.stopSequences }),
    },
    messages: applyPromptCaching(params.messages),
  };
  if (params.system) {
    payload.system = [{ text: params.system }, { cachePoint: { type: 'default' } }];
  }
  if (params.toolConfig) {
    payload.toolConfig = params.toolConfig;
  }
  return payload;
}
