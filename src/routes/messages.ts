import { AnthropicNativeProvider } from '../providers/anthropic-native';

/**
 * Creates route handlers for POST /v1/messages and POST /v1/messages/count_tokens
 */
export function createMessagesHandlers(anthropicNativeProvider: AnthropicNativeProvider) {
  return {
    handleMessages: anthropicNativeProvider.handleMessages.bind(anthropicNativeProvider),
    handleCountTokens: anthropicNativeProvider.handleCountTokens.bind(anthropicNativeProvider),
  };
}
