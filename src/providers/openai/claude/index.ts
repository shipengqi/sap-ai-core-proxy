import { Response } from 'express';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { OpenAIChatCompletionRequest } from '../../../types/openai';
import { ConverseOpenAIProvider } from './converse';
import { InvokeOpenAIProvider } from './invoke';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Entry point for the OpenAI-compatible surface (/v1/chat/completions) for Claude models.
 * Dispatches to ConverseOpenAIProvider (Claude 3.5+) or InvokeOpenAIProvider (Claude 3).
 */
export class ClaudeOpenAIProvider {
  private converseProvider: ConverseOpenAIProvider;
  private invokeProvider: InvokeOpenAIProvider;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.converseProvider = new ConverseOpenAIProvider(authManager, deploymentManager);
    this.invokeProvider = new InvokeOpenAIProvider(authManager, deploymentManager);
  }

  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    logger.info(`OpenAI Claude API: model=${req.model}, stream=${req.stream ?? false}`);

    if (catalogue.usesConverseApi(req.model)) {
      await this.converseProvider.handle(req, res);
    } else {
      await this.invokeProvider.handle(req, res);
    }
  }
}
