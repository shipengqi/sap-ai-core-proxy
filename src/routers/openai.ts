import { Router, Request, Response } from 'express';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { ClaudeOpenAIProvider } from '../providers/claude-openai';
import { GeminiProvider } from '../providers/gemini-openai';
import { OpenAIChatCompletionRequest } from '../types/openai';
import { OpenAIModel, OpenAIModelsResponse } from '../types/openai';
import { ModelProvider } from '../types/models';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

export type ChatCompletionHandler = (req: OpenAIChatCompletionRequest, res: Response) => Promise<void>;

export function buildProviderRegistry(
  anthropicOpenAIProvider: ClaudeOpenAIProvider,
  geminiProvider: GeminiProvider,
): Map<ModelProvider, ChatCompletionHandler> {
  return new Map([
    ['anthropic', anthropicOpenAIProvider.handleChatCompletion.bind(anthropicOpenAIProvider)],
    ['gemini', geminiProvider.handleChatCompletion.bind(geminiProvider)],
  ]);
}

/**
 * Creates handler for GET /v1/models - List available models
 */
function handleListModels(deploymentManager: DeploymentManager) {
  return async (_req: Request, res: Response): Promise<void> => {
    try {
      const deployments = await deploymentManager.getDeployments();

      const models: OpenAIModel[] = deployments.map(d => ({
        id: d.details.resources.backend_details.model.name,
        object: 'model' as const,
        created: new Date(d.createdAt).getTime() / 1000,
        owned_by: catalogue.getOwner(d.details.resources.backend_details.model.name),
      }));

      const response: OpenAIModelsResponse = {
        object: 'list',
        data: models,
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
  };
}

/**
 * Creates handler for GET /v1/models/:modelId - Get specific model
 */
function handleGetModel(deploymentManager: DeploymentManager) {
  return async (req: Request, res: Response): Promise<void> => {
    try {
      const { modelId } = req.params;
      const deployment = await deploymentManager.findDeploymentForModel(modelId);

      if (!deployment) {
        res.status(404).json({
          error: {
            message: `Model ${modelId} not found`,
            type: 'invalid_request_error',
            param: 'model',
            code: 'model_not_found',
          },
        });
        return;
      }

      const model: OpenAIModel = {
        id: deployment.details.resources.backend_details.model.name,
        object: 'model',
        created: new Date(deployment.createdAt).getTime() / 1000,
        owned_by: catalogue.getOwner(deployment.details.resources.backend_details.model.name),
      };

      res.json(model);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to get model:', err.message);
      res.status(500).json({
        error: {
          message: err.message || 'Failed to get model',
          type: 'api_error',
          param: null,
          code: '500',
        },
      });
    }
  };
}

/**
 * Creates handler for POST /v1/chat/completions - Chat completion dispatch
 */
function handleChatCompletions(
  providerRegistry: Map<ModelProvider, ChatCompletionHandler>,
  defaultHandler: ChatCompletionHandler,
) {
  return async (req: Request, res: Response): Promise<void> => {
    const chatRequest = req.body as OpenAIChatCompletionRequest;

    // Validate request
    if (!chatRequest.model) {
      res.status(400).json({
        error: {
          message: 'Missing required parameter: model',
          type: 'invalid_request_error',
          param: 'model',
          code: 'missing_parameter',
        },
      });
      return;
    }

    if (!chatRequest.messages || !Array.isArray(chatRequest.messages) || chatRequest.messages.length === 0) {
      res.status(400).json({
        error: {
          message: 'Missing required parameter: messages',
          type: 'invalid_request_error',
          param: 'messages',
          code: 'missing_parameter',
        },
      });
      return;
    }

    try {
      const provider = catalogue.getProvider(chatRequest.model);
      logger.info(`Processing chat completion for model: ${chatRequest.model} (provider: ${provider})`);
      const handler = providerRegistry.get(provider) ?? defaultHandler;
      await handler(chatRequest, res);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Chat completion failed:', err.message);

      if (!res.headersSent) {
        res.status(500).json({
          error: {
            message: err.message || 'Chat completion failed',
            type: 'api_error',
            param: null,
            code: '500',
          },
        });
      }
    }
  };
}

/**
 * Creates a router for OpenAI-compatible proxy endpoints.
 * Mounted at /openai
 */
export function createOpenAICompatibleRouter(
  deploymentManager: DeploymentManager,
  providerRegistry: Map<ModelProvider, ChatCompletionHandler>,
  defaultHandler: ChatCompletionHandler,
): Router {
  const router = Router();

  // Model endpoints
  router.get('/v1/models', handleListModels(deploymentManager));
  router.get('/v1/models/:modelId', handleGetModel(deploymentManager));
  router.get('/models', handleListModels(deploymentManager));

  // Chat completions
  router.post('/v1/chat/completions', handleChatCompletions(providerRegistry, defaultHandler));
  router.post('/chat/completions', handleChatCompletions(providerRegistry, defaultHandler));

  return router;
}
