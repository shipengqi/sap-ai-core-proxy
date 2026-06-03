import { Response } from 'express';
import type { AxiosResponse } from 'axios';
import {
  setSSEHeaders,
  sendSSEEvent,
  endStreamOnError,
  sendOpenAIError,
  sendAnthropicError,
  parseConverseStream,
  parseInvokeStream,
  parseGeminiStream,
} from './index';
import { SapApiError } from '../sap-ai-core/client';
import type { OpenAIChatCompletionChunk } from '../types/openai';

export interface StreamContext {
  apiFormat: 'converse' | 'invoke' | 'gemini';
  responseFormat: 'anthropic' | 'openai';
  model: string;
  completionId: string;
}

export async function orchestrateStream(
  response: Pick<AxiosResponse, 'status' | 'data'>,
  context: StreamContext,
  res: Response
): Promise<void> {
  setSSEHeaders(res);

  try {
    if (context.apiFormat === 'converse') {
      await runConverseStream(response.data as NodeJS.ReadableStream, context, res);
    } else if (context.apiFormat === 'invoke') {
      await runInvokeStream(response.data as NodeJS.ReadableStream, context, res);
    } else {
      await runGeminiStream(response.data as NodeJS.ReadableStream, context, res);
    }
  } catch (error: unknown) {
    // SapApiError: upstream rejected the request before any stream bytes were processed.
    // Even though setSSEHeaders was already called, no data was written yet — we can
    // still override the response with a format-specific error JSON.
    if (error instanceof SapApiError) {
      if (context.responseFormat === 'anthropic') {
        sendAnthropicError(res, error.statusCode, error.message);
      } else {
        sendOpenAIError(res, error.statusCode, error.message);
      }
      return;
    }
    endStreamOnError(res, error instanceof Error ? error : new Error(String(error)));
  }
}

// ---------------------------------------------------------------------------
// Converse stream
// ---------------------------------------------------------------------------

async function runConverseStream(
  stream: NodeJS.ReadableStream,
  context: StreamContext,
  res: Response
): Promise<void> {
  const { model, completionId, responseFormat } = context;
  let inputTokens = 0;
  let outputTokens = 0;
  let stopReason = 'end_turn';

  if (responseFormat === 'anthropic') {
    sendSSEEvent(res, 'message_start', {
      type: 'message_start',
      message: {
        id: completionId,
        type: 'message',
        role: 'assistant',
        content: [],
        model,
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: 0, output_tokens: 0 },
      },
    });
    sendSSEEvent(res, 'ping', { type: 'ping' });
  }

  const created = Math.floor(Date.now() / 1000);

  for await (const event of parseConverseStream(stream)) {
    switch (event.type) {
      case 'metadata':
        inputTokens = event.inputTokens || inputTokens;
        outputTokens = event.outputTokens || outputTokens;
        break;

      case 'textBlockStart':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_start', {
            type: 'content_block_start',
            index: event.index,
            content_block: { type: 'text', text: '' },
          });
        }
        break;

      case 'textDelta':
      case 'reasoningDelta':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_delta', {
            type: 'content_block_delta',
            index: event.index,
            delta: { type: 'text_delta', text: event.text },
          });
        } else {
          const chunk = openaiChunk(completionId, created, model, { content: event.text }, null);
          res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        }
        break;

      case 'textBlockStop':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_stop', {
            type: 'content_block_stop',
            index: event.index,
          });
        }
        break;

      case 'toolBlockStart':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_start', {
            type: 'content_block_start',
            index: event.index,
            content_block: { type: 'tool_use', id: event.id, name: event.name, input: {} },
          });
        }
        break;

      case 'toolInputDelta':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_delta', {
            type: 'content_block_delta',
            index: event.index,
            delta: { type: 'input_json_delta', partial_json: event.partial_json },
          });
        }
        break;

      case 'toolBlockStop':
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_stop', {
            type: 'content_block_stop',
            index: event.index,
          });
        }
        break;

      case 'messageStop':
        stopReason = event.stopReason;
        break;
    }
  }

  if (responseFormat === 'anthropic') {
    sendSSEEvent(res, 'message_delta', {
      type: 'message_delta',
      delta: { stop_reason: stopReason, stop_sequence: null },
      usage: { output_tokens: outputTokens },
    });
    sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
  } else {
    if (inputTokens > 0 || outputTokens > 0) {
      const usageChunk: OpenAIChatCompletionChunk = {
        id: completionId,
        object: 'chat.completion.chunk',
        created,
        model,
        choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
        usage: {
          prompt_tokens: inputTokens,
          completion_tokens: outputTokens,
          total_tokens: inputTokens + outputTokens,
        },
      };
      res.write(`data: ${JSON.stringify(usageChunk)}\n\n`);
    }
    res.write('data: [DONE]\n\n');
  }

  res.end();
}

// ---------------------------------------------------------------------------
// Invoke stream
// ---------------------------------------------------------------------------

async function runInvokeStream(
  stream: NodeJS.ReadableStream,
  context: StreamContext,
  res: Response
): Promise<void> {
  const { model, completionId, responseFormat } = context;
  let inputTokens = 0;
  let outputTokens = 0;
  let blockStarted = false;
  let resolvedMessageId = completionId;

  const created = Math.floor(Date.now() / 1000);

  for await (const event of parseInvokeStream(stream)) {
    switch (event.type) {
      case 'messageStart': {
        inputTokens = event.inputTokens;
        resolvedMessageId = event.messageId || completionId;
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'message_start', {
            type: 'message_start',
            message: {
              id: resolvedMessageId,
              type: 'message',
              role: 'assistant',
              content: [],
              model,
              stop_reason: null,
              stop_sequence: null,
              usage: { input_tokens: inputTokens, output_tokens: 1 },
            },
          });
          sendSSEEvent(res, 'ping', { type: 'ping' });
        } else {
          const chunk = openaiChunk(completionId, created, model, { role: 'assistant' }, null);
          res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        }
        break;
      }

      case 'blockStart': {
        blockStarted = true;
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_start', {
            type: 'content_block_start',
            index: event.index,
            content_block: event.contentBlock,
          });
        }
        break;
      }

      case 'blockDelta': {
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_delta', {
            type: 'content_block_delta',
            index: event.index,
            delta: event.delta,
          });
        } else if (event.delta.type === 'text_delta' && event.delta.text) {
          const chunk = openaiChunk(completionId, created, model, { content: event.delta.text as string }, null);
          res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        }
        break;
      }

      case 'blockStop': {
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'content_block_stop', {
            type: 'content_block_stop',
            index: event.index,
          });
        }
        break;
      }

      case 'messageDelta': {
        outputTokens = event.outputTokens;
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'message_delta', {
            type: 'message_delta',
            delta: { stop_reason: event.stopReason, stop_sequence: event.stopSequence },
            usage: { output_tokens: outputTokens },
          });
        } else {
          const chunk = openaiChunk(completionId, created, model, {}, event.stopReason === 'end_turn' ? 'stop' : event.stopReason as 'stop' | 'length');
          res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        }
        break;
      }

      case 'messageStop': {
        if (responseFormat === 'anthropic') {
          sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
        }
        break;
      }
    }
  }

  // Fallback for empty Invoke response (no blocks received)
  if (responseFormat === 'anthropic' && !blockStarted) {
    sendSSEEvent(res, 'message_start', {
      type: 'message_start',
      message: {
        id: resolvedMessageId,
        type: 'message',
        role: 'assistant',
        content: [],
        model,
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: inputTokens, output_tokens: 1 },
      },
    });
    sendSSEEvent(res, 'content_block_start', {
      type: 'content_block_start',
      index: 0,
      content_block: { type: 'text', text: '' },
    });
    sendSSEEvent(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
    sendSSEEvent(res, 'message_delta', {
      type: 'message_delta',
      delta: { stop_reason: 'end_turn', stop_sequence: null },
      usage: { output_tokens: outputTokens },
    });
    sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
  }

  if (responseFormat === 'openai' && (inputTokens > 0 || outputTokens > 0)) {
    const usageChunk: OpenAIChatCompletionChunk = {
      id: completionId,
      object: 'chat.completion.chunk',
      created,
      model,
      choices: [],
      usage: {
        prompt_tokens: inputTokens,
        completion_tokens: outputTokens,
        total_tokens: inputTokens + outputTokens,
      },
    };
    res.write(`data: ${JSON.stringify(usageChunk)}\n\n`);
  }

  if (responseFormat === 'openai') {
    res.write('data: [DONE]\n\n');
  }

  res.end();
}

// ---------------------------------------------------------------------------
// Gemini stream
// ---------------------------------------------------------------------------

async function runGeminiStream(
  stream: NodeJS.ReadableStream,
  context: StreamContext,
  res: Response
): Promise<void> {
  const { model, completionId } = context;
  let promptTokens = 0;
  let outputTokens = 0;
  const created = Math.floor(Date.now() / 1000);

  // Initial role chunk
  res.write(`data: ${JSON.stringify(openaiChunk(completionId, created, model, { role: 'assistant' }, null))}\n\n`);

  for await (const event of parseGeminiStream(stream)) {
    switch (event.type) {
      case 'textDelta': {
        const chunk = openaiChunk(completionId, created, model, { content: event.text }, null);
        res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        break;
      }
      case 'metadata':
        promptTokens = event.promptTokens;
        outputTokens = event.outputTokens;
        break;
    }
  }

  const finishChunk = openaiChunk(completionId, created, model, {}, 'stop');
  res.write(`data: ${JSON.stringify(finishChunk)}\n\n`);

  if (promptTokens > 0 || outputTokens > 0) {
    const usageChunk: OpenAIChatCompletionChunk = {
      id: completionId,
      object: 'chat.completion.chunk',
      created,
      model,
      choices: [],
      usage: {
        prompt_tokens: promptTokens,
        completion_tokens: outputTokens,
        total_tokens: promptTokens + outputTokens,
      },
    };
    res.write(`data: ${JSON.stringify(usageChunk)}\n\n`);
  }

  res.write('data: [DONE]\n\n');
  res.end();
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function openaiChunk(
  id: string,
  created: number,
  model: string,
  delta: Record<string, unknown>,
  finish_reason: 'stop' | 'length' | null
): OpenAIChatCompletionChunk {
  return {
    id,
    object: 'chat.completion.chunk',
    created,
    model,
    choices: [{ index: 0, delta, finish_reason }],
  };
}
