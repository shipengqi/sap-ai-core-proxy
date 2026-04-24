import { Router, Request, Response } from 'express';
import { AnthropicNativeProvider, ANTHROPIC_TO_SAP_MODEL_MAP } from '../providers/anthropic-native';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { logger } from '../logger';

/**
 * Builds a reverse map: SAP AI Core model name -> Anthropic standard name.
 * When multiple Anthropic names map to the same SAP name (e.g. aliases like
 * "claude-3-5-sonnet-latest" and "claude-3-5-sonnet-20241022" both map to
 * "anthropic--claude-3.5-sonnet"), we prefer the versioned/canonical name
 * (the one that does NOT end with "-latest").
 */
function buildSapToAnthropicMap(): Record<string, string> {
  const reverse: Record<string, string> = {};
  for (const [anthropicName, sapName] of Object.entries(ANTHROPIC_TO_SAP_MODEL_MAP)) {
    if (!reverse[sapName] || anthropicName.endsWith('-latest')) {
      // Prefer the first (non-latest) entry; only overwrite with a -latest alias
      // if nothing is set yet.
      if (!reverse[sapName]) {
        reverse[sapName] = anthropicName;
      }
    }
  }
  return reverse;
}

/**
 * Registers Claude Code auth stub endpoints on the given router.
 *
 * When ANTHROPIC_BASE_URL points to this proxy, Claude Code routes ALL Anthropic
 * API calls here - including OAuth / account endpoints. These stubs return
 * minimal plausible responses so the extension considers itself "logged in"
 * and proceeds to make LLM requests without needing a real Anthropic account.
 */
function setupClaudeCodeAuthRoutes(router: Router): void {
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

  const userInfoHandler = (_req: Request, res: Response): void => {
    res.json(fakeUser);
  };
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
    logger.debug(`Claude Code auth stub: ${req.method} ${req.path}`);
    res.json({ ok: true });
  });

  logger.info('Claude Code auth stub routes registered');
}

/**
 * Creates a router for Anthropic native API proxy endpoints.
 * Mounted at /anthropic
 */
export function createAnthropicRouter(
  anthropicNativeProvider: AnthropicNativeProvider,
  deploymentManager: DeploymentManager,
): Router {
  const router = Router();
  const sapToAnthropic = buildSapToAnthropicMap();

  // Health-check / connectivity probe (Claude Code sends HEAD /anthropic)
  router.head('/', (_req: Request, res: Response) => {
    res.status(200).end();
  });
  router.get('/', (_req: Request, res: Response) => {
    res.json({ provider: 'anthropic', status: 'ok' });
  });

  // Models list - dynamically built from running SAP AI Core deployments
  router.get('/v1/models', async (_req: Request, res: Response) => {
    try {
      const deployments = await deploymentManager.getDeployments();

      const data = deployments
        .filter(d => deploymentManager.getModelProvider(d.details.resources.backend_details.model.name) === 'anthropic')
        .map(d => {
          const sapName = d.details.resources.backend_details.model.name;
          const anthropicName = sapToAnthropic[sapName] ?? sapName;
          return {
            type: 'model',
            id: anthropicName,
            display_name: anthropicName,
            created_at: new Date(d.createdAt).toISOString(),
          };
        });

      res.json({ object: 'list', data });
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to list Anthropic models:', err.message);
      res.status(500).json({
        type: 'error',
        error: { type: 'api_error', message: err.message || 'Failed to list models' },
      });
    }
  });

  router.get('/v1/models/:modelId', async (req: Request, res: Response) => {
    try {
      const { modelId } = req.params;
      // modelId may be an Anthropic name; map it to SAP name for lookup
      const sapName = ANTHROPIC_TO_SAP_MODEL_MAP[modelId] ?? modelId;
      const deployment = await deploymentManager.findDeploymentForModel(sapName);

      if (!deployment) {
        res.status(404).json({
          type: 'error',
          error: { type: 'not_found_error', message: `Model \`${modelId}\` not found` },
        });
        return;
      }

      res.json({
        type: 'model',
        id: modelId,
        display_name: modelId,
        created_at: new Date(deployment.createdAt).toISOString(),
      });
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to get Anthropic model:', err.message);
      res.status(500).json({
        type: 'error',
        error: { type: 'api_error', message: err.message || 'Failed to get model' },
      });
    }
  });

  // Anthropic Messages API
  router.post('/v1/messages', anthropicNativeProvider.handleMessages.bind(anthropicNativeProvider));
  router.post('/v1/messages/count_tokens', anthropicNativeProvider.handleCountTokens.bind(anthropicNativeProvider));

  // Claude Code auth stub endpoints
  setupClaudeCodeAuthRoutes(router);

  return router;
}
