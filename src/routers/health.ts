import { Router, Request, Response } from 'express';

/**
 * Creates a router for root-level health/info endpoints.
 */
export function createHealthRouter(): Router {
  const router = Router();

  router.get('/', (_req: Request, res: Response) => {
    res.json({
      name: 'SAP AI Core Proxy',
      version: '1.0.0',
      status: 'ok',
      endpoints: {
        health: '/health',
        openaiCompatible: {
          models: '/openai-compatible/v1/models',
          chat: '/openai-compatible/v1/chat/completions',
        },
        anthropic: {
          messages: '/anthropic/v1/messages',
          countTokens: '/anthropic/v1/messages/count_tokens',
        },
        admin: '/admin/refresh-deployments',
      },
    });
  });

  router.get('/health', (_req: Request, res: Response) => {
    res.json({ status: 'ok', timestamp: new Date().toISOString() });
  });

  return router;
}
