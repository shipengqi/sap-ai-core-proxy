// SAP AI Core response for OpenAI models — OpenAI chat completions format, passed through as-is
export const SAP_OPENAI_RESPONSE = {
  id: 'chatcmpl-sap-001',
  object: 'chat.completion',
  created: 1700000000,
  model: 'gpt-4o',
  choices: [{
    index: 0,
    message: { role: 'assistant', content: 'Hello from GPT-4o!' },
    finish_reason: 'stop',
  }],
  usage: { prompt_tokens: 10, completion_tokens: 8, total_tokens: 18 },
};

// SAP AI Core response for Claude models via Converse API
export const SAP_CONVERSE_RESPONSE = {
  output: {
    message: {
      role: 'assistant',
      content: [{ text: 'Hello from Claude!' }],
    },
  },
  stopReason: 'end_turn',
  usage: { inputTokens: 10, outputTokens: 6, cacheReadInputTokens: 0, cacheWriteInputTokens: 0 },
};

// SAP AI Core response for Gemini models — Gemini generateContent format
export const SAP_GEMINI_RESPONSE = {
  candidates: [{
    content: {
      parts: [{ text: 'Hello from Gemini!' }],
      role: 'model',
    },
    finishReason: 'STOP',
    index: 0,
  }],
  usageMetadata: {
    promptTokenCount: 10,
    candidatesTokenCount: 8,
    totalTokenCount: 18,
  },
};
