import { Response } from 'express';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import {
  OpenAIChatCompletionRequest,
  OpenAIChatCompletionResponse,
  OpenAIMessage
} from '../../../types/openai';
import { extractTextContent, extractErrorDetails, sendOpenAIError, orchestrateStream } from '../../../utils';
import { SapApiError } from '../../../sap-ai-core/client';
import * as catalogue from '../../../model-catalogue';
import { logger } from '../../../logger';

/**
 * Handles Gemini model requests via SAP AI Core
 * Converts OpenAI format to Gemini format and back
 */
export class GeminiProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handleChatCompletion(req: OpenAIChatCompletionRequest, res: Response): Promise<void> {
    const { model, messages, stream = false } = req;

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const payload = this.buildPayload(req, messages);
      const basePath = `/v2/inference/deployments/${deploymentId}/models/${model}`;

      logger.debug(`Gemini request: model=${model}, stream=${stream}, messages=${messages.length}`);

      if (stream) {
        await this.handleStreamingResponse(`${basePath}:streamGenerateContent`, payload, res, model);
      } else {
        await this.handleNonStreamingResponse(`${basePath}:generateContent`, payload, res, model);
      }
    } catch (error: unknown) {
      if (error instanceof SapApiError) {
        sendOpenAIError(res, error.statusCode, error.message);
      } else {
        this.handleError(error, res);
      }
    }
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
      const textContent = extractTextContent(msg.content as string | null | Array<{ type: string; text?: string }>);

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
    const modelInfo = catalogue.getModelInfo(req.model);
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

  private async handleNonStreamingResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const response = await this.client.post(path, payload);

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

  private async handleStreamingResponse(
    path: string,
    payload: Record<string, unknown>,
    res: Response,
    model: string
  ): Promise<void> {
    const completionId = `chatcmpl-${uuidv4()}`;
    const response = await this.client.postStream(path, payload);
    await orchestrateStream(response, {
      apiFormat: 'gemini',
      responseFormat: 'openai',
      model,
      completionId,
    }, res);
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
    const { statusCode, message } = extractErrorDetails(error);
    const type = statusCode === 429 ? 'rate_limit_error' : 'api_error';
    const errorMessage = statusCode === 429
      ? `Rate limit exceeded: ${message}. Please wait and try again later.`
      : message;
    sendOpenAIError(res, statusCode, errorMessage, type);
  }
}
