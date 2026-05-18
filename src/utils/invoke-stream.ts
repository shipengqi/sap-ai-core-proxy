import { logger } from '../logger';

export type InvokeEvent =
  | { type: 'messageStart'; messageId: string; inputTokens: number }
  | { type: 'blockStart'; index: number; contentBlock: Record<string, unknown> }
  | { type: 'blockDelta'; index: number; delta: Record<string, unknown> }
  | { type: 'blockStop'; index: number }
  | { type: 'messageDelta'; stopReason: string; stopSequence: string | null; outputTokens: number }
  | { type: 'messageStop' };

export async function* parseInvokeStream(
  stream: NodeJS.ReadableStream
): AsyncGenerator<InvokeEvent> {
  let buffer = '';

  for await (const chunk of stream as AsyncIterable<Buffer>) {
    buffer += chunk.toString('utf-8');
    const lines = buffer.split('\n');
    buffer = lines.pop() ?? '';

    for (const line of lines) {
      if (line.trim() === '' || !line.startsWith('data: ')) continue;

      let data: Record<string, unknown>;
      try {
        data = JSON.parse(line.slice(6)) as Record<string, unknown>;
      } catch {
        logger.debug('parseInvokeStream: skipping non-JSON line');
        continue;
      }

      switch (data.type) {
        case 'message_start': {
          const msg = data.message as Record<string, unknown> | undefined;
          const usage = msg?.usage as Record<string, number> | undefined;
          yield {
            type: 'messageStart',
            messageId: (msg?.id as string) || '',
            inputTokens: usage?.input_tokens || 0,
          };
          break;
        }
        case 'content_block_start':
          yield {
            type: 'blockStart',
            index: (data.index as number) ?? 0,
            contentBlock: (data.content_block as Record<string, unknown>) || {},
          };
          break;
        case 'content_block_delta':
          yield {
            type: 'blockDelta',
            index: (data.index as number) ?? 0,
            delta: (data.delta as Record<string, unknown>) || {},
          };
          break;
        case 'content_block_stop':
          yield { type: 'blockStop', index: (data.index as number) ?? 0 };
          break;
        case 'message_delta': {
          const delta = data.delta as Record<string, unknown> | undefined;
          const usage = data.usage as Record<string, unknown> | undefined;
          yield {
            type: 'messageDelta',
            stopReason: (delta?.stop_reason as string) || 'end_turn',
            stopSequence: (delta?.stop_sequence as string | null) ?? null,
            outputTokens: (usage?.output_tokens as number) || 0,
          };
          break;
        }
        case 'message_stop':
          yield { type: 'messageStop' };
          break;
      }
    }
  }
}
