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
  handleAnthropicError,
  extractSystemPrompt,
  contentBlockToText,
  endStreamOnError,
  orchestrateStream,
} from '../../../utils';

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

  private async handleStreamResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const messageId = `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`;
    const response = await this.client.postStream(path, payload);
    await orchestrateStream(response, {
      apiFormat: 'invoke',
      responseFormat: 'anthropic',
      model: originalModel,
      completionId: messageId,
    }, res);
  }
}
