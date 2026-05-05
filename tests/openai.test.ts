import { describe, it, expect, beforeAll, beforeEach, afterAll } from 'vitest';
import request from 'supertest';
import { Readable } from 'stream';
import MockAdapter from 'axios-mock-adapter';
import { createTestApp, createMockAdapter, setupAuthMock } from './helpers/setup';
import { DEPLOYMENTS_RESPONSE, DEPLOY_OPENAI_ID, DEPLOY_CLAUDE_ID, DEPLOY_GEMINI_ID } from './fixtures/deployments';
import { SAP_OPENAI_RESPONSE, SAP_CONVERSE_RESPONSE, SAP_GEMINI_RESPONSE } from './fixtures/sap-responses';

describe('OpenAI Surface', () => {
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

  describe('GET /openai/v1/models', () => {
    it('returns list of available models', async () => {
      const res = await request(app).get('/openai/v1/models');

      expect(res.status).toBe(200);
      expect(res.body.object).toBe('list');
      expect(Array.isArray(res.body.data)).toBe(true);
      expect(res.body.data.length).toBe(3);

      const gpt4o = res.body.data.find((m: { id: string }) => m.id === 'gpt-4o');
      expect(gpt4o).toBeDefined();
      expect(gpt4o.object).toBe('model');
      expect(gpt4o.owned_by).toBe('openai');
    });
  });

  describe('POST /openai/v1/chat/completions', () => {
    it('routes gpt-4o to OpenAI provider', async () => {
      mock
        .onPost(new RegExp(`${DEPLOY_OPENAI_ID}/chat/completions`))
        .reply(200, SAP_OPENAI_RESPONSE);

      const res = await request(app)
        .post('/openai/v1/chat/completions')
        .send({ model: 'gpt-4o', messages: [{ role: 'user', content: 'Hello' }] });

      expect(res.status).toBe(200);
      expect(res.body.object).toBe('chat.completion');
      expect(res.body.model).toBe('gpt-4o');
      expect(res.body.choices[0].message.content).toBe('Hello from GPT-4o!');
      expect(res.body.usage.total_tokens).toBe(18);
    });

    it('routes anthropic--claude-4.5-sonnet to ClaudeOpenAI provider via converse', async () => {
      mock
        .onPost(new RegExp(`${DEPLOY_CLAUDE_ID}/converse$`))
        .reply(200, SAP_CONVERSE_RESPONSE);

      const res = await request(app)
        .post('/openai/v1/chat/completions')
        .send({ model: 'anthropic--claude-4.5-sonnet', messages: [{ role: 'user', content: 'Hello' }] });

      expect(res.status).toBe(200);
      expect(res.body.object).toBe('chat.completion');
      expect(res.body.model).toBe('anthropic--claude-4.5-sonnet');
      expect(res.body.choices[0].message.content).toBe('Hello from Claude!');
      expect(res.body.choices[0].finish_reason).toBe('stop');
    });

    it('routes gemini-2.5-flash to Gemini provider', async () => {
      mock
        .onPost(new RegExp(`${DEPLOY_GEMINI_ID}/models/gemini-2\\.5-flash:generateContent`))
        .reply(200, SAP_GEMINI_RESPONSE);

      const res = await request(app)
        .post('/openai/v1/chat/completions')
        .send({ model: 'gemini-2.5-flash', messages: [{ role: 'user', content: 'Hello' }] });

      expect(res.status).toBe(200);
      expect(res.body.object).toBe('chat.completion');
      expect(res.body.model).toBe('gemini-2.5-flash');
      expect(res.body.choices[0].message.content).toBe('Hello from Gemini!');
    });

    it('streams gpt-4o response as SSE', async () => {
      const stream = new Readable({ read() {} });

      mock
        .onPost(new RegExp(`${DEPLOY_OPENAI_ID}/chat/completions`))
        .reply(200, stream);

      process.nextTick(() => {
        stream.push('data: {"id":"c1","choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}]}\n\n');
        stream.push('data: {"id":"c1","choices":[{"delta":{"content":"Hello!"},"finish_reason":null,"index":0}]}\n\n');
        stream.push('data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\n');
        stream.push('data: [DONE]\n\n');
        stream.push(null);
      });

      const res = await request(app)
        .post('/openai/v1/chat/completions')
        .send({ model: 'gpt-4o', messages: [{ role: 'user', content: 'Hello' }], stream: true })
        .buffer(true)
        .parse((res, callback) => {
          let data = '';
          res.on('data', (chunk: Buffer) => { data += chunk.toString(); });
          res.on('end', () => callback(null, data));
        });

      expect(res.status).toBe(200);
      expect(res.body as string).toContain('data:');
      expect(res.body as string).toContain('Hello!');
      expect(res.body as string).toContain('[DONE]');
    });
  });
});
