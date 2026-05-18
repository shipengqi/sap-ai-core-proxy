import { describe, it, expect } from 'vitest';
import { Readable } from 'stream';

// Helpers ---------------------------------------------------------------

function makeStream(...chunks: string[]): Readable {
  const r = new Readable({ read() {} });
  process.nextTick(() => {
    for (const c of chunks) r.push(c);
    r.push(null);
  });
  return r;
}

async function collectInvoke(stream: NodeJS.ReadableStream) {
  const { parseInvokeStream } = await import('../src/utils/invoke-stream');
  const events = [];
  for await (const event of parseInvokeStream(stream)) events.push(event);
  return events;
}

async function collectGemini(stream: NodeJS.ReadableStream) {
  const { parseGeminiStream } = await import('../src/utils/gemini-stream');
  const events = [];
  for await (const event of parseGeminiStream(stream)) events.push(event);
  return events;
}

// -----------------------------------------------------------------------
// parseInvokeStream
// -----------------------------------------------------------------------

describe('parseInvokeStream', () => {
  it('parses message_start into messageStart event', async () => {
    const stream = makeStream(
      'data: {"type":"message_start","message":{"id":"msg_abc","usage":{"input_tokens":10}}}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({ type: 'messageStart', messageId: 'msg_abc', inputTokens: 10 });
  });

  it('parses content_block_start into blockStart event', async () => {
    const stream = makeStream(
      'data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({
      type: 'blockStart',
      index: 0,
      contentBlock: { type: 'text', text: '' },
    });
  });

  it('parses content_block_delta into blockDelta event', async () => {
    const stream = makeStream(
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({
      type: 'blockDelta',
      index: 0,
      delta: { type: 'text_delta', text: 'hello' },
    });
  });

  it('parses content_block_stop into blockStop event', async () => {
    const stream = makeStream(
      'data: {"type":"content_block_stop","index":0}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({ type: 'blockStop', index: 0 });
  });

  it('parses message_delta into messageDelta event', async () => {
    const stream = makeStream(
      'data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({
      type: 'messageDelta',
      stopReason: 'end_turn',
      stopSequence: null,
      outputTokens: 25,
    });
  });

  it('parses message_stop into messageStop event', async () => {
    const stream = makeStream(
      'data: {"type":"message_stop"}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({ type: 'messageStop' });
  });

  it('stitches events split across chunk boundaries', async () => {
    const stream = makeStream(
      'data: {"type":"content_block_del',
      'ta","index":0,"delta":{"type":"text_delta","text":"hi"}}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toContainEqual({ type: 'blockDelta', index: 0, delta: { type: 'text_delta', text: 'hi' } });
  });

  it('skips empty lines and non-data lines', async () => {
    const stream = makeStream(
      '\n',
      ': keep-alive\n',
      'data: {"type":"message_stop"}\n\n'
    );
    const events = await collectInvoke(stream);
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ type: 'messageStop' });
  });

  it('emits all events from a complete conversation turn', async () => {
    const stream = makeStream(
      'data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5}}}\n\n',
      'data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n\n',
      'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}\n\n',
      'data: {"type":"content_block_stop","index":0}\n\n',
      'data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}\n\n',
      'data: {"type":"message_stop"}\n\n'
    );
    const events = await collectInvoke(stream);
    const types = events.map(e => e.type);
    expect(types).toEqual(['messageStart', 'blockStart', 'blockDelta', 'blockStop', 'messageDelta', 'messageStop']);
  });
});

// -----------------------------------------------------------------------
// parseGeminiStream
// -----------------------------------------------------------------------

function geminiLine(obj: unknown): string {
  return `data: ${JSON.stringify(obj)}\n\n`;
}

describe('parseGeminiStream', () => {
  it('parses regular text parts into textDelta events', async () => {
    const stream = makeStream(
      geminiLine({
        candidates: [{ content: { parts: [{ text: 'hello world' }] } }],
      })
    );
    const events = await collectGemini(stream);
    expect(events).toContainEqual({ type: 'textDelta', text: 'hello world' });
  });

  it('parses thought parts into reasoningDelta events', async () => {
    const stream = makeStream(
      geminiLine({
        candidates: [{ content: { parts: [{ thought: true, text: 'let me think' }] } }],
      })
    );
    const events = await collectGemini(stream);
    expect(events).toContainEqual({ type: 'reasoningDelta', text: 'let me think' });
  });

  it('separates thought and non-thought parts in the same response', async () => {
    const stream = makeStream(
      geminiLine({
        candidates: [{
          content: {
            parts: [
              { thought: true, text: 'thinking...' },
              { text: 'answer' },
            ],
          },
        }],
      })
    );
    const events = await collectGemini(stream);
    expect(events).toContainEqual({ type: 'reasoningDelta', text: 'thinking...' });
    expect(events).toContainEqual({ type: 'textDelta', text: 'answer' });
  });

  it('parses usageMetadata into metadata event', async () => {
    const stream = makeStream(
      geminiLine({
        usageMetadata: { promptTokenCount: 10, candidatesTokenCount: 5 },
      })
    );
    const events = await collectGemini(stream);
    expect(events).toContainEqual({ type: 'metadata', promptTokens: 10, outputTokens: 5 });
  });

  it('emits metadata and text from the same response line', async () => {
    const stream = makeStream(
      geminiLine({
        candidates: [{ content: { parts: [{ text: 'done' }] } }],
        usageMetadata: { promptTokenCount: 8, candidatesTokenCount: 2 },
      })
    );
    const events = await collectGemini(stream);
    expect(events).toContainEqual({ type: 'textDelta', text: 'done' });
    expect(events).toContainEqual({ type: 'metadata', promptTokens: 8, outputTokens: 2 });
  });

  it('skips empty lines', async () => {
    const stream = makeStream(
      '\n',
      geminiLine({ candidates: [{ content: { parts: [{ text: 'x' }] } }] })
    );
    const events = await collectGemini(stream);
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ type: 'textDelta', text: 'x' });
  });

  it('handles multiple text parts per response line', async () => {
    const stream = makeStream(
      geminiLine({
        candidates: [{
          content: { parts: [{ text: 'foo' }, { text: 'bar' }] },
        }],
      })
    );
    const events = await collectGemini(stream);
    const textEvents = events.filter(e => e.type === 'textDelta');
    expect(textEvents).toHaveLength(2);
    expect(textEvents[0]).toMatchObject({ text: 'foo' });
    expect(textEvents[1]).toMatchObject({ text: 'bar' });
  });
});
