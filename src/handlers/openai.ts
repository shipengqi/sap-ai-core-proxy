import { Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../auth';
import { DeploymentManager } from '../deployments';
import { 
  OpenAIChatCompletionRequest, 
  OpenAIChatCompletionResponse,
  OpenAIChatCompletionChunk,
  OpenAIMessage 
} from '../types';
import { logger } from '../logger';

/**
 * Handles OpenAI-compatible model requests
 */
export class OpenAIHandler {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  /**
   * Handles chat completion request
   */
  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();

      // Build the SAP AI Core URL for OpenAI-compatible models
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/chat/completions?api-version=2024-06-01`;

      // Prepare the payload
      const payload = this.buildPayload(req);

      logger.debug(`OpenAI request to ${url}`, { model, stream, messageCount: messages.length });

      if (stream) {
        await this.handleStreamingResponse(url, headers, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(url, headers, payload, res, model);
      }
    } catch (error: unknown) {
      this.handleError(error, res);
    }
  }

  /**
   * Builds the payload for OpenAI-compatible API
   */
  private buildPayload(req: OpenAIChatCompletionRequest): Record<string, unknown> {
    const payload: Record<string, unknown> = {
      messages: req.messages,
      stream: req.stream || false,
    };

    // Optional parameters
    if (req.temperature !== undefined) payload.temperature = req.temperature;
    if (req.top_p !== undefined) payload.top_p = req.top_p;
    if (req.n !== undefined) payload.n = req.n;
    if (req.max_tokens !== undefined) payload.max_tokens = req.max_tokens;
    if (req.presence_penalty !== undefined) payload.presence_penalty = req.presence_penalty;
    if (req.frequency_penalty !== undefined) payload.frequency_penalty = req.frequency_penalty;
    if (req.stop !== undefined) payload.stop = req.stop;
    if (req.logit_bias !== undefined) payload.logit_bias = req.logit_bias;
    if (req.user !== undefined) payload.user = req.user;

    // Tools/Functions
    if (req.tools !== undefined) payload.tools = req.tools;
    if (req.tool_choice !== undefined) payload.tool_choice = req.tool_choice;
    if (req.functions !== undefined) payload.functions = req.functions;
    if (req.function_call !== undefined) payload.function_call = req.function_call;

    // Add stream options for usage tracking
    if (req.stream) {
      payload.stream_options = { include_usage: true };
    }

    return payload;
  }

  /**
   * Handles non-streaming response
   */
  private async handleNonStreamingResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });

    const openaiResponse: OpenAIChatCompletionResponse = {
      id: response.data.id || `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: response.data.created || Math.floor(Date.now() / 1000),
      model: model,
      choices: response.data.choices || [{
        index: 0,
        message: {
          role: 'assistant',
          content: response.data.choices?.[0]?.message?.content || '',
        },
        finish_reason: response.data.choices?.[0]?.finish_reason || 'stop',
      }],
      usage: response.data.usage,
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
    res.setHeader('Content-Type', 'text/event-stream');
    res.setHeader('Cache-Control', 'no-cache');
    res.setHeader('Connection', 'keep-alive');
    res.setHeader('X-Accel-Buffering', 'no');

    const completionId = `chatcmpl-${uuidv4()}`;
    const created = Math.floor(Date.now() / 1000);

    try {
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
      });

      let buffer = '';

      response.data.on('data', (chunk: Buffer) => {
        buffer += chunk.toString('utf-8');
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.trim() === '') continue;
          if (line.trim() === 'data: [DONE]') {
            res.write('data: [DONE]\n\n');
            continue;
          }

          if (line.startsWith('data: ')) {
            try {
              const data = JSON.parse(line.slice(6));
              const chunk = this.transformStreamChunk(data, completionId, created, model);
              res.write(`data: ${JSON.stringify(chunk)}\n\n`);
            } catch (e) {
              logger.debug('Failed to parse streaming chunk:', line);
            }
          }
        }
      });

      response.data.on('end', () => {
        if (buffer.trim() && buffer.trim() !== 'data: [DONE]') {
          if (buffer.startsWith('data: ')) {
            try {
              const data = JSON.parse(buffer.slice(6));
              const chunk = this.transformStreamChunk(data, completionId, created, model);
              res.write(`data: ${JSON.stringify(chunk)}\n\n`);
            } catch (e) {
              logger.debug('Failed to parse final streaming chunk');
            }
          }
        }
        res.write('data: [DONE]\n\n');
        res.end();
      });

      response.data.on('error', (error: Error) => {
        logger.error('Stream error:', error.message);
        res.end();
      });

    } catch (error: unknown) {
      const axiosError = error as { response?: { data?: unknown }; message?: string };
      logger.error('Streaming request failed:', axiosError.message);
      
      // Send error as SSE
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
   * Transforms a stream chunk to OpenAI format
   */
  private transformStreamChunk(
    data: Record<string, unknown>,
    completionId: string,
    created: number,
    model: string
  ): OpenAIChatCompletionChunk {
    const choices = (data.choices as Array<{ delta?: { content?: string; role?: string }; finish_reason?: string; index?: number }>) || [];
    
    return {
      id: (data.id as string) || completionId,
      object: 'chat.completion.chunk',
      created: (data.created as number) || created,
      model: (data.model as string) || model,
      choices: choices.map((choice, idx) => ({
        index: choice.index ?? idx,
        delta: {
          role: choice.delta?.role as OpenAIMessage['role'] | undefined,
          content: choice.delta?.content,
        },
        finish_reason: (choice.finish_reason as 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter') || null,
      })),
      usage: data.usage as OpenAIChatCompletionChunk['usage'],
    };
  }

  /**
   * Handles errors
   */
  private handleError(error: unknown, res: Response): void {
    const axiosError = error as { 
      response?: { status?: number; data?: unknown }; 
      message?: string 
    };

    logger.error('OpenAI handler error:', axiosError.message);

    const statusCode = axiosError.response?.status || 500;
    const errorMessage = typeof axiosError.response?.data === 'object' 
      ? JSON.stringify(axiosError.response.data)
      : axiosError.message || 'Internal server error';

    res.status(statusCode).json({
      error: {
        message: errorMessage,
        type: 'api_error',
        param: null,
        code: statusCode.toString(),
      },
    });
  }
}