import { Request, Response } from 'express';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { OpenAIProvider } from '../providers/openai';
import { AnthropicOpenAIProvider } from '../providers/anthropic-openai';
import { GeminiProvider } from '../providers/gemini';
import { OpenAIChatCompletionRequest } from '../types/openai';
import { logger } from '../logger';

/**
 * Handles POST /v1/chat/completions - Chat completion dispatch
 */
export function handleChatCompletions(
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
          // Use OpenAI-compatible handler for most models
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
