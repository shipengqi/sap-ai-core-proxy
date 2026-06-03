import { Response } from 'express';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
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
} from '../../../types/anthropic';
import {
  handleAnthropicError,
  extractSystemPrompt,
  contentBlockToText,
  assembleConversePayload,
  orchestrateStream,
} from '../../../utils';

/**
 * Handles Claude 3.5+ models via SAP AI Core Converse API.
 * Used by ClaudeAnthropicProvider when the requested model supports Converse.
 */
export class ConverseAnthropicProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handle(req: AnthropicMessagesRequest, sapModelName: string, res: Response): Promise<void> {
    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(sapModelName);
      const payload = this.buildConversePayload(req);

      if (req.stream) {
        await this.handleStreamResponse(
          `/v2/inference/deployments/${deploymentId}/converse-stream`,
          payload, res, req.model,
        );
      } else {
        await this.handleNonStreamResponse(
          `/v2/inference/deployments/${deploymentId}/converse`,
          payload, res, req.model,
        );
      }
    } catch (error: unknown) {
      handleAnthropicError(error, res);
    }
  }

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
                if (rc.type === 'text') resultContent.push({ text: rc.text });
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
        }
      }

      if (content.length > 0) {
        converseMessages.push({ role: msg.role, content });
      }
    }

    return converseMessages;
  }

  private convertTools(tools: AnthropicTool[], toolChoice?: AnthropicToolChoice): Record<string, unknown> {
    const converseTools = tools.map(tool => ({
      toolSpec: {
        name: tool.name,
        description: tool.description || '',
        inputSchema: { json: tool.input_schema },
      },
    }));

    const toolConfig: Record<string, unknown> = { tools: converseTools };

    if (toolChoice) {
      switch (toolChoice.type) {
        case 'auto':  toolConfig.toolChoice = { auto: {} }; break;
        case 'any':   toolConfig.toolChoice = { any: {} };  break;
        case 'tool':  toolConfig.toolChoice = { tool: { name: toolChoice.name } }; break;
        case 'none':  break; // Converse has no 'none' equivalent
      }
    }

    return toolConfig;
  }

  private buildConversePayload(req: AnthropicMessagesRequest): Record<string, unknown> {
    const systemPrompt = extractSystemPrompt(req.system);
    const messages = this.convertMessagesToConverse(req.messages);
    return assembleConversePayload({
      maxTokens: req.max_tokens,
      temperature: req.temperature ?? 0.0,
      messages,
      system: systemPrompt || undefined,
      topP: req.top_p,
      stopSequences: req.stop_sequences,
      toolConfig: req.tools?.length ? this.convertTools(req.tools, req.tool_choice) : undefined,
    });
  }

  private mapStopReason(
    converseStopReason: string | undefined
  ): 'end_turn' | 'max_tokens' | 'stop_sequence' | 'tool_use' | null {
    switch (converseStopReason) {
      case 'end_turn':      return 'end_turn';
      case 'max_tokens':    return 'max_tokens';
      case 'stop_sequence': return 'stop_sequence';
      case 'tool_use':      return 'tool_use';
      default:              return 'end_turn';
    }
  }

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

  private async handleNonStreamResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    originalModel: string
  ): Promise<void> {
    const response = await this.client.post(path, payload);
    const data = response.data;

    const output = data.output?.message;
    const converseContent = (output?.content || []) as Array<Record<string, unknown>>;
    const content = this.convertConverseContentToAnthropic(converseContent);
    const inputTokens =
      (data.usage?.inputTokens || 0) +
      (data.usage?.cacheReadInputTokens || 0) +
      (data.usage?.cacheWriteInputTokens || 0);
    const outputTokens = data.usage?.outputTokens || 0;

    const anthropicResponse: AnthropicMessagesResponse = {
      id: `msg_${uuidv4().replace(/-/g, '').slice(0, 24)}`,
      type: 'message',
      role: 'assistant',
      content,
      model: originalModel,
      stop_reason: this.mapStopReason(data.stopReason),
      stop_sequence: null,
      usage: { input_tokens: inputTokens, output_tokens: outputTokens },
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
      apiFormat: 'converse',
      responseFormat: 'anthropic',
      model: originalModel,
      completionId: messageId,
    }, res);
  }
}

export { extractSystemPrompt, contentBlockToText } from '../../../utils';
