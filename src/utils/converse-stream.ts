import { v4 as uuidv4 } from 'uuid';
import { convertPythonJsonToStandardJson } from './json-parser';
import { logger } from '../logger';

// Appends a cachePoint to the last two user messages in a Converse-format array.
export function applyPromptCaching<T extends { role: string; content: unknown[] }>(messages: T[]): T[] {
  const userIndices: number[] = [];
  messages.forEach((msg, idx) => { if (msg.role === 'user') userIndices.push(idx); });

  const last = userIndices[userIndices.length - 1] ?? -1;
  const secondLast = userIndices[userIndices.length - 2] ?? -1;

  return messages.map((msg, idx): T => {
    if (idx === last || idx === secondLast) {
      return { ...msg, content: [...msg.content, { cachePoint: { type: 'default' } }] } as T;
    }
    return msg;
  });
}

export type ConverseEvent =
  | { type: 'metadata'; inputTokens: number; outputTokens: number }
  | { type: 'textBlockStart'; index: number }
  | { type: 'textDelta'; index: number; text: string }
  | { type: 'reasoningDelta'; index: number; text: string }
  | { type: 'textBlockStop'; index: number }
  | { type: 'toolBlockStart'; index: number; id: string; name: string }
  | { type: 'toolInputDelta'; index: number; partial_json: string }
  | { type: 'toolBlockStop'; index: number }
  | { type: 'messageStop'; stopReason: string };

export async function drainErrorBody(stream: NodeJS.ReadableStream): Promise<string> {
  let body = '';
  for await (const chunk of stream as AsyncIterable<Buffer>) {
    body += chunk.toString('utf-8');
  }
  return body;
}

export function parseErrorMessage(body: string): string {
  try {
    const data = JSON.parse(body) as Record<string, unknown>;
    const errors = data.errors as Record<string, unknown> | undefined;
    const error = data.error as Record<string, unknown> | undefined;
    return (errors?.message as string) || (error?.message as string) || (data.message as string) || body;
  } catch {
    return body || 'Unknown error';
  }
}

export async function* parseConverseStream(
  stream: NodeJS.ReadableStream
): AsyncGenerator<ConverseEvent> {
  // Track tool block indices to distinguish toolBlockStop from textBlockStop
  const toolBlocks = new Set<number>();
  let buffer = '';

  for await (const chunk of stream as AsyncIterable<Buffer>) {
    buffer += chunk.toString('utf-8');
    const lines = buffer.split('\n');
    buffer = lines.pop() ?? '';

    for (const line of lines) {
      if (line.trim() === '') continue;

      const raw = line.startsWith('data: ') ? line.slice(6) : line;
      let data: Record<string, unknown>;
      try {
        data = JSON.parse(convertPythonJsonToStandardJson(raw)) as Record<string, unknown>;
      } catch {
        logger.debug('parseConverseStream: skipping non-JSON line');
        continue;
      }

      // metadata.usage — primary source of final token counts
      const metadata = data.metadata as Record<string, unknown> | undefined;
      if (metadata?.usage) {
        const u = metadata.usage as Record<string, number>;
        yield {
          type: 'metadata',
          inputTokens: (u.inputTokens || 0) + (u.cacheReadInputTokens || 0) + (u.cacheWriteInputTokens || 0),
          outputTokens: u.outputTokens || 0,
        };
      }

      // messageStart — carries initial input token count
      if (data.messageStart) {
        const usage = (data.messageStart as Record<string, unknown>).usage as Record<string, number> | undefined;
        if (usage?.inputTokens) {
          yield { type: 'metadata', inputTokens: usage.inputTokens, outputTokens: 0 };
        }
      }

      // contentBlockStart
      if (data.contentBlockStart) {
        const blockStart = data.contentBlockStart as Record<string, unknown>;
        const idx = (blockStart.contentBlockIndex as number) ?? 0;
        const start = blockStart.start as Record<string, unknown> | undefined;

        if (start?.toolUse) {
          const toolUse = start.toolUse as Record<string, unknown>;
          const id = (toolUse.toolUseId as string) || `toolu_${uuidv4().replace(/-/g, '').slice(0, 24)}`;
          const name = toolUse.name as string;
          toolBlocks.add(idx);
          yield { type: 'toolBlockStart', index: idx, id, name };
        } else {
          yield { type: 'textBlockStart', index: idx };
        }
      }

      // contentBlockDelta
      if (data.contentBlockDelta) {
        const blockDelta = data.contentBlockDelta as Record<string, unknown>;
        const idx = (blockDelta.contentBlockIndex as number) ?? 0;
        const delta = blockDelta.delta as Record<string, unknown> | undefined;

        if (delta?.text) {
          yield { type: 'textDelta', index: idx, text: delta.text as string };
        } else if (delta?.reasoningContent) {
          const rc = delta.reasoningContent as Record<string, unknown>;
          if (rc.text) {
            yield { type: 'reasoningDelta', index: idx, text: rc.text as string };
          }
        } else if (delta?.toolUse) {
          const toolUseDelta = delta.toolUse as Record<string, unknown>;
          if (toolUseDelta.input !== undefined) {
            const partial_json = typeof toolUseDelta.input === 'string'
              ? toolUseDelta.input
              : JSON.stringify(toolUseDelta.input);
            yield { type: 'toolInputDelta', index: idx, partial_json };
          }
        }
      }

      // contentBlockStop
      if (data.contentBlockStop) {
        const blockStop = data.contentBlockStop as Record<string, unknown>;
        const idx = (blockStop.contentBlockIndex as number) ?? 0;
        if (toolBlocks.has(idx)) {
          toolBlocks.delete(idx);
          yield { type: 'toolBlockStop', index: idx };
        } else {
          yield { type: 'textBlockStop', index: idx };
        }
      }

      // messageStop
      if (data.messageStop) {
        const msgStop = data.messageStop as Record<string, unknown>;
        yield { type: 'messageStop', stopReason: (msgStop.stopReason as string) || 'end_turn' };
      }
    }
  }
}
