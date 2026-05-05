import { Request, Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../sap-ai-core/auth';
import { DeploymentManager } from '../sap-ai-core/deployments';
import {
  AnthropicMessagesRequest,
  AnthropicMessagesResponse,
  AnthropicContentBlock,
  AnthropicTextContent,
  AnthropicToolUseContent,
  AnthropicToolResultContent,
  AnthropicMessage,
  AnthropicTool,
  AnthropicToolChoice,
  AnthropicCountTokensResponse,
} from '../types/anthropic';
import {
  setSSEHeaders,
  sendSSEEvent,
  extractErrorDetails,
  sendAnthropicError,
  parseConverseStream,
  drainErrorBody,
  parseErrorMessage,
  applyPromptCaching,
} from '../utils';
import * as catalogue from '../model-catalogue';
import { logger } from '../logger';

/**
 * Handles native Anthropic Messages API requests (/v1/messages).
 * Enables Claude Code CLI and Claude Code VSCode extension to work with SAP AI Core.
 */
export class ClaudeAnthropicProvider {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  /**
   * Extracts the system prompt text from either string or array format
   */
  private extractSystemPrompt(system: string | Array<{ type: string; text: string }> | undefined): string {
    if (!system) return '';
    if (typeof system === 'string') return system;
    return system
      .filter(s => s.type === 'text')
      .map(s => s.text)
      .join('\n');
  }

  /**
   * Converts Anthropic Messages API content block to text string
   */
  private contentBlockToText(content: string | AnthropicContentBlock[]): string {
    if (typeof content === 'string') return content;
    return content
      .filter(b => b.type === 'text')
      .map(b => (b as AnthropicTextContent).text)
      .join('');
  }

  /**
   * Converts Anthropic Messages API messages to SAP AI Core Converse format.
   * Handles text, tool_use, and tool_result content blocks.
   */
  private convertMessagesToConverse(
    messages: AnthropicMessage[]
  ): Array<{ role: 'user' | 'assistant'; content: unknown[] }> {
    const converseMessages: Array<{ role: 'user' | 'assistant'; content: unknown[] }> = [];

    for (const msg of messages) {
      const content: unknown[] = [];

      if (typeof msg.content === 'string') {
        content.push({ text: msg.content });
      } else {
        for (const block of msg.content) {
          if (block.type === 'text') {
            content.push({ text: (block as AnthropicTextContent).text });
          } else if (block.type === 'tool_use') {
            const toolUse = block as AnthropicToolUseContent;
            content.push({
              toolUse: {
                toolUseId: toolUse.id,
                name: toolUse.name,
                input: toolUse.input,
              },
            });
          } else if (block.type === 'tool_result') {
            const toolResult = block as AnthropicToolResultContent;
            const resultContent: unknown[] = [];

            if (typeof toolResult.content === 'string') {
              resultContent.push({ text: toolResult.content });
            } else if (Array.isArray(toolResult.content)) {
              for (const rc of toolResult.content) {
                if (rc.type === 'text') {
                  resultContent.push({ text: rc.text });
                }
              }
            }

            content.push({
              toolResult: {
                toolUseId: toolResult.tool_use_id,
                content: resultContent,
                status: toolResult.is_error ? 'error' : 'success',
              },
            });
          }
          // image blocks: skip for now (SAP AI Core may not support)
        }
      }

      if (content.length > 0) {
        converseMessages.push({ role: msg.role, content });
      }
    }

    return converseMessages;
  }

  /**
   * Converts Anthropic tool definitions to SAP AI Core Converse toolConfig format
   */
  private convertTools(tools: AnthropicTool[], toolChoice?: AnthropicToolChoice): Record<string, unknown> {
    const converseTools = tools.map(tool => ({
      toolSpec: {
        name: tool.name,
        description: tool.description || '',
        inputSchema: {
          json: tool.input_schema,
        },
      },
    }));

    const toolConfig: Record<string, unknown> = { tools: converseTools };

    if (toolChoice) {
      switch (toolChoice.type) {
        case 'auto':
          toolConfig.toolChoice = { auto: {} };
          break;
        case 'any':
          toolConfig.toolChoice = { any: {} };
          break;
        case 'tool':
          toolConfig.toolChoice = { tool: { name: toolChoice.name } };
          break;
        case 'none':
          // Converse doesn't have a 'none' equivalent; omit toolChoice
          break;
      }
    }

    return toolConfig;
  }

  /**
   * Builds the SAP AI Core Converse API payload from an Anthropic Messages request
   */
  private buildConversePayload(req: AnthropicMessagesRequest): Record<string, unknown> {
    const systemPrompt = this.extractSystemPrompt(req.system);
    const messages = this.convertMessagesToConverse(req.messages);

    const payload: Record<string, unknown> = {
      inferenceConfig: {
        maxTokens: req.max_tokens,
        temperature: req.temperature ?? 0.0,
        ...(req.top_p !== undefined && { topP: req.top_p }),
        ...(req.stop_sequences?.length && { stopSequences: req.stop_sequences }),
      },
      messages: applyPromptCaching(messages),
    };

    if (systemPrompt) {
      payload.system = [
        { text: systemPrompt },
        { cachePoint: { type: 'default' } },
      ];
    }

    if (req.tools && req.tools.length > 0) {
      payload.toolConfig = this.convertTools(req.tools, req.tool_choice);
    }

    return payload;
  }

  /**
   * Converts SAP AI Core Converse stop reason to Anthropic stop reason
   */
  private mapStopReason(
    converseStopReason: string | undefined
  ): 'end_turn' | 'max_tokens' | 'stop_sequence' | 'tool_use' | null {
    switch (converseStopReason) {
      case 'end_turn':
        return 'end_turn';
      case 'max_tokens':
        return 'max_tokens';
      case 'stop_sequence':
        return 'stop_sequence';
      case 'tool_use':
        return 'tool_use';
      default:
        return 'end_turn';
    }
  }

  /**
   * Converts SAP AI Core Converse response content to Anthropic content blocks
   */
  private convertConverseContentToAnthropic(
    converseContent: Array<Record<string, unknown>>
  ): AnthropicContentBlock[] {
    const blocks: AnthropicContentBlock[] = [];

    for (const item of converseContent) {
      if (item.text !== undefined) {
        blocks.push({ type: 'text', text: item.text as string });
      } else if (item.toolUse) {
        const toolUse = item.toolUse as Record<string, unknown>;
        blocks.push({
          type: 'tool_use',
          id: (toolUse.toolUseId as string) || `toolu_${uuidv4().replace(/-/g, '').slice(0, 24)}`,
          name: toolUse.name as string,
          input: (toolUse.input as Record<string, unknown>) || {},
        });
      }
    }

    return blocks;
  }

  /**
   * Handles POST /v1/messages (non-streaming and streaming)
   */
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

    // Map model name to SAP AI Core name
    const sapModelName = catalogue.mapFromAnthropic(anthropicReq.model);
    logger.info(`Anthropic Messages API: model=${anthropicReq.model} → SAP model=${sapModelName}, stream=${anthropicReq.stream}`);

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(sapModelName);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();

      const useConverse = catalogue.usesConverseApi(sapModelName);
      const payload = this.buildConversePayload(anthropicReq);

      if (anthropicReq.stream) {
        if (!useConverse) {
          // Older models: use invoke-with-response-stream, convert Anthropic stream to Anthropic stream
          await this.handleInvokeStreamResponse(
            `${baseUrl}/v2/inference/deployments/${deploymentId}/invoke-with-response-stream`,
            headers,
            this.buildInvokePayload(anthropicReq),
            res,
            anthropicReq.model
          );
        } else {
          await this.handleConverseStreamResponse(
            `${baseUrl}/v2/inference/deployments/${deploymentId}/converse-stream`,
            headers,
            payload,
            res,
            anthropicReq.model
          );
        }
      } else {
        if (!useConverse) {
          await this.handleInvokeNonStreamResponse(
            `${baseUrl}/v2/inference/deployments/${deploymentId}/invoke`,
            headers,
            this.buildInvokePayload(anthropicReq),
            res,
            anthropicReq.model
          );
        } else {
          await this.handleConverseNonStreamResponse(
            `${baseUrl}/v2/inference/deployments/${deploymentId}/converse`,
            headers,
            payload,
            res,
            anthropicReq.model
          );
        }
      }
    } catch (error: unknown) {
      this.handleError(error, res);
    }
  }

  /**
   * Builds the invoke API payload from an Anthropic Messages request (older Claude models)
   */
  private buildInvokePayload(req: AnthropicMessagesRequest): Record<string, unknown> {
    const systemPrompt = this.extractSystemPrompt(req.system);
    const anthropicMessages = req.messages.map(msg => ({
      role: msg.role,
      content: this.contentBlockToText(msg.content),
    }));

    const payload: Record<string, unknown> = {
      anthropic_version: 'bedrock-2023-05-31',
      max_tokens: req.max_tokens,
      messages: anthropicMessages,
    };

    if (systemPrompt) payload.system = systemPrompt;
    if (req.temperature !== undefined) payload.temperature = req.temperature;
    if (req.top_p !== undefined) payload.top_p = req.top_p;
    if (req.stop_sequences?.length) payload.stop_sequences = req.stop_sequences;

    if (req.tools?.length) {
      payload.tools = req.tools.map(t => ({
        name: t.name,
        description: t.description,
        input_schema: t.input_schema,
      }));
    }

    return payload;
  }

  /**
   * Handles non-streaming response from Converse API, returns Anthropic format
   */
  private async handleConverseNonStreamResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });
    const data = response.data;

    const output = data.output?.message;
    const converseContent = (output?.content || []) as Array<Record<string, unknown>>;
    const content = this.convertConverseContentToAnthropic(converseContent);
    const inputTokens = (data.usage?.inputTokens || 0) + (data.usage?.cacheReadInputTokens || 0) + (data.usage?.cacheWriteInputTokens || 0);
    const outputTokens = data.usage?.outputTokens || 0;

    const anthropicResponse: AnthropicMessagesResponse = {
      id: `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`,
      type: 'message',
      role: 'assistant',
      content,
      model: originalModel,
      stop_reason: this.mapStopReason(data.stopReason),
      stop_sequence: null,
      usage: {
        input_tokens: inputTokens,
        output_tokens: outputTokens,
      },
    };

    res.json(anthropicResponse);
  }

  /**
   * Handles non-streaming response from Invoke API (older Claude models), returns Anthropic format
   */
  private async handleInvokeNonStreamResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });
    const data = response.data;

    // The invoke API returns native Anthropic format - pass through with model name fix
    const anthropicResponse: AnthropicMessagesResponse = {
      id: data.id || `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`,
      type: 'message',
      role: 'assistant',
      content: data.content || [],
      model: originalModel,
      stop_reason: data.stop_reason || 'end_turn',
      stop_sequence: data.stop_sequence || null,
      usage: {
        input_tokens: data.usage?.input_tokens || 0,
        output_tokens: data.usage?.output_tokens || 0,
      },
    };

    res.json(anthropicResponse);
  }

  private sendAnthropicEvent(res: Response, eventType: string, data: unknown): void {
    sendSSEEvent(res, eventType, data);
  }

  /**
   * Handles streaming response from Converse Stream API, returns Anthropic SSE format
   */
  private async handleConverseStreamResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const messageId = `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`;

    try {
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
        validateStatus: (status) => status < 500,
      });

      if (response.status >= 400) {
        const body = await drainErrorBody(response.data);
        const errorMessage = parseErrorMessage(body);
        res.status(response.status).json({
          type: 'error',
          error: { type: 'api_error', message: errorMessage },
        });
        return;
      }

      setSSEHeaders(res);

      let inputTokens = 0;
      let outputTokens = 0;
      let stopReason = 'end_turn';

      this.sendAnthropicEvent(res, 'message_start', {
        type: 'message_start',
        message: {
          id: messageId,
          type: 'message',
          role: 'assistant',
          content: [],
          model: originalModel,
          stop_reason: null,
          stop_sequence: null,
          usage: { input_tokens: 0, output_tokens: 0 },
        },
      });
      this.sendAnthropicEvent(res, 'ping', { type: 'ping' });

      for await (const event of parseConverseStream(response.data)) {
        switch (event.type) {
          case 'metadata':
            inputTokens = event.inputTokens || inputTokens;
            outputTokens = event.outputTokens || outputTokens;
            break;
          case 'textBlockStart':
            this.sendAnthropicEvent(res, 'content_block_start', {
              type: 'content_block_start',
              index: event.index,
              content_block: { type: 'text', text: '' },
            });
            break;
          case 'textDelta':
            this.sendAnthropicEvent(res, 'content_block_delta', {
              type: 'content_block_delta',
              index: event.index,
              delta: { type: 'text_delta', text: event.text },
            });
            break;
          case 'textBlockStop':
            this.sendAnthropicEvent(res, 'content_block_stop', {
              type: 'content_block_stop',
              index: event.index,
            });
            break;
          case 'toolBlockStart':
            this.sendAnthropicEvent(res, 'content_block_start', {
              type: 'content_block_start',
              index: event.index,
              content_block: { type: 'tool_use', id: event.id, name: event.name, input: {} },
            });
            break;
          case 'toolInputDelta':
            this.sendAnthropicEvent(res, 'content_block_delta', {
              type: 'content_block_delta',
              index: event.index,
              delta: { type: 'input_json_delta', partial_json: event.partial_json },
            });
            break;
          case 'toolBlockStop':
            this.sendAnthropicEvent(res, 'content_block_stop', {
              type: 'content_block_stop',
              index: event.index,
            });
            break;
          case 'messageStop':
            stopReason = event.stopReason;
            break;
        }
      }

      this.sendAnthropicEvent(res, 'message_delta', {
        type: 'message_delta',
        delta: { stop_reason: stopReason, stop_sequence: null },
        usage: { output_tokens: outputTokens },
      });
      this.sendAnthropicEvent(res, 'message_stop', { type: 'message_stop' });
      res.end();

    } catch (error: unknown) {
      const axiosError = error as { response?: { status?: number }; message?: string };
      logger.error('Converse stream request failed:', axiosError.message);

      if (!res.headersSent) {
        res.status(axiosError.response?.status || 500).json({
          type: 'error',
          error: { type: 'api_error', message: axiosError.message || 'Request failed' },
        });
      } else {
        res.end();
      }
    }
  }

  /**
   * Handles streaming response from Invoke API (older Claude models), returns Anthropic SSE format
   */
  private async handleInvokeStreamResponse(
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const messageId = `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`;

    try {
      const response = await axios.post(url, payload, {
        headers,
        responseType: 'stream',
        validateStatus: (status) => status < 500,
      });

      if (response.status >= 400) {
        let errorBody = '';
        for await (const chunk of response.data) {
          errorBody += chunk.toString('utf-8');
        }
        res.status(response.status).json({
          type: 'error',
          error: { type: 'api_error', message: errorBody || 'Request failed' },
        });
        return;
      }

      setSSEHeaders(res);

      let buffer = '';
      let inputTokens = 0;
      let outputTokens = 0;
      let blockIndex = 0;
      let blockStarted = false;

      response.data.on('data', (chunk: Buffer) => {
        buffer += chunk.toString('utf-8');
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.trim() === '' || !line.startsWith('data: ')) continue;

          try {
            const data = JSON.parse(line.slice(6)) as Record<string, unknown>;

            switch (data.type) {
              case 'message_start': {
                const msg = data.message as Record<string, unknown>;
                const usage = msg?.usage as Record<string, number> | undefined;
                inputTokens = usage?.input_tokens || 0;

                this.sendAnthropicEvent(res, 'message_start', {
                  type: 'message_start',
                  message: {
                    id: (msg?.id as string) || messageId,
                    type: 'message',
                    role: 'assistant',
                    content: [],
                    model: originalModel,
                    stop_reason: null,
                    stop_sequence: null,
                    usage: { input_tokens: inputTokens, output_tokens: 1 },
                  },
                });
                this.sendAnthropicEvent(res, 'ping', { type: 'ping' });
                break;
              }

              case 'content_block_start': {
                const cb = data.content_block as Record<string, unknown>;
                blockIndex = (data.index as number) || 0;
                blockStarted = true;
                this.sendAnthropicEvent(res, 'content_block_start', {
                  type: 'content_block_start',
                  index: blockIndex,
                  content_block: cb,
                });
                break;
              }

              case 'content_block_delta':
                // Pass through delta events directly
                res.write(`event: content_block_delta\ndata: ${JSON.stringify({ ...data, model: undefined })}\n\n`);
                break;

              case 'content_block_stop':
                this.sendAnthropicEvent(res, 'content_block_stop', {
                  type: 'content_block_stop',
                  index: (data.index as number) || blockIndex,
                });
                break;

              case 'message_delta': {
                const delta = data.delta as Record<string, unknown>;
                const deltaUsage = data.usage as Record<string, number> | undefined;
                outputTokens = deltaUsage?.output_tokens || 0;

                this.sendAnthropicEvent(res, 'message_delta', {
                  type: 'message_delta',
                  delta: { stop_reason: delta?.stop_reason || 'end_turn', stop_sequence: delta?.stop_sequence || null },
                  usage: { output_tokens: outputTokens },
                });
                break;
              }

              case 'message_stop':
                this.sendAnthropicEvent(res, 'message_stop', { type: 'message_stop' });
                break;
            }
          } catch {
            // Skip unparseable lines
          }
        }
      });

      response.data.on('end', () => {
        if (!blockStarted) {
          // Ensure we always send at least a minimal valid stream
          this.sendAnthropicEvent(res, 'message_start', {
            type: 'message_start',
            message: {
              id: messageId,
              type: 'message',
              role: 'assistant',
              content: [],
              model: originalModel,
              stop_reason: null,
              stop_sequence: null,
              usage: { input_tokens: inputTokens, output_tokens: 1 },
            },
          });
          this.sendAnthropicEvent(res, 'content_block_start', {
            type: 'content_block_start',
            index: 0,
            content_block: { type: 'text', text: '' },
          });
          this.sendAnthropicEvent(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
          this.sendAnthropicEvent(res, 'message_delta', {
            type: 'message_delta',
            delta: { stop_reason: 'end_turn', stop_sequence: null },
            usage: { output_tokens: outputTokens },
          });
          this.sendAnthropicEvent(res, 'message_stop', { type: 'message_stop' });
        }
        res.end();
      });

      response.data.on('error', (error: Error) => {
        logger.error('Invoke stream error (Anthropic Messages):', error.message);
        res.end();
      });

    } catch (error: unknown) {
      const axiosError = error as { response?: { status?: number }; message?: string };
      logger.error('Invoke stream request failed:', axiosError.message);

      if (!res.headersSent) {
        res.status(axiosError.response?.status || 500).json({
          type: 'error',
          error: { type: 'api_error', message: axiosError.message || 'Request failed' },
        });
      } else {
        res.end();
      }
    }
  }

  /**
   * Handles POST /v1/messages/count_tokens
   * Returns an estimate of input tokens for the given request.
   */
  async handleCountTokens(req: Request, res: Response): Promise<void> {
    const countReq = req.body as AnthropicMessagesRequest;

    // Simple approximation: ~4 characters per token
    let totalChars = 0;

    if (countReq.system) {
      const systemText = this.extractSystemPrompt(countReq.system);
      totalChars += systemText.length;
    }

    for (const msg of countReq.messages || []) {
      totalChars += this.contentBlockToText(msg.content).length;
    }

    if (countReq.tools) {
      totalChars += JSON.stringify(countReq.tools).length;
    }

    const estimatedTokens = Math.ceil(totalChars / 4);

    const response: AnthropicCountTokensResponse = {
      input_tokens: estimatedTokens,
    };

    res.json(response);
  }

  /**
   * Handles errors and sends appropriate Anthropic-format error responses
   */
  private handleError(error: unknown, res: Response): void {
    const { statusCode, message } = extractErrorDetails(error);
    sendAnthropicError(res, statusCode, message);
  }
}
