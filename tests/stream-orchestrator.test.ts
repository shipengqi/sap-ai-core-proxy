import { describe, it, expect, vi } from 'vitest';
import { Readable } from 'stream';
import type { Response } from 'express';
import { orchestrateStream } from '../src/utils/stream-orchestrator';
import { SapApiError } from '../src/sap-ai-core/client';
import type { StreamContext } from '../src/utils/stream-orchestrator';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function mockRes() {
  let _headersSent = false;
  const json = vi.fn();
  const write = vi.fn().mockImplementation(() => { _headersSent = true; });
  const end = vi.fn();
  const setHeader = vi.fn().mockImplementation(() => { _headersSent = true; });
  const statusChain = { json };
  const status = vi.fn().mockReturnValue(statusChain);
  const res = {
    get headersSent() { return _headersSent; },
    status, json, write, end, setHeader,
  } as unknown as Response;
  return { res, status, json, write, end, setHeader };
}

/** Wrap raw SSE line(s) in a Readable. Each line is terminated with \n. */
function makeStream(...lines: string[]): { status: number; data: NodeJS.ReadableStream } {
  return {
    status: 200,
    data: Readable.from(lines.map(l => Buffer.from(l + '\n'))),
  };
}

function errorStream200(firstLine: string): { status: number; data: NodeJS.ReadableStream } {
  async function* gen(): AsyncGenerator<Buffer> {
    yield Buffer.from(firstLine + '\n');
    throw new Error('mid-stream network error');
  }
  return { status: 200, data: Readable.from(gen()) };
}

/** Simulate a SapApiError being thrown by postStream() before orchestrateStream is called.
 *  We wrap the call so the orchestrator receives a rejected promise. */
async function orchestrateWithSapApiError(
  statusCode: number,
  message: string,
  context: StreamContext,
  res: Response
): Promise<void> {
  // In real usage, postStream() throws before orchestrateStream is called.
  // We simulate the error surfacing inside orchestrateStream via a stream
  // that immediately throws SapApiError on first read.
  async function* failStream(): AsyncGenerator<Buffer> {
    throw new SapApiError(statusCode, message);
    yield Buffer.from(''); // unreachable, satisfies type
  }
  const response = { status: 200, data: Readable.from(failStream()) };
  await orchestrateStream(response, context, res);
}

// Parse res.write() calls into structured objects for easy assertions
function parseWrites(writeMock: ReturnType<typeof vi.fn>): Array<{ event?: string; data: unknown } | { done: true } | { raw: string }> {
  return writeMock.mock.calls.map((call) => {
    const raw = call[0] as string;
    if (raw === 'data: [DONE]\n\n') return { done: true };
    // Anthropic SSE: "event: foo\ndata: {...}\n\n"
    const anthMatch = raw.match(/^event: ([^\n]+)\ndata: (.+)\n\n$/s);
    if (anthMatch) return { event: anthMatch[1], data: JSON.parse(anthMatch[2]) };
    // OpenAI chunk: "data: {...}\n\n"
    if (raw.startsWith('data: ')) return { data: JSON.parse(raw.slice(6).trimEnd()) };
    return { raw };
  });
}

// Converse-format (SAP Converse API) raw lines — no "data: " prefix needed
const C = {
  metadata: (input: number, output: number) =>
    JSON.stringify({ metadata: { usage: { inputTokens: input, outputTokens: output } } }),
  textBlockStart: (index: number) =>
    JSON.stringify({ contentBlockStart: { contentBlockIndex: index, start: {} } }),
  textDelta: (index: number, text: string) =>
    JSON.stringify({ contentBlockDelta: { contentBlockIndex: index, delta: { text } } }),
  textBlockStop: (index: number) =>
    JSON.stringify({ contentBlockStop: { contentBlockIndex: index } }),
  toolBlockStart: (index: number, id: string, name: string) =>
    JSON.stringify({ contentBlockStart: { contentBlockIndex: index, start: { toolUse: { toolUseId: id, name } } } }),
  toolInputDelta: (index: number, partial_json: string) =>
    JSON.stringify({ contentBlockDelta: { contentBlockIndex: index, delta: { toolUse: { input: partial_json } } } }),
  toolBlockStop: (index: number) =>
    JSON.stringify({ contentBlockStop: { contentBlockIndex: index } }),
  messageStop: (stopReason = 'end_turn') =>
    JSON.stringify({ messageStop: { stopReason } }),
};

// Invoke-format (Anthropic native SSE) — must start with "data: "
const I = {
  messageStart: (id: string, inputTokens: number) =>
    `data: ${JSON.stringify({ type: 'message_start', message: { id, usage: { input_tokens: inputTokens } } })}`,
  blockStart: (index: number, contentBlock: unknown) =>
    `data: ${JSON.stringify({ type: 'content_block_start', index, content_block: contentBlock })}`,
  blockDelta: (index: number, delta: unknown) =>
    `data: ${JSON.stringify({ type: 'content_block_delta', index, delta })}`,
  blockStop: (index: number) =>
    `data: ${JSON.stringify({ type: 'content_block_stop', index })}`,
  messageDelta: (stopReason: string, outputTokens: number) =>
    `data: ${JSON.stringify({ type: 'message_delta', delta: { stop_reason: stopReason, stop_sequence: null }, usage: { output_tokens: outputTokens } })}`,
  messageStop: () =>
    `data: ${JSON.stringify({ type: 'message_stop' })}`,
};

// Gemini-format SSE — must start with "data: "
const G = {
  textDelta: (text: string) =>
    `data: ${JSON.stringify({ candidates: [{ content: { parts: [{ text }] } }] })}`,
  metadata: (promptTokens: number, outputTokens: number) =>
    `data: ${JSON.stringify({ usageMetadata: { promptTokenCount: promptTokens, candidatesTokenCount: outputTokens } })}`,
};

const CTX = {
  converseAnthropic: (model = 'claude-3-5-sonnet', id = 'msg_test'): StreamContext => ({
    apiFormat: 'converse', responseFormat: 'anthropic', model, completionId: id,
  }),
  invokeAnthropic: (model = 'claude-3-haiku', id = 'msg_test'): StreamContext => ({
    apiFormat: 'invoke', responseFormat: 'anthropic', model, completionId: id,
  }),
  converseOpenAI: (model = 'claude-3-5-sonnet', id = 'chatcmpl-test'): StreamContext => ({
    apiFormat: 'converse', responseFormat: 'openai', model, completionId: id,
  }),
  invokeOpenAI: (model = 'claude-3-haiku', id = 'chatcmpl-test'): StreamContext => ({
    apiFormat: 'invoke', responseFormat: 'openai', model, completionId: id,
  }),
  geminiOpenAI: (model = 'gemini-pro', id = 'chatcmpl-test'): StreamContext => ({
    apiFormat: 'gemini', responseFormat: 'openai', model, completionId: id,
  }),
};

// ---------------------------------------------------------------------------
// Tests — pre-stream 4xx error gate
// ---------------------------------------------------------------------------

describe('orchestrateStream — pre-stream 4xx errors (SapApiError before data)', () => {
  it('SapApiError before bytes: Anthropic responseFormat → json with Anthropic error shape', async () => {
    // SapApiError thrown from stream on first read, before any res.write()
    async function* failStream(): AsyncGenerator<Buffer> {
      throw new SapApiError(429, 'quota exceeded');
      yield Buffer.from(''); // unreachable
    }
    const { res, status, json } = mockRes();
    const response = { status: 200, data: Readable.from(failStream()) };

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    expect(status).toHaveBeenCalledWith(429);
    expect(json).toHaveBeenCalledWith({
      type: 'error',
      error: { type: 'api_error', message: 'quota exceeded' },
    });
  });

  it('SapApiError before bytes: OpenAI responseFormat → json with OpenAI error shape', async () => {
    async function* failStream(): AsyncGenerator<Buffer> {
      throw new SapApiError(429, 'rate limited');
      yield Buffer.from(''); // unreachable
    }
    const { res, status, json } = mockRes();
    const response = { status: 200, data: Readable.from(failStream()) };

    await orchestrateStream(response, CTX.converseOpenAI(), res);

    expect(status).toHaveBeenCalledWith(429);
    expect(json).toHaveBeenCalledWith({
      error: { message: 'rate limited', type: 'api_error', param: null, code: '429' },
    });
  });
});

// ---------------------------------------------------------------------------
// Tests — Converse → Anthropic
// ---------------------------------------------------------------------------

describe('orchestrateStream — Converse → Anthropic', () => {
  it('textDelta event produces content_block_delta SSE with text_delta', async () => {
    const { res, write, end } = mockRes();
    const response = makeStream(
      C.textBlockStart(0),
      C.textDelta(0, 'hello'),
      C.textBlockStop(0),
      C.messageStop(),
    );

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    const writes = parseWrites(write);
    expect(writes).toContainEqual({
      event: 'content_block_delta',
      data: { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'hello' } },
    });
    expect(end).toHaveBeenCalled();
  });

  it('metadata event causes token counts to appear in message_delta SSE', async () => {
    const { res, write } = mockRes();
    const response = makeStream(
      C.metadata(10, 20),
      C.messageStop(),
    );

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    const writes = parseWrites(write);
    const messageDelta = writes.find(w => 'event' in w && w.event === 'message_delta');
    expect(messageDelta).toBeDefined();
    expect((messageDelta as { event: string; data: Record<string, unknown> }).data).toMatchObject({
      usage: { output_tokens: 20 },
    });
  });

  it('messageStop stopReason propagates to message_delta stop_reason', async () => {
    const { res, write } = mockRes();
    const response = makeStream(C.messageStop('max_tokens'));

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    const writes = parseWrites(write);
    const messageDelta = writes.find(w => 'event' in w && w.event === 'message_delta');
    expect((messageDelta as { event: string; data: Record<string, unknown> }).data).toMatchObject({
      delta: { stop_reason: 'max_tokens' },
    });
  });

  it('toolBlockStart → tool_use content_block_start; toolInputDelta → input_json_delta', async () => {
    const { res, write } = mockRes();
    const response = makeStream(
      C.toolBlockStart(0, 'toolu_abc', 'get_weather'),
      C.toolInputDelta(0, '{"city":'),
      C.toolBlockStop(0),
      C.messageStop('tool_use'),
    );

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    const writes = parseWrites(write);
    expect(writes).toContainEqual({
      event: 'content_block_start',
      data: expect.objectContaining({
        content_block: { type: 'tool_use', id: 'toolu_abc', name: 'get_weather', input: {} },
      }),
    });
    expect(writes).toContainEqual({
      event: 'content_block_delta',
      data: expect.objectContaining({
        delta: { type: 'input_json_delta', partial_json: '{"city":' },
      }),
    });
  });

  it('emits message_start with completionId, ping, and message_stop', async () => {
    const { res, write } = mockRes();
    const response = makeStream(C.messageStop());

    await orchestrateStream(response, CTX.converseAnthropic('claude-3-5-sonnet', 'msg_abc'), res);

    const writes = parseWrites(write);
    const messageStart = writes.find(w => 'event' in w && w.event === 'message_start');
    expect((messageStart as { event: string; data: Record<string, unknown> }).data).toMatchObject({
      message: expect.objectContaining({ id: 'msg_abc', model: 'claude-3-5-sonnet' }),
    });
    expect(writes).toContainEqual({ event: 'ping', data: { type: 'ping' } });
    expect(writes).toContainEqual({ event: 'message_stop', data: { type: 'message_stop' } });
  });
});

// ---------------------------------------------------------------------------
// Tests — Invoke → Anthropic
// ---------------------------------------------------------------------------

describe('orchestrateStream — Invoke → Anthropic', () => {
  it('blockDelta text event produces content_block_delta SSE', async () => {
    const { res, write, end } = mockRes();
    const response = makeStream(
      I.messageStart('msg_srv', 5),
      I.blockStart(0, { type: 'text', text: '' }),
      I.blockDelta(0, { type: 'text_delta', text: 'world' }),
      I.blockStop(0),
      I.messageDelta('end_turn', 8),
      I.messageStop(),
    );

    await orchestrateStream(response, CTX.invokeAnthropic(), res);

    const writes = parseWrites(write);
    expect(writes).toContainEqual({
      event: 'content_block_delta',
      data: { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'world' } },
    });
    expect(end).toHaveBeenCalled();
  });

  it('uses messageId from messageStart event in message_start SSE', async () => {
    const { res, write } = mockRes();
    const response = makeStream(
      I.messageStart('msg_from_server', 5),
      I.messageDelta('end_turn', 8),
      I.messageStop(),
    );

    await orchestrateStream(response, CTX.invokeAnthropic(), res);

    const writes = parseWrites(write);
    const messageStart = writes.find(w => 'event' in w && w.event === 'message_start');
    expect((messageStart as { event: string; data: Record<string, unknown> }).data).toMatchObject({
      message: expect.objectContaining({ id: 'msg_from_server' }),
    });
  });
});

// ---------------------------------------------------------------------------
// Tests — Converse → OpenAI
// ---------------------------------------------------------------------------

describe('orchestrateStream — Converse → OpenAI', () => {
  it('textDelta produces OpenAI content chunk', async () => {
    const { res, write, end } = mockRes();
    const response = makeStream(
      C.textBlockStart(0),
      C.textDelta(0, 'hi'),
      C.textBlockStop(0),
      C.messageStop(),
    );

    await orchestrateStream(response, CTX.converseOpenAI('claude-3-5-sonnet', 'chatcmpl-x'), res);

    const writes = parseWrites(write);
    const contentChunk = writes.find(
      w => 'data' in w && !('done' in w) && (w.data as Record<string, unknown>).choices !== undefined
        && ((w.data as Record<string, unknown>).choices as Array<Record<string, unknown>>)[0]?.delta !== undefined
        && ((w.data as Record<string, unknown>).choices as Array<Record<string, unknown>>)[0]?.finish_reason === null
        && ((w.data as Record<string, unknown>).choices as Array<Record<string, unknown>>)[0]?.delta !== null
        && typeof (((w.data as Record<string, unknown>).choices as Array<Record<string, unknown>>)[0]?.delta as Record<string, unknown>)?.content === 'string'
    );
    expect(contentChunk).toBeDefined();
    expect((contentChunk as { data: Record<string, unknown> }).data).toMatchObject({
      id: 'chatcmpl-x',
      model: 'claude-3-5-sonnet',
      choices: [{ delta: { content: 'hi' }, finish_reason: null }],
    });
    expect(writes).toContainEqual({ done: true });
    expect(end).toHaveBeenCalled();
  });

  it('metadata event produces usage chunk with prompt_tokens and completion_tokens', async () => {
    const { res, write } = mockRes();
    const response = makeStream(
      C.metadata(15, 30),
      C.messageStop(),
    );

    await orchestrateStream(response, CTX.converseOpenAI(), res);

    const writes = parseWrites(write);
    const usageChunk = writes.find(
      w => 'data' in w && !('done' in w) && (w.data as Record<string, unknown>).usage !== undefined
    );
    expect(usageChunk).toBeDefined();
    expect((usageChunk as { data: Record<string, unknown> }).data).toMatchObject({
      usage: { prompt_tokens: 15, completion_tokens: 30, total_tokens: 45 },
    });
  });
});

// ---------------------------------------------------------------------------
// Tests — Invoke → OpenAI
// ---------------------------------------------------------------------------

describe('orchestrateStream — Invoke → OpenAI', () => {
  it('text blockDelta produces OpenAI content chunk', async () => {
    const { res, write, end } = mockRes();
    const response = makeStream(
      I.messageStart('msg_x', 5),
      I.blockStart(0, { type: 'text', text: '' }),
      I.blockDelta(0, { type: 'text_delta', text: 'foo' }),
      I.blockStop(0),
      I.messageDelta('end_turn', 8),
      I.messageStop(),
    );

    await orchestrateStream(response, CTX.invokeOpenAI('claude-3-haiku', 'chatcmpl-y'), res);

    const writes = parseWrites(write);
    expect(writes).toContainEqual(
      expect.objectContaining({ data: expect.objectContaining({ choices: [expect.objectContaining({ delta: { content: 'foo' } })] }) })
    );
    expect(writes).toContainEqual({ done: true });
    expect(end).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Tests — Gemini → OpenAI
// ---------------------------------------------------------------------------

describe('orchestrateStream — Gemini → OpenAI', () => {
  it('textDelta produces OpenAI content chunk and metadata produces usage chunk', async () => {
    const { res, write, end } = mockRes();
    const response = makeStream(
      G.textDelta('hello gemini'),
      G.metadata(8, 12),
    );

    await orchestrateStream(response, CTX.geminiOpenAI('gemini-2.0-flash', 'chatcmpl-g'), res);

    const writes = parseWrites(write);
    // Initial role chunk
    expect(writes).toContainEqual(
      expect.objectContaining({ data: expect.objectContaining({ choices: [expect.objectContaining({ delta: { role: 'assistant' } })] }) })
    );
    // Content chunk
    expect(writes).toContainEqual(
      expect.objectContaining({ data: expect.objectContaining({ choices: [expect.objectContaining({ delta: { content: 'hello gemini' } })] }) })
    );
    // Usage chunk
    const usageChunk = writes.find(
      w => 'data' in w && !('done' in w) && (w.data as Record<string, unknown>).usage !== undefined
    );
    expect((usageChunk as { data: Record<string, unknown> }).data).toMatchObject({
      usage: { prompt_tokens: 8, completion_tokens: 12, total_tokens: 20 },
    });
    expect(writes).toContainEqual({ done: true });
    expect(end).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Tests — mid-stream error
// ---------------------------------------------------------------------------

describe('orchestrateStream — mid-stream error', () => {
  it('calls endStreamOnError: writes [DONE] and ends when headers already sent', async () => {
    const { res, write, end } = mockRes();
    const response = {
      status: 200,
      data: (() => {
        async function* gen(): AsyncGenerator<Buffer> {
          yield Buffer.from(C.textDelta(0, 'partial') + '\n');
          throw new Error('network dropped');
        }
        return Readable.from(gen());
      })(),
    };

    await orchestrateStream(response, CTX.converseAnthropic(), res);

    // After setSSEHeaders fires and write is called, headersSent becomes true.
    // endStreamOnError then writes [DONE] and ends.
    expect(write).toHaveBeenCalledWith('data: [DONE]\n\n');
    expect(end).toHaveBeenCalled();
  });
});
