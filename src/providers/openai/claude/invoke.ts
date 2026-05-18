import { Response } from 'express';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIChatCompletionChunk,
  OpenAIMessage,
} from '../../../types/openai';
import { setSSEHeaders, handleOpenAIError, mapConverseStopReasonToOpenAI, contentBlockToText, endStreamOnError, parseInvokeStream, drainErrorBody, parseErrorMessage, sendOpenAIError } from '../../../utils';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Handles Claude 3 models via SAP AI Core Invoke API.
 * Used by ClaudeOpenAIProvider when the requested model does not support Converse.
 */
export class InvokeOpenAIProvider {
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
      const { systemPrompt, anthropicMessages } = this.convertMessages(messages);
      const payload = this.buildPayload(req, systemPrompt, anthropicMessages);
      const endpoint = stream ? 'invoke-with-response-stream' : 'invoke';
      const path = `/v2/inference/deployments/${deploymentId}/${endpoint}`;

      logger.debug(`Invoke request: model=${model}, stream=${stream}, messages=${messages.length}`);

      if (stream) {
        await this.handleStreamingResponse(path, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(path, payload, res, model);
      }
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  private convertMessages(messages: OpenAIMessage[]): {
    systemPrompt: string;
    anthropicMessages: Array<{ role: string; content: string }>;
  } {
    let systemPrompt = '';
    const anthropicMessages: Array<{ role: string; content: string }> = [];

    for (const msg of messages) {
      if (msg.role === 'system') {
        systemPrompt += (systemPrompt ? '\n' : '') + (msg.content || '');
      } else {
        anthropicMessages.push({
          role: msg.role === 'assistant' ? 'assistant' : 'user',
          content: msg.content || '',
        });
      }
    }

    return { systemPrompt, anthropicMessages };
  }

  private buildPayload(
    req: OpenAIChatCompletionRequest,
    systemPrompt: string,
    messages: Array<{ role: string; content: string }>
  ): Record<string, unknown> {
    const modelInfo = catalogue.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo.maxTokens;

    const payload: Record<string, unknown> = {
      anthropic_version: 'bedrock-2023-05-31',
      max_tokens: maxTokens,
      messages,
    };

    if (systemPrompt) payload.system = systemPrompt;
    if (req.temperature !== undefined) payload.temperature = req.temperature;
    if (req.top_p !== undefined) payload.top_p = req.top_p;
    if (req.stop !== undefined) {
      payload.stop_sequences = Array.isArray(req.stop) ? req.stop : [req.stop];
    }

    return payload;
  }

  private async handleNonStreamingResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await this.client.post(path, payload);
    const content = contentBlockToText(response.data.content ?? []);

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model,
      choices: [{
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: mapConverseStopReasonToOpenAI(response.data.stop_reason),
      }],
      usage: response.data.usage ? {
        prompt_tokens: response.data.usage.input_tokens || 0,
        completion_tokens: response.data.usage.output_tokens || 0,
        total_tokens: (response.data.usage.input_tokens || 0) + (response.data.usage.output_tokens || 0),
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
    const created = Math.floor(Date.now() / 1000);

    try {
      const response = await this.client.postStream(path, payload);

      if (response.status >= 400) {
        const body = await drainErrorBody(response.data);
        sendOpenAIError(res, response.status, parseErrorMessage(body));
        return;
      }

      setSSEHeaders(res);

      let inputTokens = 0;
      let outputTokens = 0;

      for await (const event of parseInvokeStream(response.data)) {
        switch (event.type) {
          case 'messageStart': {
            inputTokens = event.inputTokens;
            const chunk: OpenAIChatCompletionChunk = {
              id: completionId,
              object: 'chat.completion.chunk',
              created,
              model,
              choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }],
            };
            res.write(`data: ${JSON.stringify(chunk)}\n\n`);
            break;
          }
          case 'blockDelta': {
            if (event.delta.type === 'text_delta' && event.delta.text) {
              const chunk: OpenAIChatCompletionChunk = {
                id: completionId,
                object: 'chat.completion.chunk',
                created,
                model,
                choices: [{ index: 0, delta: { content: event.delta.text as string }, finish_reason: null }],
              };
              res.write(`data: ${JSON.stringify(chunk)}\n\n`);
            }
            break;
          }
          case 'messageDelta': {
            outputTokens = event.outputTokens;
            const chunk: OpenAIChatCompletionChunk = {
              id: completionId,
              object: 'chat.completion.chunk',
              created,
              model,
              choices: [{ index: 0, delta: {}, finish_reason: mapConverseStopReasonToOpenAI(event.stopReason) }],
            };
            res.write(`data: ${JSON.stringify(chunk)}\n\n`);
            break;
          }
        }
      }

      if (inputTokens > 0 || outputTokens > 0) {
        const usageChunk: OpenAIChatCompletionChunk = {
          id: completionId,
          object: 'chat.completion.chunk',
          created,
          model,
          choices: [],
          usage: {
            prompt_tokens: inputTokens,
            completion_tokens: outputTokens,
            total_tokens: inputTokens + outputTokens,
          },
        };
        res.write(`data: ${JSON.stringify(usageChunk)}\n\n`);
      }
      res.write('data: [DONE]\n\n');
      res.end();

    } catch (error: unknown) {
      const axiosError = error as { response?: { status?: number }; message?: string };
      logger.error('Anthropic streaming request failed:', axiosError.message);

      if (!res.headersSent) {
        sendOpenAIError(res, axiosError.response?.status || 500, axiosError.message || 'Request failed');
        return;
      }

      const errorChunk: OpenAIChatCompletionChunk = {
        id: completionId,
        object: 'chat.completion.chunk',
        created,
        model,
        choices: [{ index: 0, delta: { content: `Error: ${axiosError.message}` }, finish_reason: 'stop' }],
      };
      res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      res.write('data: [DONE]\n\n');
      res.end();
    }
  }

}
