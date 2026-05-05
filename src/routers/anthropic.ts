import { Router, Request, Response } from 'express';
import { ClaudeAnthropicProvider } from '../providers/claude-anthropic';

/**
 * Creates a router for Anthropic native API proxy endpoints.
 * Mounted at /anthropic
 */
export function createAnthropicRouter(
  anthropicNativeProvider: ClaudeAnthropicProvider,
): Router {
  const router = Router();

  // Health-check / connectivity probe (Claude Code sends HEAD /anthropic)
  router.head('/', (_req: Request, res: Response) => {
    res.status(200).end();
  });
  router.get('/', (_req: Request, res: Response) => {
    res.json({ provider: 'anthropic', status: 'ok' });
  });

  // Anthropic Messages API
  router.post('/v1/messages', anthropicNativeProvider.handleMessages.bind(anthropicNativeProvider));
  router.post('/v1/messages/count_tokens', anthropicNativeProvider.handleCountTokens.bind(anthropicNativeProvider));

  return router;
}
