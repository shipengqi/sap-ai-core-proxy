import { Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIChatCompletionChunk,
  OpenAIMessage,
} from '../../../types/openai';
import { setSSEHeaders, handleOpenAIError } from '../../../utils';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Handles Claude 3 models via SAP AI Core Invoke API.
 * Used by ClaudeOpenAIProvider when the requested model does not support Converse.
 */
export class InvokeOpenAIProvider {
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
      const { systemPrompt, anthropicMessages } = this.convertMessages(messages);
      const payload = this.buildPayload(req, systemPrompt, anthropicMessages);
      const endpoint = stream ? 'invoke-with-response-stream' : 'invoke';
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/${endpoint}`;

      logger.debug(`Invoke request: model=${model}, stream=${stream}, messages=${messages.length}`);

      if (stream) {
        await this.handleStreamingResponse(url, headers, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(url, headers, payload, res, model);
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
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });
    const content = this.extractContent(response.data);

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model,
      choices: [{
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: this.mapStopReason(response.data.stop_reason),
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
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    setSSEHeaders(res);

    const completionId = `chatcmpl-${uuidv4()}`;
    const created = Math.floor(Date.now() / 1000);

    try {
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
      });

      let buffer = '';
      let inputTokens = 0;
      let outputTokens = 0;

      response.data.on('data', (chunk: Buffer) => {
        buffer += chunk.toString('utf-8');
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.trim() === '') continue;
          if (line.startsWith('data: ')) {
            try {
              const data = JSON.parse(line.slice(6));
              const chunks = this.processAnthropicStreamEvent(data, completionId, created, model);
              for (const c of chunks) {
                res.write(`data: ${JSON.stringify(c)}\n\n`);
              }
              if (data.type === 'message_start' && data.message?.usage) {
                inputTokens = data.message.usage.input_tokens || 0;
              }
              if (data.type === 'message_delta' && data.usage) {
                outputTokens = data.usage.output_tokens || 0;
              }
            } catch {
              logger.debug('Failed to parse Anthropic streaming chunk:', line);
            }
          }
        }
      });

      response.data.on('end', () => {
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
      });

      response.data.on('error', (error: Error) => {
        logger.error('Anthropic stream error:', error.message);
        res.end();
      });

    } catch (error: unknown) {
      const axiosError = error as { response?: { data?: unknown }; message?: string };
      logger.error('Anthropic streaming request failed:', axiosError.message);

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

  private processAnthropicStreamEvent(
    data: Record<string, unknown>,
    completionId: string,
    created: number,
    model: string
  ): OpenAIChatCompletionChunk[] {
    const chunks: OpenAIChatCompletionChunk[] = [];

    switch (data.type) {
      case 'message_start':
        chunks.push({
          id: completionId,
          object: 'chat.completion.chunk',
          created,
          model,
          choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }],
        });
        break;

      case 'content_block_delta': {
        const delta = data.delta as Record<string, unknown> | undefined;
        if (delta?.type === 'text_delta' && delta?.text) {
          chunks.push({
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [{ index: 0, delta: { content: delta.text as string }, finish_reason: null }],
          });
        }
        break;
      }

      case 'message_delta': {
        const stopReason = (data.delta as Record<string, unknown>)?.stop_reason as string | undefined;
        if (stopReason) {
          chunks.push({
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [{ index: 0, delta: {}, finish_reason: this.mapStopReason(stopReason) }],
          });
        }
        break;
      }
    }

    return chunks;
  }

  private extractContent(data: Record<string, unknown>): string {
    if (data.content && Array.isArray(data.content)) {
      return data.content
        .filter((block: Record<string, unknown>) => block.type === 'text')
        .map((block: Record<string, unknown>) => block.text)
        .join('');
    }
    return '';
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
