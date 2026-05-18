import { logger } from '../logger';

export type GeminiEvent =
  | { type: 'textDelta'; text: string }
  | { type: 'reasoningDelta'; text: string }
  | { type: 'metadata'; promptTokens: number; outputTokens: number };

type GeminiPart = { thought?: boolean; text?: string };
type GeminiCandidate = { content?: { parts?: GeminiPart[] } };
type GeminiChunk = {
  candidates?: GeminiCandidate[];
  usageMetadata?: { promptTokenCount?: number; candidatesTokenCount?: number };
};

export async function* parseGeminiStream(
  stream: NodeJS.ReadableStream
): AsyncGenerator<GeminiEvent> {
  let buffer = '';

  for await (const chunk of stream as AsyncIterable<Buffer>) {
    buffer += chunk.toString('utf-8');
    const lines = buffer.split('\n');
    buffer = lines.pop() ?? '';

    for (const line of lines) {
      if (line.trim() === '' || !line.startsWith('data: ')) continue;

      let data: GeminiChunk;
      try {
        data = JSON.parse(line.slice(6)) as GeminiChunk;
      } catch {
        logger.debug('parseGeminiStream: skipping non-JSON line');
        continue;
      }

      const parts = data.candidates?.[0]?.content?.parts;
      if (parts) {
        for (const part of parts) {
          if (!part.text) continue;
          if (part.thought) {
            yield { type: 'reasoningDelta', text: part.text };
          } else {
            yield { type: 'textDelta', text: part.text };
          }
        }
      }

      const usage = data.usageMetadata;
      if (usage) {
        yield {
          type: 'metadata',
          promptTokens: usage.promptTokenCount ?? 0,
          outputTokens: usage.candidatesTokenCount ?? 0,
        };
      }
    }
  }
}
