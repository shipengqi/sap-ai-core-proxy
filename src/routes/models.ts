import { Request, Response } from 'express';
import { DeploymentManager } from '../sap-ai-core/deployments';
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
 * Handles GET /v1/models - List available models
 */
export function handleListModels(deploymentManager: DeploymentManager) {
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
 * Handles GET /v1/models/:modelId - Get specific model
 */
export function handleGetModel(deploymentManager: DeploymentManager) {
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
