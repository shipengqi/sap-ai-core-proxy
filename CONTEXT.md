# sap-ai-core-proxy — Domain Glossary

## Surface

One of the two API protocols this proxy exposes to callers. A surface is named after its protocol, not after any model family it can serve.

- **OpenAI surface** — `/openai`; accepts OpenAI Chat Completions format (`POST /openai/v1/chat/completions`).
- **Anthropic surface** — `/anthropic`; accepts Anthropic Messages API format (`POST /anthropic/v1/messages`).

Callers choose a surface based on which SDK or protocol they use, independent of which model they request.

## Provider

A module that translates an incoming request from one surface's protocol into SAP AI Core's format for a specific model family, then translates the response back. Named `{model-family}-{surface}` (e.g., `claude-openai` = Claude models accessed via the OpenAI surface).

When model family and surface share the same name (OpenAI models via the OpenAI surface), the suffix is omitted: `openai`.

## Claude

The model-family name used in provider naming to refer to Anthropic's Claude models. Kept distinct from `anthropic`, which refers to the Anthropic surface (protocol). This avoids the ambiguity of `anthropic-anthropic` and makes both naming dimensions explicit.

| Provider file | Model family | Surface |
|---|---|---|
| `openai.ts` | openai | openai |
| `claude-openai.ts` | claude | openai |
| `gemini-openai.ts` | gemini | openai |
| `claude-anthropic.ts` | claude | anthropic |
