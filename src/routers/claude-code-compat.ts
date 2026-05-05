import { Router, Request, Response } from 'express';
import { logger } from '../logger';

const fakeUser = {
  id: 'user_proxy_sap_ai_core',
  email: 'proxy@sap-ai-core.local',
  name: 'SAP AI Core Proxy User',
  display_name: 'SAP AI Core Proxy',
  has_claude_pro: true,
  has_pro_subscription: true,
  has_api_access: true,
};

const fakeOrg = {
  id: 'org_proxy_sap_ai_core',
  name: 'SAP AI Core',
  billing_type: 'api_error_counts',
  rate_limit_tier: 'production',
};

/**
 * Compatibility shim for the Claude Code CLI and VSCode extension.
 *
 * When ANTHROPIC_BASE_URL points to this proxy, Claude Code routes ALL Anthropic
 * API calls here — including OAuth and account endpoints it expects from
 * claude.ai. These stubs return minimal plausible responses so the extension
 * considers itself "logged in" and proceeds to make LLM requests without
 * requiring a real Anthropic account.
 *
 * Mount at the same base path as the Anthropic messages router (/anthropic).
 */
export function createClaudeCodeCompatRouter(): Router {
  const router = Router();

  const userInfoHandler = (_req: Request, res: Response): void => { res.json(fakeUser); };
  router.get('/api/auth/me', userInfoHandler);
  router.get('/api/user', userInfoHandler);
  router.get('/api/account', userInfoHandler);
  router.get('/api/auth/current_user', userInfoHandler);

  router.post('/oauth/token', (_req: Request, res: Response) => {
    res.json({
      access_token: 'sk-ant-proxy-auth-bypass-token-for-sap-ai-core',
      refresh_token: 'sk-ant-proxy-refresh-bypass-token',
      token_type: 'Bearer',
      expires_in: 315360000,
      scope: 'user:inference',
    });
  });

  router.get('/api/organizations', (_req: Request, res: Response) => {
    res.json({ organizations: [fakeOrg] });
  });

  router.get('/api/quota', (_req: Request, res: Response) => {
    res.json({
      usage: { tokens_used: 0, requests_used: 0 },
      limits: { tokens: 1000000000, requests: 1000000 },
      reset_at: new Date(Date.now() + 86400000).toISOString(),
    });
  });

  router.get('/api/user_flags', (_req: Request, res: Response) => {
    res.json({
      flags: {
        has_pro_subscription: true,
        claude_ai_mcp_enabled: true,
        interleaved_thinking_enabled: true,
      },
    });
  });

  router.get('/api/billing/subscription', (_req: Request, res: Response) => {
    res.json({
      plan: 'max_tier',
      status: 'active',
      current_period_end: new Date(Date.now() + 315360000000).toISOString(),
    });
  });

  router.get('/api/auth/claude_ai_oauth', (_req: Request, res: Response) => {
    res.json({ eligible: true, user: fakeUser });
  });

  router.all('/api/*', (req: Request, res: Response) => {
    logger.debug(`Claude Code compat stub: ${req.method} ${req.path}`);
    res.json({ ok: true });
  });

  return router;
}
