# ADR-0001: Surface and provider naming convention

**Status**: Accepted

## Context

The proxy exposes two API surfaces (OpenAI and Anthropic protocols) and four providers that translate requests for different model families. Two naming problems arose:

1. The OpenAI router was named `openai-compatible` while the Anthropic router was just `anthropic` — asymmetric and the `-compatible` suffix encodes an implementation detail rather than the caller's perspective.
2. Provider files used `anthropic` for both the model family (Claude models) and the protocol surface, making `anthropic-openai` ambiguous ("Anthropic + OpenAI hybrid?" or "Anthropic model via OpenAI protocol?") and `anthropic-native` vague ("native relative to what?").

## Decision

**Surfaces** are named after their protocol only, without qualifiers: `openai` and `anthropic`. Router filenames and mount paths follow the surface name directly.

**Providers** follow the `{model-family}-{surface}` pattern. The model family for Anthropic's Claude models is `claude` — not `anthropic` — to keep the model-family dimension and the surface dimension orthogonal. When model family and surface share the same name, the suffix is omitted (`openai`).

| Before | After |
|---|---|
| `routers/openai-compatible.ts` → `/openai-compatible` | `routers/openai.ts` → `/openai` |
| `providers/anthropic-openai.ts` (`AnthropicOpenAIProvider`) | `providers/claude-openai.ts` (`ClaudeOpenAIProvider`) |
| `providers/anthropic-native.ts` (`AnthropicNativeProvider`) | `providers/claude-anthropic.ts` (`ClaudeAnthropicProvider`) |

## Consequences

Future providers are named `{model-family}-{surface}` without exception. Adding a Gemini provider for the Anthropic surface would be `gemini-anthropic.ts` / `GeminiAnthropicProvider`. The distinction between `claude` (model family) and `anthropic` (surface/protocol) must be maintained — do not use `anthropic` as a model-family name in provider code.
