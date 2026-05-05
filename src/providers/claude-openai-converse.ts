import { Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../sap-ai-core/auth';
import { DeploymentManager } from '../sap-ai-core/deployments';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIChatCompletionChunk,
  OpenAIMessage,
} from '../types/openai';
import {
  extractTextContent,
  setSSEHeaders,
  handleOpenAIError,
  parseConverseStream,
  drainErrorBody,
  parseErrorMessage,
  applyPromptCaching,
} from '../utils';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

/**
 * Handles Claude 3.5+ models via SAP AI Core Converse API.
 * Used by ClaudeOpenAIProvider when the requested model supports Converse.
 */
export class ConverseOpenAIProvider {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  async handle(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;
    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const payload = this.buildConversePayload(req, messages);
      const url = stream
        ? `${baseUrl}/v2/inference/deployments/${deploymentId}/converse-stream`
        : `${baseUrl}/v2/inference/deployments/${deploymentId}/converse`;

      logger.debug(`Converse request: model=${model}, stream=${stream}, messages=${messages.length}`);

      if (stream) {
        await this.handleStreamingResponse(url, headers, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(url, headers, payload, res, model);
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

    const payload: Record<string, unknown> = {
      inferenceConfig: {
        maxTokens,
        temperature: req.temperature ?? 0.0,
      },
      messages: applyPromptCaching(converseMessages),
    };

    if (systemPrompt) {
      payload.system = [
        { text: systemPrompt },
        { cachePoint: { type: 'default' } },
      ];
    }

    return payload;
  }

  private async handleNonStreamingResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });
    const content = response.data.output?.message?.content?.[0]?.text || '';

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model,
      choices: [{
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: this.mapStopReason(response.data.stopReason),
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
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const completionId = `chatcmpl-${uuidv4()}`;
    const created = Math.floor(Date.now() / 1000);

    try {
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
        validateStatus: (status) => status < 500,
      });

      if (response.status >= 400) {
        const body = await drainErrorBody(response.data);
        const errorMessage = parseErrorMessage(body);
        logger.error(`Converse streaming error: ${response.status} ${errorMessage}`);
        res.status(response.status).json({
          error: { message: errorMessage, type: 'api_error', param: null, code: response.status.toString() },
        });
        return;
      }

      setSSEHeaders(res);

      let inputTokens = 0;
      let outputTokens = 0;

      for await (const event of parseConverseStream(response.data)) {
        switch (event.type) {
          case 'metadata':
            inputTokens = event.inputTokens || inputTokens;
            outputTokens = event.outputTokens || outputTokens;
            break;
          case 'textDelta':
          case 'reasoningDelta': {
            const chunk: OpenAIChatCompletionChunk = {
              id: completionId,
              object: 'chat.completion.chunk',
              created,
              model,
              choices: [{ index: 0, delta: { content: event.text }, finish_reason: null }],
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
          choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
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
      const axiosError = error as {
        response?: { status?: number; data?: unknown };
        message?: string;
        config?: { url?: string };
      };
      logger.error('Converse streaming request failed:');
      logger.error('  Message: ' + (axiosError.message || 'Unknown'));
      logger.error('  Status: ' + (axiosError.response?.status || 'N/A'));
      logger.error('  URL: ' + (axiosError.config?.url || 'N/A'));

      const errorMessage = axiosError.message || 'Unknown error';

      if (!res.headersSent) {
        res.status(axiosError.response?.status || 500).json({
          error: { message: errorMessage, type: 'api_error', param: null, code: (axiosError.response?.status || 500).toString() },
        });
        return;
      }

      const errorChunk: OpenAIChatCompletionChunk = {
        id: completionId,
        object: 'chat.completion.chunk',
        created,
        model,
        choices: [{ index: 0, delta: { content: `Error: ${errorMessage}` }, finish_reason: 'stop' }],
      };
      res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      res.write('data: [DONE]\n\n');
      res.end();
    }
  }

  private mapStopReason(stopReason: string | undefined): 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null {
    if (!stopReason) return null;
    switch (stopReason) {
      case 'end_turn':
      case 'stop_sequence': return 'stop';
      case 'max_tokens':    return 'length';
      case 'tool_use':      return 'tool_calls';
      default:              return 'stop';
    }
  }
}
