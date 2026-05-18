import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import axios from 'axios';
import MockAdapter from 'axios-mock-adapter';
import { AuthManager } from '../src/sap-ai-core/auth';

const TOKEN_URL = 'https://test.auth.example.com';

const CREDENTIALS = {
  clientId: 'test-id',
  clientSecret: 'test-secret',
  tokenUrl: TOKEN_URL,
  baseUrl: 'https://api.example.com',
  resourceGroup: 'default',
};

function tokenReply(token = 'test-token') {
  return { access_token: token, token_type: 'Bearer', expires_in: 3600 };
}

describe('AuthManager', () => {
  let mock: MockAdapter;
  let auth: AuthManager;

  beforeEach(() => {
    mock = new MockAdapter(axios);
    auth = new AuthManager(CREDENTIALS);
  });

  afterEach(() => {
    mock.restore();
  });

  it('fetches a token and returns it', async () => {
    mock.onPost(`${TOKEN_URL}/oauth/token`).reply(200, tokenReply('fresh-token'));
    const token = await auth.getToken();
    expect(token).toBe('fresh-token');
    expect(mock.history.post).toHaveLength(1);
  });

  it('returns cached token without re-authenticating', async () => {
    mock.onPost(`${TOKEN_URL}/oauth/token`).reply(200, tokenReply('cached-token'));
    await auth.getToken();
    await auth.getToken();
    expect(mock.history.post).toHaveLength(1);
  });

  it('concurrent calls trigger only one authenticate()', async () => {
    mock.onPost(`${TOKEN_URL}/oauth/token`).reply(200, tokenReply('shared-token'));
    const [t1, t2] = await Promise.all([auth.getToken(), auth.getToken()]);
    expect(t1).toBe('shared-token');
    expect(t2).toBe('shared-token');
    expect(mock.history.post).toHaveLength(1);
  });
});
