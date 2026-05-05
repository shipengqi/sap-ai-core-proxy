import { Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../sap-ai-core/auth';
import { DeploymentManager } from '../sap-ai-core/deployments';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIChatCompletionChunk,
  OpenAIMessage
} from '../types/openai';
import {
  extractTextContent,
  setSSEHeaders,
  extractErrorDetails,
  sendOpenAIError,
  parseConverseStream,
  drainErrorBody,
  parseErrorMessage,
  applyPromptCaching,
} from '../utils';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

/**
 * Handles Anthropic/Claude model requests via SAP AI Core
 * Accepts OpenAI format input, converts to Anthropic format and back
 */
export class ClaudeOpenAIProvider {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  /**
   * Handles chat completion request for Anthropic models
   */
  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();

      const useConverseStream = catalogue.usesConverseApi(model);

      let url: string;
      let payload: Record<string, unknown>;

      if (useConverseStream && stream) {
        // Use converse-stream endpoint for newer Claude models
        url = `${baseUrl}/v2/inference/deployments/${deploymentId}/converse-stream`;
        payload = this.buildConversePayload(req, messages);
      } else if (useConverseStream && !stream) {
        // Use converse endpoint for non-streaming
        url = `${baseUrl}/v2/inference/deployments/${deploymentId}/converse`;
        payload = this.buildConversePayload(req, messages);
      } else {
        // Use invoke endpoint for older Anthropic models
        const endpoint = stream ? 'invoke-with-response-stream' : 'invoke';
        url = `${baseUrl}/v2/inference/deployments/${deploymentId}/${endpoint}`;
        const { systemPrompt, anthropicMessages } = this.convertMessages(messages);
        payload = this.buildPayload(req, systemPrompt, anthropicMessages);
      }

      logger.debug(`Anthropic request to ${url}`);
      logger.debug('  Model: ' + model);
      logger.debug('  Stream: ' + stream);
      logger.debug('  Message Count: ' + messages.length);
      logger.debug('  Use Converse Stream: ' + useConverseStream);

      if (stream) {
        if (useConverseStream) {
          await this.handleConverseStreamingResponse(url, headers, payload, res, model);
        } else {
          await this.handleStreamingResponse(url, headers, payload, res, model);
        }
      } else {
        if (useConverseStream) {
          await this.handleConverseNonStreamingResponse(url, headers, payload, res, model);
        } else {
          await this.handleNonStreamingResponse(url, headers, payload, res, model);
        }
      }
    } catch (error: unknown) {
      this.handleError(error, res);
    }
  }

  /**
   * Converts OpenAI messages to Anthropic format
   */
  private convertMessages(messages: OpenAIMessage[]): {
    systemPrompt: string;
    anthropicMessages: Array<{ role: string; content: string }>
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

  /**
   * Builds the payload for Anthropic API (invoke endpoint)
   */
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
      messages: messages,
    };

    if (systemPrompt) {
      payload.system = systemPrompt;
    }

    // Optional parameters
    if (req.temperature !== undefined) payload.temperature = req.temperature;
    if (req.top_p !== undefined) payload.top_p = req.top_p;
    if (req.stop !== undefined) {
      payload.stop_sequences = Array.isArray(req.stop) ? req.stop : [req.stop];
    }

    return payload;
  }

  /**
   * Builds the payload for Converse API (newer Claude models)
   * Uses AWS Bedrock Converse API format
   */
  private buildConversePayload(
    req: OpenAIChatCompletionRequest,
    messages: OpenAIMessage[]
  ): Record<string, unknown> {
    const modelInfo = catalogue.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo.maxTokens;

    // Collect system prompt and convert messages to Bedrock Converse API format
    let systemPrompt = '';
    const converseMessages: Array<{ role: 'user' | 'assistant'; content: Array<{ text: string }> }> = [];

    for (const msg of messages) {
      const textContent = extractTextContent(msg.content as string | null | Array<{ type: string; text?: string }>);

      if (msg.role === 'system') {
        if (textContent) {
          systemPrompt += (systemPrompt ? '\n' : '') + textContent;
        }
      } else {
        converseMessages.push({
          role: msg.role === 'assistant' ? 'assistant' : 'user',
          content: [{ text: textContent }],
        });
      }
    }

    const payload: Record<string, unknown> = {
      inferenceConfig: {
        maxTokens: maxTokens,
        temperature: req.temperature ?? 0.0,
      },
      messages: applyPromptCaching(converseMessages),
    };

    // Add system messages with caching support (like Cline does)
    if (systemPrompt) {
      payload.system = [
        { text: systemPrompt },
        { cachePoint: { type: 'default' } },
      ];
    }

    return payload;
  }

  /**
   * Handles non-streaming response for Converse API
   */
  private async handleConverseNonStreamingResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });

    // Extract content from Converse response
    const output = response.data.output;
    const content = output?.message?.content?.[0]?.text || '';

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model: model,
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: content,
        },
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

  /**
   * Handles streaming response for Converse Stream API
   */
  private async handleConverseStreamingResponse(
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
          error: {
            message: errorMessage,
            type: 'api_error',
            param: null,
            code: (axiosError.response?.status || 500).toString(),
          },
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

  /**
   * Handles non-streaming response (invoke endpoint)
   */
  private async handleNonStreamingResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });

    // Convert Anthropic response to OpenAI format
    const content = this.extractContent(response.data);

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model: model,
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: content,
        },
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

  /**
   * Handles streaming response
   */
  private async handleStreamingResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    // Set SSE headers
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

              for (const chunk of chunks) {
                res.write(`data: ${JSON.stringify(chunk)}\n\n`);
              }

              // Track usage
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
        // Send final usage chunk if we have usage data
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
        choices: [{
          index: 0,
          delta: { content: `Error: ${axiosError.message}` },
          finish_reason: 'stop',
        }],
      };
      res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      res.write('data: [DONE]\n\n');
      res.end();
    }
  }

  /**
   * Processes Anthropic stream events and converts to OpenAI format
   */
  private processAnthropicStreamEvent(
    data: Record<string, unknown>,
    completionId: string,
    created: number,
    model: string
  ): OpenAIChatCompletionChunk[] {
    const chunks: OpenAIChatCompletionChunk[] = [];

    switch (data.type) {
      case 'message_start':
        // Send initial chunk with role
        chunks.push({
          id: completionId,
          object: 'chat.completion.chunk',
          created,
          model,
          choices: [{
            index: 0,
            delta: { role: 'assistant' },
            finish_reason: null,
          }],
        });
        break;

      case 'content_block_start':
        // Content block starting
        break;

      case 'content_block_delta': {
        const delta = data.delta as Record<string, unknown> | undefined;
        if (delta?.type === 'text_delta' && delta?.text) {
          chunks.push({
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [{
              index: 0,
              delta: { content: delta.text as string },
              finish_reason: null,
            }],
          });
        }
        break;
      }

      case 'content_block_stop':
        // Content block ended
        break;

      case 'message_delta': {
        const stopReason = (data.delta as Record<string, unknown>)?.stop_reason as string | undefined;
        if (stopReason) {
          chunks.push({
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [{
              index: 0,
              delta: {},
              finish_reason: this.mapStopReason(stopReason),
            }],
          });
        }
        break;
      }

      case 'message_stop':
        // Message ended
        break;
    }

    return chunks;
  }

  /**
   * Handles errors
   */
  private handleError(error: unknown, res: Response): void {
    const { statusCode, message } = extractErrorDetails(error);
    sendOpenAIError(res, statusCode, message);
  }

  /**
   * Extracts content from Anthropic response
   */
  private extractContent(data: Record<string, unknown>): string {
    if (data.content && Array.isArray(data.content)) {
      return data.content
        .filter((block: Record<string, unknown>) => block.type === 'text')
        .map((block: Record<string, unknown>) => block.text)
        .join('');
    }
    return '';
  }

  /**
   * Maps Anthropic stop reason to OpenAI finish reason
   */
  private mapStopReason(stopReason: string | undefined): 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null {
    if (!stopReason) return null;

    switch (stopReason) {
      case 'end_turn':
      case 'stop_sequence':
        return 'stop';
      case 'max_tokens':
        return 'length';
      case 'tool_use':
        return 'tool_calls';
      default:
        return 'stop';
    }
  }
}
