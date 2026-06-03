import { describe, it, expect, vi } from 'vitest';
import type { Response } from 'express';
import { ClaudeDispatcher } from '../src/providers/claude-dispatcher';

function makeHandler() {
  return vi.fn().mockResolvedValue(undefined);
}

function fakeRes() {
  return {} as Response;
}

describe('ClaudeDispatcher', () => {
  it('calls converseHandler when usesConverseApi returns true for the sapModelName', async () => {
    const converse = makeHandler();
    const invoke = makeHandler();
    const dispatcher = new ClaudeDispatcher(converse, invoke);
    const req = {};
    const res = fakeRes();

    // anthropic--claude-4.5-sonnet uses Converse API
    await dispatcher.dispatch('anthropic--claude-4.5-sonnet', req, res);

    expect(converse).toHaveBeenCalledOnce();
    expect(converse).toHaveBeenCalledWith(req, res);
    expect(invoke).not.toHaveBeenCalled();
  });

  it('calls invokeHandler when usesConverseApi returns false for the sapModelName', async () => {
    const converse = makeHandler();
    const invoke = makeHandler();
    const dispatcher = new ClaudeDispatcher(converse, invoke);
    const req = {};
    const res = fakeRes();

    // anthropic--claude-3-haiku uses Invoke API (usesConverseApi = false)
    await dispatcher.dispatch('anthropic--claude-3-haiku', req, res);

    expect(invoke).toHaveBeenCalledOnce();
    expect(invoke).toHaveBeenCalledWith(req, res);
    expect(converse).not.toHaveBeenCalled();
  });

  it('passes req and res through to the selected handler unchanged', async () => {
    const converse = makeHandler();
    const invoke = makeHandler();
    const dispatcher = new ClaudeDispatcher(converse, invoke);
    const req = { model: 'anthropic--claude-3.5-sonnet', stream: true };
    const res = { headersSent: false } as unknown as Response;

    await dispatcher.dispatch('anthropic--claude-3.5-sonnet', req, res);

    expect(converse).toHaveBeenCalledWith(req, res);
  });
});
