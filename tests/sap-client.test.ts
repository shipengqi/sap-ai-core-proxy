import { describe, it, expect, vi, beforeEach } from 'vitest';
import axios from 'axios';
import type { Request, Response } from 'express';
import { SapClient } from '../src/sap-ai-core/client';
import { DeploymentManager } from '../src/sap-ai-core/deployments';
import type { AuthManager } from '../src/sap-ai-core/auth';
import { ResponsesProvider } from '../src/providers/openai/native/responses';
import { DEPLOYMENTS_RESPONSE, DEPLOY_CLAUDE_ID } from './fixtures/deployments';

const BASE_URL = 'https://api.example.com';
const MOCK_HEADERS = {
  Authorization: 'Bearer test-token',
  'AI-Resource-Group': 'default',
  'Content-Type': 'application/json',
};

function fakeAuth(): AuthManager {
  return {
    buildHeaders: vi.fn().mockResolvedValue(MOCK_HEADERS),
    getBaseUrl: vi.fn().mockReturnValue(BASE_URL),
  } as unknown as AuthManager;
}

describe('SapClient', () => {
  let auth: AuthManager;
  let client: SapClient;

  beforeEach(() => {
    auth = fakeAuth();
    client = new SapClient(auth);
    vi.restoreAllMocks();
  });

  it('post() uses 30s inference timeout', async () => {
    const spy = vi.spyOn(axios, 'post').mockResolvedValue({ status: 200, data: {} });
    await client.post('/test', { foo: 'bar' });
    expect(spy).toHaveBeenCalledWith(
      `${BASE_URL}/test`,
      { foo: 'bar' },
      expect.objectContaining({ timeout: 30_000 }),
    );
  });

  it('get() uses 10s API timeout', async () => {
    const spy = vi.spyOn(axios, 'get').mockResolvedValue({ status: 200, data: {} });
    await client.get('/test');
    expect(spy).toHaveBeenCalledWith(
      `${BASE_URL}/test`,
      expect.objectContaining({ timeout: 10_000 }),
    );
  });

  it('delete() uses 10s API timeout', async () => {
    const spy = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 200, data: {} });
    await client.delete('/test');
    expect(spy).toHaveBeenCalledWith(
      `${BASE_URL}/test`,
      expect.objectContaining({ timeout: 10_000 }),
    );
  });

  it('postStream() sets stream responseType, 30s timeout, and permissive validateStatus', async () => {
    const spy = vi.spyOn(axios, 'post').mockResolvedValue({ status: 200, data: {} });
    await client.postStream('/test', {});
    const config = spy.mock.calls[0][2];
    expect(config).toMatchObject({ responseType: 'stream', timeout: 30_000 });
    expect(config?.validateStatus?.(429)).toBe(true);
    expect(config?.validateStatus?.(500)).toBe(false);
  });

  it('postStream() resolves instead of throwing on 4xx', async () => {
    vi.spyOn(axios, 'post').mockResolvedValue({ status: 429, data: 'rate limited' });
    const result = await client.postStream('/test', {});
    expect(result.status).toBe(429);
  });

  it('postForm() strips Content-Type so axios sets the multipart boundary', async () => {
    const spy = vi.spyOn(axios, 'post').mockResolvedValue({ status: 200, data: {} });
    await client.postForm('/test', new FormData());
    const config = spy.mock.calls[0][2];
    expect(config?.headers).not.toHaveProperty('Content-Type');
    expect(config?.headers).toHaveProperty('Authorization');
  });

  it('injects auth headers and prepends base URL on every call', async () => {
    const spy = vi.spyOn(axios, 'post').mockResolvedValue({ status: 200, data: {} });
    await client.post('/the/path', {});
    expect(spy).toHaveBeenCalledWith(
      `${BASE_URL}/the/path`,
      expect.anything(),
      expect.objectContaining({ headers: MOCK_HEADERS }),
    );
    expect(auth.buildHeaders).toHaveBeenCalledTimes(1);
    expect(auth.getBaseUrl).toHaveBeenCalledTimes(1);
  });
});

describe('DeploymentManager', () => {
  let auth: AuthManager;
  let manager: DeploymentManager;

  beforeEach(() => {
    auth = fakeAuth();
    manager = new DeploymentManager(auth);
    vi.restoreAllMocks();
  });

  it('getDeploymentId returns correct ID for known model', async () => {
    vi.spyOn(SapClient.prototype, 'get').mockResolvedValue({
      status: 200,
      data: DEPLOYMENTS_RESPONSE,
    } as any);
    const id = await manager.getDeploymentId('anthropic--claude-4.5-sonnet');
    expect(id).toBe(DEPLOY_CLAUDE_ID);
  });

  it('uses cached deployments on second call without re-fetching', async () => {
    const getSpy = vi.spyOn(SapClient.prototype, 'get').mockResolvedValue({
      status: 200,
      data: DEPLOYMENTS_RESPONSE,
    } as any);
    await manager.getDeploymentId('gpt-4o');
    await manager.getDeploymentId('gemini-2.5-flash');
    expect(getSpy).toHaveBeenCalledTimes(1);
  });

  it('throws for an unknown model', async () => {
    vi.spyOn(SapClient.prototype, 'get').mockResolvedValue({
      status: 200,
      data: DEPLOYMENTS_RESPONSE,
    } as any);
    await expect(manager.getDeploymentId('unknown-model')).rejects.toThrow(
      'No running deployment found for model: unknown-model',
    );
  });
});

describe('ResponsesProvider cache', () => {
  it('evicts oldest entry when cache exceeds 10,000 entries', async () => {
    const auth = fakeAuth();
    const deploymentManager = {
      getDeploymentId: vi.fn().mockResolvedValue('deploy-001'),
    } as unknown as DeploymentManager;
    const provider = new ResponsesProvider(auth, deploymentManager);

    let counter = 0;
    vi.spyOn(SapClient.prototype, 'post').mockImplementation(() =>
      Promise.resolve({ status: 200, data: { id: `resp-${++counter}` } } as any),
    );

    const makeReq = () => ({ body: { model: 'gpt-4o', input: 'x' } }) as unknown as Request;
    const makeRes = () =>
      ({
        json: vi.fn(),
        status: vi.fn().mockReturnValue({ json: vi.fn() }),
        headersSent: false,
      }) as unknown as Response;

    for (let i = 0; i < 10_001; i++) {
      await provider.handleCreate(makeReq(), makeRes());
    }

    // resp-1 was the oldest entry — should be evicted (404)
    const evictedRes = {
      json: vi.fn(),
      status: vi.fn().mockReturnValue({ json: vi.fn() }),
      headersSent: false,
    } as unknown as Response;
    await provider.handleGet('resp-1', {} as Request, evictedRes);
    expect(evictedRes.status).toHaveBeenCalledWith(404);

    // resp-10001 is the most recent — should still be cached (200)
    vi.spyOn(SapClient.prototype, 'get').mockResolvedValue({
      status: 200,
      data: { id: 'resp-10001' },
    } as any);
    const cachedRes = {
      json: vi.fn(),
      status: vi.fn().mockReturnValue({ json: vi.fn() }),
      headersSent: false,
    } as unknown as Response;
    await provider.handleGet('resp-10001', {} as Request, cachedRes);
    expect(cachedRes.json).toHaveBeenCalledWith({ id: 'resp-10001' });
  }, 10_000);
});
