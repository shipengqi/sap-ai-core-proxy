import { Router, Request, Response } from 'express';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { OpenAIProvider } from '../providers/openai';
import { AnthropicOpenAIProvider } from '../providers/anthropic-openai';
import { GeminiProvider } from '../providers/gemini-openai';
import { OpenAIChatCompletionRequest } from '../types/openai';
import { OpenAIModel, OpenAIModelsResponse } from '../types/openai';
import { logger } from '../logger';

/**
 * Gets the owner/provider for a model
 */
function getModelOwner(modelName: string): string {
  const lowerName = modelName.toLowerCase();
  if (lowerName.includes('gpt') || lowerName.includes('o1') || lowerName.includes('o3')) {
    return 'openai';
  }
  if (lowerName.includes('claude') || lowerName.includes('anthropic')) {
    return 'anthropic';
  }
  if (lowerName.includes('gemini')) {
    return 'google';
  }
  if (lowerName.includes('llama') || lowerName.includes('meta')) {
    return 'meta';
  }
  if (lowerName.includes('mistral') || lowerName.includes('mixtral')) {
    return 'mistral';
  }
  return 'sap-ai-core';
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
        owned_by: getModelOwner(d.details.resources.backend_details.model.name),
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
        owned_by: getModelOwner(deployment.details.resources.backend_details.model.name),
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
  deploymentManager: DeploymentManager,
  openaiProvider: OpenAIProvider,
  anthropicOpenAIProvider: AnthropicOpenAIProvider,
  geminiProvider: GeminiProvider,
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
      // Determine which handler to use based on model provider
      const provider = deploymentManager.getModelProvider(chatRequest.model);

      logger.info(`Processing chat completion for model: ${chatRequest.model} (provider: ${provider})`);

      switch (provider) {
        case 'anthropic':
          await anthropicOpenAIProvider.handleChatCompletion(chatRequest, res);
          break;
        case 'gemini':
          await geminiProvider.handleChatCompletion(chatRequest, res);
          break;
        case 'openai':
        case 'meta':
        case 'mistral':
        default:
          await openaiProvider.handleChatCompletion(chatRequest, res);
          break;
      }
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
 * Mounted at /openai-compatible
 */
export function createOpenAICompatibleRouter(
  deploymentManager: DeploymentManager,
  openaiProvider: OpenAIProvider,
  anthropicOpenAIProvider: AnthropicOpenAIProvider,
  geminiProvider: GeminiProvider,
): Router {
  const router = Router();

  // Model endpoints
  router.get('/v1/models', handleListModels(deploymentManager));
  router.get('/v1/models/:modelId', handleGetModel(deploymentManager));
  router.get('/models', handleListModels(deploymentManager));

  // Chat completions
  router.post('/v1/chat/completions', handleChatCompletions(deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider));
  router.post('/chat/completions', handleChatCompletions(deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider));

  return router;
}
