import { Request, Response } from 'express';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import {
  AnthropicMessagesRequest,
  AnthropicCountTokensResponse,
} from '../../../types/anthropic';
import { extractSystemPrompt, contentBlockToText } from '../../../utils';
import { ConverseAnthropicProvider } from './converse';
import { InvokeAnthropicProvider } from './invoke';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Entry point for the Anthropic surface (/v1/messages).
 * Validates the request, maps the model name, and dispatches to
 * ConverseAnthropicProvider (Claude 3.5+) or InvokeAnthropicProvider (Claude 3).
 */
export class ClaudeAnthropicProvider {
  private converseProvider: ConverseAnthropicProvider;
  private invokeProvider: InvokeAnthropicProvider;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.converseProvider = new ConverseAnthropicProvider(authManager, deploymentManager);
    this.invokeProvider = new InvokeAnthropicProvider(authManager, deploymentManager);
  }

  async handleMessages(req: Request, res: Response): Promise<void> {
    const anthropicReq = req.body as AnthropicMessagesRequest;

    if (!anthropicReq.model) {
      res.status(400).json({
        type: 'error',
        error: { type: 'invalid_request_error', message: 'Missing required parameter: model' },
      });
      return;
    }

    if (!anthropicReq.messages || anthropicReq.messages.length === 0) {
      res.status(400).json({
        type: 'error',
        error: { type: 'invalid_request_error', message: 'Missing required parameter: messages' },
      });
      return;
    }

    let sapModelName: string;
    try {
      sapModelName = catalogue.mapFromAnthropic(anthropicReq.model);
    } catch (error: unknown) {
      const err = error as { message?: string };
      res.status(400).json({
        type: 'error',
        error: { type: 'invalid_request_error', message: err.message || 'Unknown model' },
      });
      return;
    }

    logger.info(`Anthropic Messages API: model=${anthropicReq.model} → SAP model=${sapModelName}, stream=${anthropicReq.stream}`);

    if (catalogue.usesConverseApi(sapModelName)) {
      await this.converseProvider.handle(anthropicReq, sapModelName, res);
    } else {
      await this.invokeProvider.handle(anthropicReq, sapModelName, res);
    }
  }

  async handleCountTokens(req: Request, res: Response): Promise<void> {
    const countReq = req.body as AnthropicMessagesRequest;
    let totalChars = 0;

    if (countReq.system) totalChars += extractSystemPrompt(countReq.system).length;
    for (const msg of countReq.messages || []) totalChars += contentBlockToText(msg.content).length;
    if (countReq.tools) totalChars += JSON.stringify(countReq.tools).length;

    const response: AnthropicCountTokensResponse = {
      input_tokens: Math.ceil(totalChars / 4),
    };
    res.json(response);
  }
}
