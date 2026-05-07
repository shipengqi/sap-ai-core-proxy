import { Router, Request, Response } from 'express';
import multer from 'multer';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { ClaudeOpenAIProvider, GeminiProvider, EmbeddingsProvider, ResponsesProvider, AudioProvider, MulterRequest } from '../providers';
import { OpenAIChatCompletionRequest } from '../types/openai';
import { OpenAIModel, OpenAIModelsResponse } from '../types/openai';
import { ModelProvider } from '../types/models';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

const upload = multer({ storage: multer.memoryStorage() });

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
      const models: OpenAIModel[] = (await deploymentManager.getDeploymentModels()).map(m => ({
        id: m.sapName,
        object: 'model' as const,
        created: new Date(m.createdAt).getTime() / 1000,
        owned_by: catalogue.getOwner(m.sapName),
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
      const modelId = req.params['modelId'] as string;
      const model = await deploymentManager.findModelDeployment(modelId);

      if (!model) {
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

      const openAIModel: OpenAIModel = {
        id: model.sapName,
        object: 'model',
        created: new Date(model.createdAt).getTime() / 1000,
        owned_by: catalogue.getOwner(model.sapName),
      };

      res.json(openAIModel);
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
export function createOpenAICompatibleRouter(opts: {
  deploymentManager: DeploymentManager;
  providerRegistry: Map<ModelProvider, ChatCompletionHandler>;
  defaultHandler: ChatCompletionHandler;
  embeddingsProvider: EmbeddingsProvider;
  responsesProvider: ResponsesProvider;
  audioProvider: AudioProvider;
}): Router {
  const router = Router();

  // Model endpoints
  router.get('/v1/models', handleListModels(opts.deploymentManager));
  router.get('/v1/models/:modelId', handleGetModel(opts.deploymentManager));
  router.get('/models', handleListModels(opts.deploymentManager));

  // Chat completions
  router.post('/v1/chat/completions', handleChatCompletions(opts.providerRegistry, opts.defaultHandler));
  router.post('/chat/completions', handleChatCompletions(opts.providerRegistry, opts.defaultHandler));

  // Embeddings
  router.post('/v1/embeddings', (req: Request, res: Response) => opts.embeddingsProvider.handleEmbeddings(req, res));

  // Responses API
  router.post('/v1/responses', (req: Request, res: Response) => opts.responsesProvider.handleCreate(req, res));
  router.get('/v1/responses/:responseId', (req: Request, res: Response) =>
    opts.responsesProvider.handleGet(req.params['responseId'] as string, req, res));
  router.delete('/v1/responses/:responseId', (req: Request, res: Response) =>
    opts.responsesProvider.handleDelete(req.params['responseId'] as string, req, res));

  // Audio transcription
  router.post('/v1/audio/transcriptions', upload.single('file'), (req: Request, res: Response) =>
    opts.audioProvider.handleTranscription(req as MulterRequest, res));

  return router;
}
