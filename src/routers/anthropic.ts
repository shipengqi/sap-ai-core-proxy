import { Router, Request, Response } from 'express';
import { ClaudeAnthropicProvider } from '../providers/claude-anthropic';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { AnthropicModel, AnthropicModelsResponse } from '../types/anthropic';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

/**
 * Creates a router for Anthropic native API proxy endpoints.
 * Mounted at /anthropic
 */
export function createAnthropicRouter(
  anthropicNativeProvider: ClaudeAnthropicProvider,
  deploymentManager: DeploymentManager,
): Router {
  const router = Router();

  // Health-check / connectivity probe (Claude Code sends HEAD /anthropic)
  router.head('/', (_req: Request, res: Response) => {
    res.status(200).end();
  });
  router.get('/', (_req: Request, res: Response) => {
    res.json({ provider: 'anthropic', status: 'ok' });
  });

  // Anthropic Models API
  router.get('/v1/models', async (_req: Request, res: Response): Promise<void> => {
    try {
      const models: AnthropicModel[] = (await deploymentManager.getDeploymentModels())
        .filter(m => {
          const entry = catalogue.tryGetEntry(m.sapName);
          return entry?.provider === 'anthropic';
        })
        .map(m => {
          const entry = catalogue.tryGetEntry(m.sapName);
          const id = entry?.anthropicAliases?.[0] ?? m.sapName;
          return {
            type: 'model' as const,
            id,
            display_name: toDisplayName(m.sapName),
            created_at: new Date(m.createdAt).toISOString(),
          };
        });

      const response: AnthropicModelsResponse = {
        data: models,
        has_more: false,
        first_id: models[0]?.id ?? null,
        last_id: models[models.length - 1]?.id ?? null,
      };

      res.json(response);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to list models:', err.message);
      res.status(500).json({
        error: {
          message: err.message || 'Failed to list models',
          type: 'api_error',
          param: null,
          code: '500',
        },
      });
    }
  });

  // Anthropic Messages API
  router.post('/v1/messages', anthropicNativeProvider.handleMessages.bind(anthropicNativeProvider));
  router.post('/v1/messages/count_tokens', anthropicNativeProvider.handleCountTokens.bind(anthropicNativeProvider));

  return router;
}

function toDisplayName(sapName: string): string {
  return sapName
    .replace('anthropic--claude-', 'Claude ')
    .replace(/-/g, ' ')
    .replace(/\b\w/g, c => c.toUpperCase());
}
