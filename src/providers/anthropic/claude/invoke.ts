import { Response } from 'express';
import axios from 'axios';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import {
  AnthropicMessagesRequest,
  AnthropicMessagesResponse,
  AnthropicContentBlock,
  AnthropicTextContent,
} from '../../../types/anthropic';
import {
  setSSEHeaders,
  sendSSEEvent,
  handleAnthropicError,
} from '../../../utils';
import { logger } from '../../../logger';

function extractSystemPrompt(system: string | Array<{ type: string; text: string }> | undefined): string {
  if (!system) return '';
  if (typeof system === 'string') return system;
  return system.filter(s => s.type === 'text').map(s => s.text).join('\n');
}

function contentBlockToText(content: string | AnthropicContentBlock[]): string {
  if (typeof content === 'string') return content;
  return content.filter(b => b.type === 'text').map(b => (b as AnthropicTextContent).text).join('');
}

/**
 * Handles Claude 3 models via SAP AI Core Invoke API.
 * Used by ClaudeAnthropicProvider when the requested model does not support Converse.
 */
export class InvokeAnthropicProvider {
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.authManager = authManager;
    this.deploymentManager = deploymentManager;
  }

  async handle(req: AnthropicMessagesRequest, sapModelName: string, res: Response): Promise<void> {
    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(sapModelName);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const payload = this.buildInvokePayload(req);

      if (req.stream) {
        await this.handleStreamResponse(
          `${baseUrl}/v2/inference/deployments/${deploymentId}/invoke-with-response-stream`,
          headers, payload, res, req.model,
        );
      } else {
        await this.handleNonStreamResponse(
          `${baseUrl}/v2/inference/deployments/${deploymentId}/invoke`,
          headers, payload, res, req.model,
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
    url: string,
    headers: Record<string, string>,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const response = await axios.post(url, payload, { headers });
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
}
