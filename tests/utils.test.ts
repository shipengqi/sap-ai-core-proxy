import { describe, it, expect, vi } from 'vitest';
import type { Response } from 'express';
import { extractErrorDetails, sendOpenAIError, sendAnthropicError } from '../src/utils/error-handler';
import { convertPythonJsonToStandardJson } from '../src/utils/json-parser';
import { extractTextContent } from '../src/utils/content-extractor';
import { applyPromptCaching, parseErrorMessage } from '../src/utils/converse-stream';
import { endStreamOnError } from '../src/utils/sse';

function mockRes() {
  const json = vi.fn();
  const chainable = { json };
  const status = vi.fn().mockReturnValue(chainable);
  const res = { status } as unknown as Response;
  return { res, status, json };
}

describe('extractErrorDetails', () => {
  it('extracts from errors.message', () => {
    const error = { response: { status: 422, data: { errors: { message: 'bad input' } } } };
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(422);
    expect(message).toBe('bad input');
  });

  it('extracts from error.message', () => {
    const error = { response: { status: 400, data: { error: { message: 'invalid param' } } } };
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(400);
    expect(message).toBe('invalid param');
  });

  it('extracts from top-level message', () => {
    const error = { response: { status: 429, data: { message: 'rate limited' } } };
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(429);
    expect(message).toBe('rate limited');
  });

  it('uses string response data directly', () => {
    const error = { response: { status: 503, data: 'Service Unavailable' } };
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(503);
    expect(message).toBe('Service Unavailable');
  });

  it('falls back to error.message when no response body', () => {
    const error = { message: 'network timeout' };
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(500);
    expect(message).toBe('network timeout');
  });
});

describe('sendOpenAIError', () => {
  it('sends OpenAI-shaped error with correct status and body', () => {
    const { res, status, json } = mockRes();
    sendOpenAIError(res, 404, 'not found');
    expect(status).toHaveBeenCalledWith(404);
    expect(json).toHaveBeenCalledWith({
      error: { message: 'not found', type: 'api_error', param: null, code: '404' },
    });
  });

  it('uses custom type when provided', () => {
    const { res, json } = mockRes();
    sendOpenAIError(res, 400, 'missing param', 'invalid_request_error');
    expect(json).toHaveBeenCalledWith(
      expect.objectContaining({ error: expect.objectContaining({ type: 'invalid_request_error' }) })
    );
  });
});

describe('sendAnthropicError', () => {
  it('sends Anthropic-shaped error with correct status and body', () => {
    const { res, status, json } = mockRes();
    sendAnthropicError(res, 401, 'unauthorized');
    expect(status).toHaveBeenCalledWith(401);
    expect(json).toHaveBeenCalledWith({
      type: 'error',
      error: { type: 'api_error', message: 'unauthorized' },
    });
  });
});

describe('convertPythonJsonToStandardJson', () => {
  it('converts single-quoted strings to double-quoted', () => {
    expect(convertPythonJsonToStandardJson("{'key': 'value'}")).toBe('{"key": "value"}');
  });

  it('leaves already double-quoted strings unchanged', () => {
    expect(convertPythonJsonToStandardJson('{"key": "value"}')).toBe('{"key": "value"}');
  });

  it('escapes embedded double quotes when converting single-quoted strings', () => {
    const input = "{'msg': 'say \"hi\"'}";
    const result = convertPythonJsonToStandardJson(input);
    expect(JSON.parse(result)).toEqual({ msg: 'say "hi"' });
  });

  it('handles nested objects', () => {
    const result = convertPythonJsonToStandardJson("{'a': {'b': 'c'}}");
    expect(JSON.parse(result)).toEqual({ a: { b: 'c' } });
  });
});

describe('extractTextContent', () => {
  it('returns empty string for null', () => {
    expect(extractTextContent(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(extractTextContent(undefined)).toBe('');
  });

  it('returns string as-is', () => {
    expect(extractTextContent('hello')).toBe('hello');
  });

  it('joins text items from array', () => {
    expect(extractTextContent([
      { type: 'text', text: 'hello ' },
      { type: 'text', text: 'world' },
    ])).toBe('hello world');
  });

  it('filters out non-text items from array', () => {
    expect(extractTextContent([
      { type: 'image' },
      { type: 'text', text: 'hello' },
    ])).toBe('hello');
  });

  it('returns empty string for empty array', () => {
    expect(extractTextContent([])).toBe('');
  });
});

describe('applyPromptCaching', () => {
  const cachePoint = { cachePoint: { type: 'default' } };

  it('appends cachePoint to last two user messages', () => {
    const messages = [
      { role: 'user',      content: [{ type: 'text', text: 'first' }] },
      { role: 'assistant', content: [{ type: 'text', text: 'reply' }] },
      { role: 'user',      content: [{ type: 'text', text: 'second' }] },
    ];
    const result = applyPromptCaching(messages);
    expect(result[0].content).toContainEqual(cachePoint);
    expect(result[2].content).toContainEqual(cachePoint);
    expect(result[1].content).not.toContainEqual(cachePoint);
  });

  it('appends cachePoint to the only user message when there is one', () => {
    const messages = [{ role: 'user', content: [{ type: 'text', text: 'hi' }] }];
    const result = applyPromptCaching(messages);
    expect(result[0].content).toContainEqual(cachePoint);
  });

  it('does not mutate the original messages array', () => {
    const messages = [{ role: 'user', content: [{ type: 'text', text: 'hi' }] }];
    applyPromptCaching(messages);
    expect(messages[0].content).toHaveLength(1);
  });

  it('leaves messages unchanged when there are no user messages', () => {
    const messages = [{ role: 'assistant', content: [{ type: 'text', text: 'hi' }] }];
    const result = applyPromptCaching(messages);
    expect(result[0].content).toHaveLength(1);
  });
});

describe('parseErrorMessage', () => {
  it('extracts errors.message', () => {
    expect(parseErrorMessage(JSON.stringify({ errors: { message: 'quota exceeded' } }))).toBe('quota exceeded');
  });

  it('extracts error.message', () => {
    expect(parseErrorMessage(JSON.stringify({ error: { message: 'bad request' } }))).toBe('bad request');
  });

  it('extracts top-level message', () => {
    expect(parseErrorMessage(JSON.stringify({ message: 'server error' }))).toBe('server error');
  });

  it('returns raw body for invalid JSON', () => {
    expect(parseErrorMessage('plain error text')).toBe('plain error text');
  });

  it('returns "Unknown error" for empty body', () => {
    expect(parseErrorMessage('')).toBe('Unknown error');
  });
});

describe('endStreamOnError', () => {
  it('sends 500 JSON when headers have not been sent', () => {
    const json = vi.fn();
    const status = vi.fn().mockReturnValue({ json });
    const res = { headersSent: false, status, write: vi.fn(), end: vi.fn() } as unknown as Response;
    endStreamOnError(res, new Error('network failed'));
    expect(status).toHaveBeenCalledWith(500);
    expect(json).toHaveBeenCalledWith(
      expect.objectContaining({
        error: expect.objectContaining({ message: 'network failed', type: 'api_error' }),
      }),
    );
  });

  it('writes DONE and ends when headers are already sent', () => {
    const write = vi.fn();
    const end = vi.fn();
    const res = { headersSent: true, status: vi.fn(), write, end } as unknown as Response;
    endStreamOnError(res, new Error('stream broke'));
    expect(write).toHaveBeenCalledWith('data: [DONE]\n\n');
    expect(end).toHaveBeenCalled();
  });
});

describe('extractErrorDetails — statusCode fallback', () => {
  it('uses error.statusCode when there is no response object', () => {
    const error = Object.assign(new Error('Response not found: xyz'), { statusCode: 404 });
    const { statusCode, message } = extractErrorDetails(error);
    expect(statusCode).toBe(404);
    expect(message).toBe('Response not found: xyz');
  });
});
