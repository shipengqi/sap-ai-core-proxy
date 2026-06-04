# SAP AI Core LLM Proxy

A Go proxy server that provides **OpenAI-compatible** and **Anthropic-native** API endpoints for SAP AI Core's LLM deployments. Use any OpenAI SDK, Anthropic SDK, or Claude Code to interact with models deployed on SAP AI Core.

## Features

- **Dual API surfaces**: OpenAI (`/openai`) and Anthropic (`/anthropic`)
- **Multi-model support**: OpenAI GPT, Anthropic Claude, Google Gemini, Meta Llama, Mistral, Perplexity, Cohere, and Amazon models
- **Streaming support**: Full Server-Sent Events (SSE) streaming for real-time responses
- **Automatic authentication**: OAuth token management with automatic refresh
- **Deployment discovery**: Automatically discovers running model deployments from SAP AI Core
- **Claude Code support**: Native Anthropic Messages API for Claude Code CLI and VSCode extension

## Supported Models

### OpenAI Models
- gpt-4o, gpt-4o-mini, gpt-4, gpt-4-32k
- gpt-4.1, gpt-4.1-nano
- gpt-5, gpt-5-nano, gpt-5-mini
- gpt-35-turbo, gpt-35-turbo-16k, gpt-35-turbo-0125
- o1, o3-mini, o3, o4-mini

### Anthropic Models (Claude)
- anthropic--claude-4.8-opus *(catalogue entry; deployment pending)*
- anthropic--claude-4.7-opus
- anthropic--claude-4.6-sonnet, anthropic--claude-4.6-opus, anthropic--claude-4.6-haiku
- anthropic--claude-4.5-sonnet, anthropic--claude-4.5-opus, anthropic--claude-4.5-haiku
- anthropic--claude-4-sonnet, anthropic--claude-4-opus
- anthropic--claude-3.7-sonnet, anthropic--claude-3.5-sonnet, anthropic--claude-3.5-haiku
- anthropic--claude-3-opus, anthropic--claude-3-sonnet, anthropic--claude-3-haiku

### Google Gemini Models
- gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite, gemini-2.5-flash-image
- gemini-2.0-flash, gemini-2.0-flash-lite
- gemini-1.5-pro, gemini-1.5-flash
- gemini-1.0-pro

### Perplexity Models
- sonar-pro, sonar, sonar-deep-research

### Meta Models (Llama)
- meta--llama3-70b-instruct
- meta--llama3.1-70b-instruct

### Mistral Models
- mistralai--mixtral-8x7b-instruct-v01
- mistralai--mistral-large-instruct
- mistralai--mistral-medium-instruct

### Cohere Models
- cohere--command-a-reasoning

### Amazon Models
- amazon--nova-lite

## Quick Start

### 1. Clone and Build

```bash
git clone https://github.com/shipengqi/sap-ai-core-proxy.git
cd sap-ai-core-proxy
go build -o sap-ai-core-proxy ./...
```

### 2. Configure Environment

Create a `.env` file with your SAP AI Core credentials:

```bash
cp .env.example .env
```

Edit `.env` with your credentials:

```env
SAP_AI_CORE_CLIENT_ID=your_client_id
SAP_AI_CORE_CLIENT_SECRET=your_client_secret
SAP_AI_CORE_TOKEN_URL=https://your-tenant.authentication.region.hana.ondemand.com
SAP_AI_CORE_BASE_URL=https://api.ai.your-region.aws.ml.hana.ondemand.com
SAP_AI_CORE_RESOURCE_GROUP=default
PORT=3001
```

### 3. Run the Proxy

```bash
./sap-ai-core-proxy
# or:
make run
```

## API Endpoints

### OpenAI Surface (`/openai`)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/openai/v1/models` | GET | List available models |
| `/openai/v1/models/:modelId` | GET | Get specific model info |
| `/openai/v1/chat/completions` | POST | Chat completion |
| `/openai/v1/embeddings` | POST | Text embeddings |
| `/openai/v1/responses` | POST | Responses API (create) |
| `/openai/v1/responses/:id` | GET | Responses API (retrieve) |
| `/openai/v1/responses/:id` | DELETE | Responses API (delete) |
| `/openai/v1/audio/transcriptions` | POST | Audio transcription (Whisper) |

### Anthropic Surface (`/anthropic`)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/anthropic` | HEAD/GET | Connectivity probe / provider status |
| `/anthropic/v1/models` | GET | List available models |
| `/anthropic/v1/messages` | POST | Anthropic Messages API |
| `/anthropic/v1/messages/count_tokens` | POST | Token counting |
| `/anthropic/oauth/token` | POST | Claude Code auth stub |
| `/anthropic/api/auth/me` | GET | Claude Code user info stub |
| `/anthropic/api/organizations` | GET | Claude Code org stub |
| `/anthropic/api/*` | ANY | Claude Code compat catch-all |

### General

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | API info |
| `/health` | GET | Health check |
| `/admin/refresh-deployments` | POST | Force refresh deployment cache |

## Usage

### Using with OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3001/openai/v1",
    api_key="not-needed"  # Authentication is handled by the proxy
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[
        {"role": "system", "content": "You are a helpful assistant."},
        {"role": "user", "content": "Hello, how are you?"}
    ]
)

print(response.choices[0].message.content)
```

### Using with OpenAI Node.js SDK

```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:3001/openai/v1',
  apiKey: 'not-needed',
});

const response = await client.chat.completions.create({
  model: 'anthropic--claude-4.5-sonnet',
  messages: [
    { role: 'user', content: 'Hello!' }
  ],
});

console.log(response.choices[0].message.content);
```

### Streaming Example

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3001/openai/v1",
    api_key="not-needed"
)

stream = client.chat.completions.create(
    model="gemini-2.5-flash",
    messages=[{"role": "user", "content": "Tell me a story"}],
    stream=True
)

for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

### Using with curl

```bash
# List available models
curl http://localhost:3001/openai/v1/models

# Chat completion
curl http://localhost:3001/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'

# Anthropic Messages API
curl http://localhost:3001/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: any-value" \
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## Claude Code Support

This proxy supports [Claude Code CLI](https://claude.ai/code) and the Claude Code VSCode extension via the Anthropic native proxy mode.

### Setup

Configure Claude Code to use the proxy:

```bash
export ANTHROPIC_BASE_URL=http://localhost:3001/anthropic
export ANTHROPIC_API_KEY=any-value   # The proxy handles SAP AI Core auth automatically
export ANTHROPIC_AUTH_TOKEN=any-value
```

Then run Claude Code normally:

```bash
claude
```

For the VSCode extension, set the API Base URL to `http://localhost:3001/anthropic` in the extension settings.

### Model Name Mapping

Claude Code sends standard Anthropic model names. The proxy automatically maps them to SAP AI Core model names. `-latest` aliases dynamically resolve to the newest deployed version in their family:

| Claude Code model name | SAP AI Core model name |
|------------------------|------------------------|
| `claude-opus-latest` | newest deployed opus (e.g. `anthropic--claude-4.7-opus`) |
| `claude-sonnet-latest` | newest deployed sonnet (e.g. `anthropic--claude-4.6-sonnet`) |
| `claude-haiku-latest` | newest deployed haiku (e.g. `anthropic--claude-4.5-haiku`) |
| `claude-opus-4-8` | `anthropic--claude-4.8-opus` |
| `claude-opus-4-7` | `anthropic--claude-4.7-opus` |
| `claude-sonnet-4-6` | `anthropic--claude-4.6-sonnet` |
| `claude-opus-4-6` | `anthropic--claude-4.6-opus` |
| `claude-haiku-4-6` | `anthropic--claude-4.6-haiku` |
| `claude-sonnet-4-5` | `anthropic--claude-4.5-sonnet` |
| `claude-opus-4-5` | `anthropic--claude-4.5-opus` |
| `claude-haiku-4-5` | `anthropic--claude-4.5-haiku` |
| `claude-sonnet-4` | `anthropic--claude-4-sonnet` |
| `claude-opus-4` | `anthropic--claude-4-opus` |
| `claude-3-7-sonnet-20250219` | `anthropic--claude-3.7-sonnet` |
| `claude-3-5-sonnet-20241022` | `anthropic--claude-3.5-sonnet` |
| `claude-3-5-haiku-20241022` | `anthropic--claude-3.5-haiku` |
| `claude-3-opus-20240229` | `anthropic--claude-3-opus` |
| `claude-3-sonnet-20240229` | `anthropic--claude-3-sonnet` |
| `claude-3-haiku-20240307` | `anthropic--claude-3-haiku` |

You can also use SAP AI Core model names directly (e.g. `--model anthropic--claude-4.6-sonnet`).

## Project Structure

```
main.go                       # Entry point: config, clients, server startup
internal/
├── config/
│   └── config.go             # Environment variable loading and validation
├── sapclient/                # SAP AI Core clients
│   ├── auth.go               # OAuth token management (singleflight caching)
│   ├── client.go             # HTTP client (Post, PostStream, Get, Delete, PostForm)
│   ├── deployments.go        # Deployment discovery and 60s caching
│   └── errors.go             # SapAPIError type
├── catalogue/
│   └── catalogue.go          # Model registry: metadata, alias maps, provider lookup
├── stream/                   # SSE streaming infrastructure
│   ├── types.go              # ConverseEvent, InvokeEvent, GeminiEvent discriminated types
│   ├── converse.go           # SAP Converse API stream parser
│   ├── invoke.go             # SAP Invoke API stream parser
│   ├── gemini.go             # Gemini API stream parser
│   └── orchestrator.go       # OrchestrateStream — drives full SSE lifecycle
├── provider/                 # LLM provider implementations
│   ├── dispatcher.go         # ClaudeDispatcher (Converse vs Invoke routing)
│   ├── anthropic/            # Anthropic-native surface
│   │   ├── claude.go         # Entry point: validates, maps model, dispatches
│   │   ├── converse.go       # Claude 3.5+ via SAP Converse API
│   │   └── invoke.go         # Claude 3 via SAP Invoke API
│   └── openai/               # OpenAI-compatible surface
│       └── providers.go      # GPT, Claude, Gemini, embeddings, responses, audio
├── router/
│   └── router.go             # RegisterAll() — mounts all Gin route groups
└── middleware/
    ├── cors.go               # Permissive CORS + OPTIONS 204
    └── logger.go             # Structured request logging (log/slog)
```

## How It Works

1. **Authentication**: The proxy authenticates with SAP AI Core using OAuth 2.0 client credentials flow. Tokens are cached with automatic refresh 60 seconds before expiry. Concurrent requests share one in-flight token fetch.
2. **Deployment Discovery**: On startup (and on demand via `/admin/refresh-deployments`), it fetches running model deployments from SAP AI Core and caches them for 60 seconds.
3. **Request Routing**: Incoming requests are routed by URL prefix (`/openai` or `/anthropic`) to the appropriate surface handler.
4. **Request Translation**: Requests are translated to the appropriate SAP AI Core format — Converse API for Claude 3.5+, Invoke API for Claude 3, native Gemini format for Gemini models.
5. **Response Translation**: Responses from SAP AI Core are converted back to the client's expected format (OpenAI or Anthropic).
6. **Streaming**: For streaming requests, SSE events from SAP AI Core are parsed by format-specific parsers and re-emitted in the client's protocol format.

## Model Routing (OpenAI Surface)

In the OpenAI surface, the proxy automatically routes requests to the appropriate backend based on the model name:

- **OpenAI models** (`gpt-*`, `o1`, `o3-*`, `o4-*`): Native OpenAI chat completions API
- **Anthropic models** (`anthropic--claude-*`):
  - Newer models (claude-4.x, claude-3.7, claude-3.5): SAP Converse API with prompt caching
  - Older models (claude-3-*): SAP Invoke API
- **Gemini models** (`gemini-*`): Gemini native generateContent API
- **Other models** (Mistral, Meta, Perplexity, Cohere, Amazon): Routed via OpenAI-compatible chat completions

## Adding a New API Surface

1. Add route registration in `internal/router/router.go` — add a new `register*` function and call it from `RegisterAll()`
2. Add a new provider package under `internal/provider/` if the backend format differs
3. Register any new model entries in `internal/catalogue/catalogue.go`

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SAP_AI_CORE_CLIENT_ID` | Yes | - | OAuth client ID |
| `SAP_AI_CORE_CLIENT_SECRET` | Yes | - | OAuth client secret |
| `SAP_AI_CORE_TOKEN_URL` | Yes | - | OAuth token URL |
| `SAP_AI_CORE_BASE_URL` | Yes | - | SAP AI Core API base URL |
| `SAP_AI_CORE_RESOURCE_GROUP` | No | `default` | Resource group |
| `PORT` | No | `3001` | Server port |

## License

MIT
