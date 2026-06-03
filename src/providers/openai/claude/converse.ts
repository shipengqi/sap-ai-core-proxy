import { Response } from 'express';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIMessage,
} from '../../../types/openai';
import {
  extractTextContent,
  handleOpenAIError,
  mapConverseStopReasonToOpenAI,
  assembleConversePayload,
  orchestrateStream,
} from '../../../utils';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Handles Claude 3.5+ models via SAP AI Core Converse API.
 * Used by ClaudeOpenAIProvider when the requested model supports Converse.
 */
export class ConverseOpenAIProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handle(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;
    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const payload = this.buildConversePayload(req, messages);
      const path = stream
        ? `/v2/inference/deployments/${deploymentId}/converse-stream`
        : `/v2/inference/deployments/${deploymentId}/converse`;

      logger.debug(`Converse request: model=${model}, stream=${stream}, messages=${messages.length}`);

      if (stream) {
        await this.handleStreamingResponse(path, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(path, payload, res, model);
      }
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  private buildConversePayload(
    req: OpenAIChatCompletionRequest,
    messages: OpenAIMessage[]
  ): Record<string, unknown> {
    const modelInfo = catalogue.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo.maxTokens;

    let systemPrompt = '';
    const converseMessages: Array<{ role: 'user' | 'assistant'; content: Array<{ text: string }> }> = [];

    for (const msg of messages) {
      const textContent = extractTextContent(msg.content as string | null | Array<{ type: string; text?: string }>);
      if (msg.role === 'system') {
        if (textContent) systemPrompt += (systemPrompt ? '\n' : '') + textContent;
      } else {
        converseMessages.push({
          role: msg.role === 'assistant' ? 'assistant' : 'user',
          content: [{ text: textContent }],
        });
      }
    }

    return assembleConversePayload({
      maxTokens,
      temperature: req.temperature ?? 0.0,
      messages: converseMessages,
      system: systemPrompt || undefined,
    });
  }

  private async handleNonStreamingResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await this.client.post(path, payload);
    const content = response.data.output?.message?.content?.[0]?.text || '';

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model,
      choices: [{
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: mapConverseStopReasonToOpenAI(response.data.stopReason),
      }],
      usage: response.data.usage ? {
        prompt_tokens: response.data.usage.inputTokens || 0,
        completion_tokens: response.data.usage.outputTokens || 0,
        total_tokens: (response.data.usage.inputTokens || 0) + (response.data.usage.outputTokens || 0),
      } : undefined,
    };

    res.json(openaiResponse);
  }

  private async handleStreamingResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const completionId = `chatcmpl-${uuidv4()}`;
    const response = await this.client.postStream(path, payload);
    await orchestrateStream(response, {
      apiFormat: 'converse',
      responseFormat: 'openai',
      model,
      completionId,
    }, res);
  }

}
