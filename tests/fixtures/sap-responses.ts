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

// SAP AI Core response for embeddings endpoint
export const SAP_EMBEDDINGS_RESPONSE = {
  object: 'list',
  data: [{
    object: 'embedding',
    index: 0,
    embedding: [0.1, 0.2, 0.3],
  }],
  model: 'text-embedding-ada-002',
  usage: { prompt_tokens: 5, total_tokens: 5 },
};

// SAP AI Core response for Responses API
export const SAP_RESPONSES_RESPONSE = {
  id: 'resp-sap-001',
  object: 'response',
  created_at: 1700000000,
  model: 'gpt-4o',
  output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Hello from Responses API!' }] }],
  usage: { input_tokens: 10, output_tokens: 8, total_tokens: 18 },
};

// SAP AI Core response for audio transcription endpoint
export const SAP_AUDIO_RESPONSE = {
  text: 'Hello from Whisper!',
};
