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
 * Handles Gemini model requests via SAP AI Core
 * Converts OpenAI format to Gemini format and back
 */
export class GeminiHandler {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  /**
   * Handles chat completion request for Gemini models
   */
  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();

      // Use Gemini-specific endpoint
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/models/${model}:streamGenerateContent`;

      // Convert OpenAI messages to Gemini format
      const payload = this.buildPayload(req, messages);

      logger.debug(`Gemini request to ${url}`, { model, stream, messageCount: messages.length });

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
   * Extracts text content from OpenAI message content field
   * Handles both string and array formats
   */
  private extractTextContent(content: string | null | undefined | Array<{ type: string; text?: string }>): string {
    if (!content) {
      return '';
    }
    if (typeof content === 'string') {
      return content;
    }
    if (Array.isArray(content)) {
      return content
        .filter((item) => item.type === 'text' && item.text)
        .map((item) => item.text)
        .join('');
    }
    return String(content);
  }

  /**
   * Converts OpenAI messages to Gemini format
   */
  private convertMessages(messages: OpenAIMessage[]): { 
    systemInstruction?: { parts: Array<{ text: string }> };
    contents: Array<{ role: string; parts: Array<{ text: string }> }>;
  } {
    let systemInstruction: { parts: Array<{ text: string }> } | undefined;
    const contents: Array<{ role: string; parts: Array<{ text: string }> }> = [];

    for (const msg of messages) {
      // Extract text content (handle both string and array formats)
      const textContent = this.extractTextContent(msg.content as string | null | Array<{ type: string; text?: string }>);
      
      if (msg.role === 'system') {
        // Collect system messages
        if (!systemInstruction) {
          systemInstruction = { parts: [] };
        }
        if (textContent) {
          systemInstruction.parts.push({ text: textContent });
        }
      } else {
        // Convert role: assistant -> model, user -> user
        const role = msg.role === 'assistant' ? 'model' : 'user';
        contents.push({
          role,
          parts: [{ text: textContent }],
        });
      }
    }

    return { systemInstruction, contents };
  }

  /**
   * Builds the payload for Gemini API
   */
  private buildPayload(
    req: OpenAIChatCompletionRequest,
    messages: OpenAIMessage[]
  ): Record<string, unknown> {
    const { systemInstruction, contents } = this.convertMessages(messages);
    const modelInfo = this.deploymentManager.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo?.maxTokens || 8192;

    const payload: Record<string, unknown> = {
      contents,
      generationConfig: {
        maxOutputTokens: maxTokens,
        temperature: req.temperature ?? 0.0,
      },
    };

    if (systemInstruction) {
      payload.systemInstruction = systemInstruction;
    }

    if (req.top_p !== undefined) {
      (payload.generationConfig as Record<string, unknown>).topP = req.top_p;
    }

    if (req.stop !== undefined) {
      (payload.generationConfig as Record<string, unknown>).stopSequences = 
        Array.isArray(req.stop) ? req.stop : [req.stop];
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
    // For non-streaming, use generateContent endpoint
    const nonStreamUrl = url.replace(':streamGenerateContent', ':generateContent');
    const response = await axios.post(nonStreamUrl, payload, { headers });

    // Convert Gemini response to OpenAI format
    const content = this.extractContent(response.data);
    const usage = this.extractUsage(response.data);
    
    const openaiResponse: OpenAIChatCompletionResponse = {
      id: `chatcmpl-${uuidv4()}`,
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model: model,
      choices: [{
        index: 0,
        message: {
          role: 'assistant',
          content: content,
        },
        finish_reason: this.mapFinishReason(response.data),
      }],
      usage: usage,
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
      let promptTokens = 0;
      let outputTokens = 0;

      // Send initial chunk with role
      const initialChunk: OpenAIChatCompletionChunk = {
        id: completionId,
        object: 'chat.completion.chunk',
        created,
        model,
        choices: [{
          index: 0,
          delta: { role: 'assistant' },
          finish_reason: null,
        }],
      };
      res.write(`data: ${JSON.stringify(initialChunk)}\n\n`);

      response.data.on('data', (chunk: Buffer) => {
        buffer += chunk.toString('utf-8');
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.trim() === '') continue;

          if (line.startsWith('data: ')) {
            try {
              const data = JSON.parse(line.slice(6));
              const processed = this.processStreamChunk(data);

              // Yield text if present
              if (processed.text) {
                const textChunk: OpenAIChatCompletionChunk = {
                  id: completionId,
                  object: 'chat.completion.chunk',
                  created,
                  model,
                  choices: [{
                    index: 0,
                    delta: { content: processed.text },
                    finish_reason: null,
                  }],
                };
                res.write(`data: ${JSON.stringify(textChunk)}\n\n`);
              }

              // Track usage
              if (processed.usageMetadata) {
                promptTokens = processed.usageMetadata.promptTokenCount ?? promptTokens;
                outputTokens = processed.usageMetadata.candidatesTokenCount ?? outputTokens;
              }
            } catch (e) {
              logger.debug('Failed to parse Gemini streaming chunk:', line);
            }
          }
        }
      });

      response.data.on('end', () => {
        // Send finish chunk
        const finishChunk: OpenAIChatCompletionChunk = {
          id: completionId,
          object: 'chat.completion.chunk',
          created,
          model,
          choices: [{
            index: 0,
            delta: {},
            finish_reason: 'stop',
          }],
        };
        res.write(`data: ${JSON.stringify(finishChunk)}\n\n`);

        // Send usage chunk if we have usage data
        if (promptTokens > 0 || outputTokens > 0) {
          const usageChunk: OpenAIChatCompletionChunk = {
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [],
            usage: {
              prompt_tokens: promptTokens,
              completion_tokens: outputTokens,
              total_tokens: promptTokens + outputTokens,
            },
          };
          res.write(`data: ${JSON.stringify(usageChunk)}\n\n`);
        }
        res.write('data: [DONE]\n\n');
        res.end();
      });

      response.data.on('error', (error: Error) => {
        logger.error('Gemini stream error:', error.message);
        res.end();
      });

    } catch (error: unknown) {
      const axiosError = error as { response?: { data?: unknown }; message?: string };
      logger.error('Gemini streaming request failed:', axiosError.message);
      
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
   * Process Gemini streaming response chunk
   */
  private processStreamChunk(data: Record<string, unknown>): {
    text?: string;
    reasoning?: string;
    usageMetadata?: {
      promptTokenCount?: number;
      candidatesTokenCount?: number;
      thoughtsTokenCount?: number;
      cachedContentTokenCount?: number;
    };
  } {
    const result: ReturnType<typeof this.processStreamChunk> = {};

    // Handle thinking content from Gemini's response
    const candidates = data.candidates as Array<{ content?: { parts?: Array<{ thought?: boolean; text?: string }> } }> | undefined;
    const candidateForThoughts = candidates?.[0];
    const partsForThoughts = candidateForThoughts?.content?.parts;
    let thoughts = '';

    if (partsForThoughts) {
      for (const part of partsForThoughts) {
        if (part.thought && part.text) {
          thoughts += part.text + '\n';
        }
      }
    }

    if (thoughts.trim() !== '') {
      result.reasoning = thoughts.trim();
    }

    // Handle regular text content
    if (candidates && candidates[0]?.content?.parts) {
      let nonThoughtText = '';
      for (const part of candidates[0].content.parts) {
        if (part.text && !part.thought) {
          nonThoughtText += part.text;
        }
      }
      if (nonThoughtText) {
        result.text = nonThoughtText;
      }
    }

    // Handle usage metadata with caching support
    const usageMetadata = data.usageMetadata as Record<string, number> | undefined;
    if (usageMetadata) {
      result.usageMetadata = {
        promptTokenCount: usageMetadata.promptTokenCount,
        candidatesTokenCount: usageMetadata.candidatesTokenCount,
        thoughtsTokenCount: usageMetadata.thoughtsTokenCount,
        cachedContentTokenCount: usageMetadata.cachedContentTokenCount,
      };
    }

    return result;
  }

  /**
   * Extracts content from Gemini response
   */
  private extractContent(data: Record<string, unknown>): string {
    const candidates = data.candidates as Array<{ content?: { parts?: Array<{ text?: string }> } }> | undefined;
    if (candidates && candidates[0]?.content?.parts) {
      return candidates[0].content.parts
        .filter(part => part.text)
        .map(part => part.text)
        .join('');
    }
    return '';
  }

  /**
   * Extracts usage from Gemini response
   */
  private extractUsage(data: Record<string, unknown>): OpenAIChatCompletionResponse['usage'] | undefined {
    const usageMetadata = data.usageMetadata as Record<string, number> | undefined;
    if (usageMetadata) {
      const promptTokens = usageMetadata.promptTokenCount || 0;
      const completionTokens = usageMetadata.candidatesTokenCount || 0;
      return {
        prompt_tokens: promptTokens,
        completion_tokens: completionTokens,
        total_tokens: promptTokens + completionTokens,
      };
    }
    return undefined;
  }

  /**
   * Maps Gemini finish reason to OpenAI format
   */
  private mapFinishReason(data: Record<string, unknown>): 'stop' | 'length' | 'function_call' | 'tool_calls' | 'content_filter' | null {
    const candidates = data.candidates as Array<{ finishReason?: string }> | undefined;
    const finishReason = candidates?.[0]?.finishReason;
    
    if (!finishReason) return null;
    
    switch (finishReason) {
      case 'STOP':
        return 'stop';
      case 'MAX_TOKENS':
        return 'length';
      case 'SAFETY':
        return 'content_filter';
      default:
        return 'stop';
    }
  }

  /**
   * Handles errors
   */
  private handleError(error: unknown, res: Response): void {
    const axiosError = error as { 
      response?: { status?: number; data?: unknown }; 
      message?: string;
      config?: { url?: string };
    };

    logger.error('Gemini handler error:', axiosError.message);
    if (axiosError.response?.data) {
      logger.error('  Response data:', axiosError.response.data);
    }

    const statusCode = axiosError.response?.status || 500;
    
    // Extract error message
    let errorMessage = 'Internal server error';
    const responseData = axiosError.response?.data;
    
    if (responseData) {
      if (typeof responseData === 'string') {
        errorMessage = responseData;
      } else if (typeof responseData === 'object') {
        const data = responseData as Record<string, unknown>;
        // Handle various error formats
        if (data.error && typeof data.error === 'object') {
          const err = data.error as Record<string, unknown>;
          errorMessage = (err.message as string) || JSON.stringify(data.error);
        } else if (data.errors && typeof data.errors === 'object') {
          const errors = data.errors as Record<string, unknown>;
          errorMessage = (errors.message as string) || JSON.stringify(data.errors);
        } else if (data.message) {
          errorMessage = data.message as string;
        } else {
          errorMessage = JSON.stringify(responseData);
        }
      }
    } else if (axiosError.message) {
      errorMessage = axiosError.message;
    }

    // Add helpful context for common errors
    if (statusCode === 429) {
      errorMessage = `Rate limit exceeded: ${errorMessage}. Please wait and try again later.`;
    }

    res.status(statusCode).json({
      error: {
        message: errorMessage,
        type: statusCode === 429 ? 'rate_limit_error' : 'api_error',
        param: null,
        code: statusCode.toString(),
      },
    });
  }
}
