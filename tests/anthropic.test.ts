import { describe, it, expect, beforeAll, beforeEach, afterAll } from 'vitest';
import request from 'supertest';
import MockAdapter from 'axios-mock-adapter';
import { createTestApp, createMockAdapter, setupAuthMock } from './helpers/setup';
import { DEPLOYMENTS_RESPONSE, DEPLOY_CLAUDE_ID } from './fixtures/deployments';
import { SAP_CONVERSE_RESPONSE } from './fixtures/sap-responses';

describe('Anthropic Surface', () => {
  let app: ReturnType<typeof createTestApp>;
  let mock: MockAdapter;

  beforeAll(() => {
    app = createTestApp();
    mock = createMockAdapter();
  });

  beforeEach(() => {
    mock.reset();
    setupAuthMock(mock);
    mock.onGet(/\/v2\/lm\/deployments/).reply(200, DEPLOYMENTS_RESPONSE);
  });

  afterAll(() => {
    mock.restore();
  });

  describe('POST /anthropic/v1/messages', () => {
    it('returns Anthropic Messages format for Anthropic SDK model name', async () => {
      mock
        .onPost(new RegExp(`${DEPLOY_CLAUDE_ID}/converse$`))
        .reply(200, SAP_CONVERSE_RESPONSE);

      const res = await request(app)
        .post('/anthropic/v1/messages')
        .set('x-api-key', 'any-value')
        .set('anthropic-version', '2023-06-01')
        .send({
          model: 'claude-sonnet-4-5',
          max_tokens: 1024,
          messages: [{ role: 'user', content: 'Hello' }],
        });

      expect(res.status).toBe(200);
      expect(res.body.type).toBe('message');
      expect(res.body.role).toBe('assistant');
      expect(res.body.model).toBe('claude-sonnet-4-5');
      expect(res.body.content[0].type).toBe('text');
      expect(res.body.content[0].text).toBe('Hello from Claude!');
      expect(res.body.stop_reason).toBe('end_turn');
      expect(res.body.usage.input_tokens).toBe(10);
      expect(res.body.usage.output_tokens).toBe(6);
    });

    it('accepts SAP model name as pass-through', async () => {
      mock
        .onPost(new RegExp(`${DEPLOY_CLAUDE_ID}/converse$`))
        .reply(200, SAP_CONVERSE_RESPONSE);

      const res = await request(app)
        .post('/anthropic/v1/messages')
        .set('x-api-key', 'any-value')
        .send({
          model: 'anthropic--claude-4.5-sonnet',
          max_tokens: 1024,
          messages: [{ role: 'user', content: 'Hello' }],
        });

      expect(res.status).toBe(200);
      expect(res.body.type).toBe('message');
    });
  });

  describe('POST /anthropic/v1/messages/count_tokens', () => {
    it('returns token count estimate without calling SAP AI Core', async () => {
      const res = await request(app)
        .post('/anthropic/v1/messages/count_tokens')
        .set('x-api-key', 'any-value')
        .send({
          model: 'claude-sonnet-4-5',
          messages: [{ role: 'user', content: 'Hello, how are you today?' }],
        });

      expect(res.status).toBe(200);
      expect(typeof res.body.input_tokens).toBe('number');
      expect(res.body.input_tokens).toBeGreaterThan(0);
      // "Hello, how are you today?" = 25 chars ≈ 7 tokens
      expect(res.body.input_tokens).toBeLessThan(20);
    });
  });

  describe('validation', () => {
    it('returns 400 when model is missing', async () => {
      const res = await request(app)
        .post('/anthropic/v1/messages')
        .send({ messages: [{ role: 'user', content: 'Hello' }], max_tokens: 1024 });

      expect(res.status).toBe(400);
      expect(res.body.type).toBe('error');
      expect(res.body.error.type).toBe('invalid_request_error');
    });

    it('returns 400 when messages array is empty', async () => {
      const res = await request(app)
        .post('/anthropic/v1/messages')
        .send({ model: 'claude-sonnet-4-5', messages: [], max_tokens: 1024 });

      expect(res.status).toBe(400);
      expect(res.body.type).toBe('error');
    });
  });

  describe('Claude Code compat stubs', () => {
    it('POST /anthropic/oauth/token returns access_token', async () => {
      const res = await request(app).post('/anthropic/oauth/token').send({});
      expect(res.status).toBe(200);
      expect(typeof res.body.access_token).toBe('string');
      expect(res.body.token_type).toBe('Bearer');
    });

    it('GET /anthropic/api/auth/me returns user info with api access', async () => {
      const res = await request(app).get('/anthropic/api/auth/me');
      expect(res.status).toBe(200);
      expect(res.body.has_api_access).toBe(true);
      expect(typeof res.body.id).toBe('string');
    });

    it('GET /anthropic/api/organizations returns organizations array', async () => {
      const res = await request(app).get('/anthropic/api/organizations');
      expect(res.status).toBe(200);
      expect(Array.isArray(res.body.organizations)).toBe(true);
      expect(res.body.organizations.length).toBeGreaterThan(0);
    });

    it('GET /anthropic/api/quota returns usage and limits', async () => {
      const res = await request(app).get('/anthropic/api/quota');
      expect(res.status).toBe(200);
      expect(res.body.usage).toBeDefined();
      expect(res.body.limits).toBeDefined();
    });

    it('GET /anthropic/api/unknown-endpoint returns { ok: true } via catch-all', async () => {
      const res = await request(app).get('/anthropic/api/completely-unknown-path');
      expect(res.status).toBe(200);
      expect(res.body.ok).toBe(true);
    });
  });
});
