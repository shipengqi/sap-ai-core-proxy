import { Response } from 'express';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import {
  AnthropicMessagesRequest,
  AnthropicMessagesResponse,
} from '../../../types/anthropic';
import {
  setSSEHeaders,
  sendSSEEvent,
  handleAnthropicError,
  extractSystemPrompt,
  contentBlockToText,
  endStreamOnError,
  parseInvokeStream,
  drainErrorBody,
  parseErrorMessage,
  sendAnthropicError,
} from '../../../utils';
import { logger } from '../../../logger';

/**
 * Handles Claude 3 models via SAP AI Core Invoke API.
 * Used by ClaudeAnthropicProvider when the requested model does not support Converse.
 */
export class InvokeAnthropicProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handle(req: AnthropicMessagesRequest, sapModelName: string, res: Response): Promise<void> {
    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(sapModelName);
      const payload = this.buildInvokePayload(req);

      if (req.stream) {
        await this.handleStreamResponse(
          `/v2/inference/deployments/${deploymentId}/invoke-with-response-stream`,
          payload, res, req.model,
        );
      } else {
        await this.handleNonStreamResponse(
          `/v2/inference/deployments/${deploymentId}/invoke`,
          payload, res, req.model,
        );
      }
    } catch (error: unknown) {
      handleAnthropicError(error, res);
    }
  }

  private buildInvokePayload(req: AnthropicMessagesRequest): Record<string, unknown> {
    const systemPrompt = extractSystemPrompt(req.system);
    const anthropicMessages = req.messages.map(msg => ({
      role: msg.role,
      content: contentBlockToText(msg.content),
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

  private async handleNonStreamResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const response = await this.client.post(path, payload);
    const data = response.data;

    // Invoke API returns native Anthropic format — pass through with model name fix
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

  private async handleStreamResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const messageId = `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`;

    try {
      const response = await this.client.postStream(path, payload);

      if (response.status >= 400) {
        const body = await drainErrorBody(response.data);
        sendAnthropicError(res, response.status, parseErrorMessage(body));
        return;
      }

      setSSEHeaders(res);

      let inputTokens = 0;
      let outputTokens = 0;
      let blockStarted = false;

      for await (const event of parseInvokeStream(response.data)) {
        switch (event.type) {
          case 'messageStart': {
            inputTokens = event.inputTokens;
            this.sendAnthropicEvent(res, 'message_start', {
              type: 'message_start',
              message: {
                id: event.messageId || messageId,
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
          case 'blockStart': {
            blockStarted = true;
            this.sendAnthropicEvent(res, 'content_block_start', {
              type: 'content_block_start',
              index: event.index,
              content_block: event.contentBlock,
            });
            break;
          }
          case 'blockDelta': {
            this.sendAnthropicEvent(res, 'content_block_delta', {
              type: 'content_block_delta',
              index: event.index,
              delta: event.delta,
            });
            break;
          }
          case 'blockStop': {
            this.sendAnthropicEvent(res, 'content_block_stop', {
              type: 'content_block_stop',
              index: event.index,
            });
            break;
          }
          case 'messageDelta': {
            outputTokens = event.outputTokens;
            this.sendAnthropicEvent(res, 'message_delta', {
              type: 'message_delta',
              delta: { stop_reason: event.stopReason, stop_sequence: event.stopSequence },
              usage: { output_tokens: outputTokens },
            });
            break;
          }
          case 'messageStop': {
            this.sendAnthropicEvent(res, 'message_stop', { type: 'message_stop' });
            break;
          }
        }
      }

      if (!blockStarted) {
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
}
