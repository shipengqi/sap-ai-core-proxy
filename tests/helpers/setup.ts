import axios from 'axios';
import MockAdapter from 'axios-mock-adapter';
import { createApp } from '../../src/app';
import type { AppConfig } from '../../src/config';

export const BASE_URL = 'https://api.ai.test.example.com';
export const TOKEN_URL = 'https://test.auth.example.com';
export const MOCK_TOKEN = 'test-bearer-token-12345';

export const TEST_CONFIG: AppConfig = {
  port: 3001,
  credentials: {
    clientId: 'test-client-id',
    clientSecret: 'test-client-secret',
    tokenUrl: TOKEN_URL,
    baseUrl: BASE_URL,
    resourceGroup: 'default',
  },
};

export function createTestApp() {
  const { app } = createApp(TEST_CONFIG);
  return app;
}

export function createMockAdapter(): MockAdapter {
  return new MockAdapter(axios);
}

export function setupAuthMock(mock: MockAdapter): void {
  mock.onPost(`${TOKEN_URL}/oauth/token`).reply(200, {
    access_token: MOCK_TOKEN,
    token_type: 'Bearer',
    expires_in: 3600,
  });
}
