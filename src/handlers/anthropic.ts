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
 * Handles Anthropic/Claude model requests via SAP AI Core
 * Converts OpenAI format to Anthropic format and back
 */
export class AnthropicHandler {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  // Models that use the converse-stream endpoint
  private readonly converseStreamModels = [
    'anthropic--claude-4.6-sonnet',
    'anthropic--claude-4.5-sonnet',
    'anthropic--claude-4.5-opus',
    'anthropic--claude-4.5-haiku',
    'anthropic--claude-4-sonnet',
    'anthropic--claude-4-opus',
    'anthropic--claude-3.7-sonnet',
  ];

  /**
   * Handles chat completion request for Anthropic models
   */
  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();

      // Check if this model uses the converse-stream endpoint
      const useConverseStream = this.converseStreamModels.some(m => 
        model.toLowerCase().includes(m.toLowerCase()) || m.toLowerCase().includes(model.toLowerCase())
      );

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
    const modelInfo = this.deploymentManager.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo?.maxTokens || 4096;

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
   * Builds the payload for Converse API (newer Claude models)
   * Uses AWS Bedrock Converse API format
   */
  private buildConversePayload(
    req: OpenAIChatCompletionRequest,
    messages: OpenAIMessage[]
  ): Record<string, unknown> {
    const modelInfo = this.deploymentManager.getModelInfo(req.model);
    const maxTokens = req.max_tokens || modelInfo?.maxTokens || 16384;

    // Collect system prompt and convert messages to Bedrock Converse API format
    let systemPrompt = '';
    const converseMessages: Array<{ role: 'user' | 'assistant'; content: Array<{ text: string }> }> = [];

    // Track user message indices for caching
    const userMsgIndices: number[] = [];

    for (const msg of messages) {
      // Extract text content (handle both string and array formats)
      const textContent = this.extractTextContent(msg.content as string | null | Array<{ type: string; text?: string }>);
      
      if (msg.role === 'system') {
        // Concatenate system messages
        if (textContent) {
          systemPrompt += (systemPrompt ? '\n' : '') + textContent;
        }
      } else {
        const role = msg.role === 'assistant' ? 'assistant' : 'user';
        if (role === 'user') {
          userMsgIndices.push(converseMessages.length);
        }
        converseMessages.push({
          role: role as 'user' | 'assistant',
          content: [{ text: textContent }],
        });
      }
    }

    // Apply caching to last two user messages (like Cline does)
    const lastUserMsgIndex = userMsgIndices[userMsgIndices.length - 1] ?? -1;
    const secondLastMsgUserIndex = userMsgIndices[userMsgIndices.length - 2] ?? -1;

    const messagesWithCache = converseMessages.map((message, index) => {
      if (index === lastUserMsgIndex || index === secondLastMsgUserIndex) {
        // Add cachePoint to the end of the content array
        return {
          ...message,
          content: [
            ...message.content,
            { cachePoint: { type: 'default' } },
          ],
        };
      }
      return message;
    });

    const payload: Record<string, unknown> = {
      inferenceConfig: {
        maxTokens: maxTokens,
        temperature: req.temperature ?? 0.0,
      },
      messages: messagesWithCache,
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
      // First, make a non-streaming request to validate the request
      // This helps catch errors before setting up the stream
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
        validateStatus: (status) => status < 500, // Accept 4xx to handle them ourselves
      });

      // If we got an error status, read the error response
      if (response.status >= 400) {
        let errorBody = '';
        for await (const chunk of response.data) {
          errorBody += chunk.toString('utf-8');
        }
        
        logger.error('Converse streaming request error response:');
        logger.error('  Status: ' + response.status);
        logger.error('  Body: ' + errorBody);
        
        // Parse error message
        let errorMessage = 'Request failed';
        try {
          const errorData = JSON.parse(errorBody);
          if (errorData.error?.message) {
            errorMessage = errorData.error.message;
          } else if (errorData.errors?.message) {
            errorMessage = errorData.errors.message;
          } else if (errorData.message) {
            errorMessage = errorData.message;
          } else {
            errorMessage = errorBody;
          }
        } catch {
          errorMessage = errorBody || 'Unknown error';
        }
        
        res.status(response.status).json({
          error: {
            message: errorMessage,
            type: 'api_error',
            param: null,
            code: response.status.toString(),
          },
        });
        return;
      }

      // Set SSE headers only after successful connection
      res.setHeader('Content-Type', 'text/event-stream');
      res.setHeader('Cache-Control', 'no-cache');
      res.setHeader('Connection', 'keep-alive');
      res.setHeader('X-Accel-Buffering', 'no');

      let buffer = '';
      let inputTokens = 0;
      let outputTokens = 0;

      logger.debug('Converse stream connected, waiting for data...');

      response.data.on('data', (chunk: Buffer) => {
        const chunkStr = chunk.toString('utf-8');
        logger.debug('Received raw chunk (' + chunkStr.length + ' chars)');
        
        buffer += chunkStr;
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.trim() === '') continue;
          
          logger.debug('Processing line: ' + line.substring(0, 200));

          // Try to parse as SSE format first (data: {...})
          if (line.startsWith('data: ')) {
            try {
              let jsonStr = line.slice(6);
              // SAP AI Core may return single-quoted JSON (Python style)
              // Convert to standard double-quoted JSON
              jsonStr = this.convertPythonJsonToStandardJson(jsonStr);
              const data = JSON.parse(jsonStr);
              logger.debug('Parsed SSE data: ' + JSON.stringify(data).substring(0, 100));
              this.processConverseStreamData(data, completionId, created, model, res, { inputTokens, outputTokens });
            } catch (e) {
              logger.debug('Failed to parse SSE chunk:', line.substring(0, 100));
            }
          } else {
            // Try to parse as raw JSON (SAP AI Core may send raw JSON lines)
            try {
              let jsonStr = line;
              jsonStr = this.convertPythonJsonToStandardJson(jsonStr);
              const data = JSON.parse(jsonStr);
              logger.debug('Parsed raw JSON data');
              this.processConverseStreamData(data, completionId, created, model, res, { inputTokens, outputTokens });
            } catch (e) {
              // Not JSON, log it
              logger.debug('Non-JSON line: ' + line.substring(0, 100));
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
            choices: [{
              index: 0,
              delta: {},
              finish_reason: 'stop',
            }],
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
        logger.error('Converse stream error:', error.message);
        res.end();
      });

    } catch (error: unknown) {
      const axiosError = error as { 
        response?: { status?: number; data?: unknown }; 
        message?: string;
        config?: { url?: string };
      };
      
      // Log detailed error information
      logger.error('Converse streaming request failed:');
      logger.error('  Message: ' + (axiosError.message || 'Unknown'));
      logger.error('  Status: ' + (axiosError.response?.status || 'N/A'));
      logger.error('  URL: ' + (axiosError.config?.url || 'N/A'));
      
      // Safely stringify response data
      let responseDataStr = 'N/A';
      try {
        if (axiosError.response?.data) {
          if (typeof axiosError.response.data === 'string') {
            responseDataStr = axiosError.response.data;
          } else if (Buffer.isBuffer(axiosError.response.data)) {
            responseDataStr = axiosError.response.data.toString('utf-8');
          } else {
            responseDataStr = JSON.stringify(axiosError.response.data, null, 2);
          }
        }
      } catch (e) {
        responseDataStr = 'Unable to stringify response data';
      }
      logger.error('  Response Data: ' + responseDataStr);
      
      // Extract error message
      let errorMessage = axiosError.message || 'Unknown error';
      const responseData = axiosError.response?.data;
      if (responseData && typeof responseData === 'object') {
        const data = responseData as Record<string, unknown>;
        if (data.errors && typeof data.errors === 'object') {
          const errors = data.errors as Record<string, unknown>;
          errorMessage = (errors.message as string) || JSON.stringify(data.errors);
        } else if (data.message) {
          errorMessage = data.message as string;
        }
      }

      // If headers haven't been sent yet, send a proper error response
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
        choices: [{
          index: 0,
          delta: { content: `Error: ${errorMessage}` },
          finish_reason: 'stop',
        }],
      };
      res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      res.write('data: [DONE]\n\n');
      res.end();
    }
  }

  /**
   * Processes Converse stream data and writes to response
   */
  private processConverseStreamData(
    data: Record<string, unknown>,
    completionId: string,
    created: number,
    model: string,
    res: Response,
    usage: { inputTokens: number; outputTokens: number }
  ): void {
    // Handle metadata (token usage)
    const metadata = data.metadata as Record<string, unknown> | undefined;
    if (metadata?.usage) {
      const usageData = metadata.usage as Record<string, number>;
      usage.inputTokens = usageData.inputTokens || 0;
      usage.outputTokens = usageData.outputTokens || 0;
      
      // Include cached tokens
      const cacheReadInputTokens = usageData.cacheReadInputTokens || 0;
      const cacheWriteInputTokens = usageData.cacheWriteInputTokens || 0;
      usage.inputTokens = usage.inputTokens + cacheReadInputTokens + cacheWriteInputTokens;
    }

    // Handle content block delta (text generation)
    if (data.contentBlockDelta) {
      const delta = data.contentBlockDelta as Record<string, unknown>;
      const deltaContent = delta.delta as Record<string, unknown>;
      
      if (deltaContent?.text) {
        const textChunk: OpenAIChatCompletionChunk = {
          id: completionId,
          object: 'chat.completion.chunk',
          created,
          model,
          choices: [{
            index: 0,
            delta: { content: deltaContent.text as string },
            finish_reason: null,
          }],
        };
        res.write(`data: ${JSON.stringify(textChunk)}\n\n`);
      }

      // Handle reasoning content if present
      if (deltaContent?.reasoningContent) {
        const reasoningContent = deltaContent.reasoningContent as Record<string, unknown>;
        if (reasoningContent.text) {
          const reasoningChunk: OpenAIChatCompletionChunk = {
            id: completionId,
            object: 'chat.completion.chunk',
            created,
            model,
            choices: [{
              index: 0,
              delta: { content: reasoningContent.text as string },
              finish_reason: null,
            }],
          };
          res.write(`data: ${JSON.stringify(reasoningChunk)}\n\n`);
        }
      }
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
            } catch (e) {
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
   * Converts Python-style JSON (single quotes) to standard JSON (double quotes)
   * SAP AI Core may return single-quoted JSON in streaming responses
   */
  private convertPythonJsonToStandardJson(jsonStr: string): string {
    // Replace single quotes with double quotes, but be careful about escaped quotes
    // This is a simple conversion that works for most cases
    let result = '';
    let inString = false;
    let stringChar = '';
    
    for (let i = 0; i < jsonStr.length; i++) {
      const char = jsonStr[i];
      const prevChar = i > 0 ? jsonStr[i - 1] : '';
      
      if (!inString) {
        if (char === "'" || char === '"') {
          inString = true;
          stringChar = char;
          result += '"'; // Always use double quotes
        } else {
          result += char;
        }
      } else {
        if (char === stringChar && prevChar !== '\\') {
          inString = false;
          result += '"'; // Always use double quotes
        } else if (char === '"' && stringChar === "'") {
          // Escape double quotes inside single-quoted strings
          result += '\\"';
        } else {
          result += char;
        }
      }
    }
    
    return result;
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

  /**
   * Handles errors
   */
  private handleError(error: unknown, res: Response): void {
    const axiosError = error as { 
      response?: { status?: number; data?: unknown; headers?: Record<string, string> }; 
      message?: string;
      config?: { url?: string; method?: string };
    };

    // Log detailed error information
    logger.error('Anthropic handler error:', {
      message: axiosError.message,
      status: axiosError.response?.status,
      url: axiosError.config?.url,
      method: axiosError.config?.method,
      responseData: axiosError.response?.data,
    });

    const statusCode = axiosError.response?.status || 500;
    
    // Extract error message from various response formats
    let errorMessage = 'Internal server error';
    const responseData = axiosError.response?.data;
    
    if (responseData) {
      if (typeof responseData === 'string') {
        errorMessage = responseData;
      } else if (typeof responseData === 'object') {
        const data = responseData as Record<string, unknown>;
        // Handle SAP AI Core error format: { errors: { message: string } }
        if (data.errors && typeof data.errors === 'object') {
          const errors = data.errors as Record<string, unknown>;
          errorMessage = (errors.message as string) || JSON.stringify(data.errors);
        } 
        // Handle standard error format: { error: { message: string } }
        else if (data.error && typeof data.error === 'object') {
          const err = data.error as Record<string, unknown>;
          errorMessage = (err.message as string) || JSON.stringify(data.error);
        }
        // Handle message field directly
        else if (data.message) {
          errorMessage = data.message as string;
        }
        // Fallback to stringify
        else {
          errorMessage = JSON.stringify(responseData);
        }
      }
    } else if (axiosError.message) {
      errorMessage = axiosError.message;
    }

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
