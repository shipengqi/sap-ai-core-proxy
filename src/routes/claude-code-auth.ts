import { Request, Response, Application } from 'express';
import { logger } from '../logger';

/**
 * Sets up Claude Code auth stub endpoints.
 *
 * When ANTHROPIC_BASE_URL points to this proxy, Claude Code routes ALL Anthropic
 * API calls here - including OAuth / account endpoints. These stubs return
 * minimal plausible responses so the extension considers itself "logged in"
 * and proceeds to make LLM requests without needing a real Anthropic account.
 *
 * Companion: create ~/.claude/.credentials.json with a far-future expiresAt so
 * the SDK picks up an authToken and never tries to refresh it.
 */
export function setupClaudeCodeAuthRoutes(app: Application): void {
  // Fake user / account info
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

  // --- OAuth / session endpoints ---

  // Token introspection / user info (various paths Claude Code may call)
  const userInfoHandler = (_req: Request, res: Response): void => {
    res.json(fakeUser);
  };
  app.get('/api/auth/me', userInfoHandler);
  app.get('/api/user', userInfoHandler);
  app.get('/api/account', userInfoHandler);
  app.get('/api/auth/current_user', userInfoHandler);

  // OAuth token refresh – return a long-lived fake token
  app.post('/oauth/token', (_req: Request, res: Response) => {
    res.json({
      access_token: 'sk-ant-proxy-auth-bypass-token-for-sap-ai-core',
      refresh_token: 'sk-ant-proxy-refresh-bypass-token',
      token_type: 'Bearer',
      expires_in: 315360000, // 10 years
      scope: 'user:inference',
    });
  });

  // Organizations list
  app.get('/api/organizations', (_req: Request, res: Response) => {
    res.json({ organizations: [fakeOrg] });
  });

  // Usage / quota – return generous fake limits
  app.get('/api/quota', (_req: Request, res: Response) => {
    res.json({
      usage: { tokens_used: 0, requests_used: 0 },
      limits: { tokens: 1000000000, requests: 1000000 },
      reset_at: new Date(Date.now() + 86400000).toISOString(),
    });
  });

  // Feature flags / user flags
  app.get('/api/user_flags', (_req: Request, res: Response) => {
    res.json({
      flags: {
        has_pro_subscription: true,
        claude_ai_mcp_enabled: true,
        interleaved_thinking_enabled: true,
      },
    });
  });

  // Billing / subscription
  app.get('/api/billing/subscription', (_req: Request, res: Response) => {
    res.json({
      plan: 'max_tier',
      status: 'active',
      current_period_end: new Date(Date.now() + 315360000000).toISOString(),
    });
  });

  // Claude.ai MCP eligibility check
  app.get('/api/auth/claude_ai_oauth', (_req: Request, res: Response) => {
    res.json({ eligible: true, user: fakeUser });
  });

  // Catch-all for any other /api/* paths Claude Code might probe
  app.all('/api/*', (req: Request, res: Response) => {
    logger.debug(`Claude Code auth stub: ${req.method} ${req.path}`);
    res.json({ ok: true });
  });

  logger.info('Claude Code auth stub routes registered');
}
