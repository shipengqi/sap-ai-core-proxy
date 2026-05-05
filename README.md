# SAP AI Core LLM Proxy

A TypeScript proxy server that provides **OpenAI-compatible** and **Anthropic-native** API endpoints for SAP AI Core's LLM deployments. Use any OpenAI SDK, Anthropic SDK, or Claude Code to interact with models deployed on SAP AI Core.

## Features

- **Dual API surfaces**: OpenAI (`/openai`) and Anthropic (`/anthropic`)
- **Multi-model support**: OpenAI GPT, Anthropic Claude, Google Gemini, Meta Llama, Mistral, and Perplexity models
- **Streaming support**: Full Server-Sent Events (SSE) streaming for real-time responses
- **Automatic authentication**: OAuth token management with automatic refresh
- **Deployment discovery**: Automatically discovers running model deployments from SAP AI Core
- **Claude Code support**: Native Anthropic Messages API for Claude Code CLI and VSCode extension
- **Extensible architecture**: Router-per-proxy-type design makes adding new API formats straightforward

## Supported Models

### OpenAI Models
- gpt-4o, gpt-4o-mini, gpt-4
- gpt-4.1, gpt-4.1-nano
- gpt-5, gpt-5-nano, gpt-5-mini
- o1, o3-mini, o3, o4-mini

### Anthropic Models (Claude)
- anthropic--claude-4.6-sonnet, anthropic--claude-4.6-opus, anthropic--claude-4.6-haiku
- anthropic--claude-4.5-sonnet, anthropic--claude-4.5-opus, anthropic--claude-4.5-haiku
- anthropic--claude-4-sonnet, anthropic--claude-4-opus
- anthropic--claude-3.7-sonnet, anthropic--claude-3.5-sonnet
- anthropic--claude-3-opus, anthropic--claude-3-sonnet, anthropic--claude-3-haiku

### Google Gemini Models
- gemini-2.5-pro, gemini-2.5-flash
- gemini-1.5-pro, gemini-1.5-flash

### Perplexity Models
- sonar-pro, sonar

### Meta Models (Llama)
- meta--llama3-70b-instruct
- meta--llama3.1-70b-instruct

### Mistral Models
- mistralai--mixtral-8x7b-instruct-v01
- mistralai--mistral-large-instruct-2407

## Quick Start

### 1. Clone and Install

```bash
git clone https://github.com/shipengqi/sap-ai-core-proxy.git
cd sap-ai-core-proxy
npm install
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
LOG_LEVEL=info
```

### 3. Run the Proxy

Development mode:
```bash
npm run dev
```

Production mode:
```bash
npm run build
npm start
```

## API Endpoints

### OpenAI Surface (`/openai`)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/openai/v1/models` | GET | List available models |
| `/openai/v1/models/:modelId` | GET | Get specific model info |
| `/openai/v1/chat/completions` | POST | Chat completion |

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
| `/anthropic/api/*` | GET | Claude Code compat catch-all |

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

```typescript
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
```

Then run Claude Code normally:

```bash
claude
```

For the VSCode extension, set the API Base URL to `http://localhost:3001/anthropic` in the extension settings.

### Model Name Mapping

Claude Code sends standard Anthropic model names. The proxy automatically maps them to SAP AI Core model names:


| Claude Code model name | SAP AI Core model name |
|------------------------|------------------------|
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

You can also use SAP AI Core model names directly (e.g. `--model anthropic--claude-4.5-sonnet`).

## Project Structure

```
src/
├── index.ts                        # Entry point
├── app.ts                          # Express app setup, mounts routers
├── config.ts                       # Environment configuration
├── logger.ts                       # Logging utility
├── model-catalogue.ts              # Authoritative model registry with alias maps
├── routers/                        # Express Router per API surface
│   ├── index.ts                    # Router exports
│   ├── openai.ts                   # /openai/* routes
│   ├── anthropic.ts                # /anthropic/v1/* routes
│   ├── claude-code-compat.ts       # /anthropic/oauth/* and /anthropic/api/* stubs
│   ├── admin.ts                    # /admin/* routes
│   └── health.ts                   # / and /health routes
├── providers/                      # LLM provider implementations
│   ├── index.ts                    # Provider exports
│   ├── openai.ts                   # OpenAI models via OpenAI surface
│   ├── claude-openai.ts            # Claude via OpenAI surface (dispatcher)
│   ├── claude-openai-converse.ts   # Claude Converse API, Claude 3.5+ (OpenAI surface)
│   ├── claude-openai-invoke.ts     # Claude Invoke API, Claude 3 (OpenAI surface)
│   ├── gemini-openai.ts            # Gemini models via OpenAI surface
│   ├── claude-anthropic.ts         # Claude via Anthropic surface (dispatcher)
│   ├── claude-anthropic-converse.ts # Claude Converse API, Claude 3.5+ (Anthropic surface)
│   └── claude-anthropic-invoke.ts  # Claude Invoke API, Claude 3 (Anthropic surface)
├── utils/                          # Shared utilities
│   ├── json-parser.ts              # Python-style JSON conversion
│   ├── content-extractor.ts        # Message content extraction
│   ├── sse.ts                      # SSE header/event helpers
│   └── error-handler.ts            # Error extraction and formatting
├── sap-ai-core/                    # SAP AI Core integration
│   ├── auth.ts                     # OAuth token management
│   ├── deployments.ts              # Model deployment discovery
│   └── types.ts                    # SAP AI Core type definitions
└── types/                          # Shared TypeScript interfaces
    ├── openai.ts                   # OpenAI API types
    ├── anthropic.ts                # Anthropic API types
    └── models.ts                   # Model/provider types
```

## How It Works

1. **Authentication**: The proxy authenticates with SAP AI Core using OAuth 2.0 client credentials flow
2. **Deployment Discovery**: On startup (and periodically), it fetches available model deployments
3. **Request Routing**: Incoming requests are routed by URL prefix to the appropriate proxy mode
4. **Request Translation**: Requests are translated to the appropriate format for each model provider
5. **Response Translation**: Responses from SAP AI Core are converted back to the client's expected format
6. **Streaming**: For streaming requests, SSE streams are properly forwarded and formatted

## Adding a New API Surface

The router-per-surface architecture makes it easy to add new API formats:

1. Create a new router in `src/routers/` (e.g. `google.ts`)
2. Mount it in `src/app.ts` with `app.use('/google', createGoogleRouter(...))`
3. Optionally add a new provider in `src/providers/` if the backend format differs

## Model Routing (OpenAI Surface)

In the OpenAI surface, the proxy automatically routes requests to the appropriate provider based on the model name:

- **OpenAI models** (gpt-\*, o1, o3-\*): Standard OpenAI chat completions API
- **Anthropic models** (anthropic--claude-\*):
  - Newer models (claude-4.x, claude-3.7, claude-3.5): Converse Stream API with prompt caching
  - Older models (claude-3-\*): Invoke API
- **Gemini models** (gemini-\*): Gemini native generateContent API

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SAP_AI_CORE_CLIENT_ID` | Yes | - | OAuth client ID |
| `SAP_AI_CORE_CLIENT_SECRET` | Yes | - | OAuth client secret |
| `SAP_AI_CORE_TOKEN_URL` | Yes | - | OAuth token URL |
| `SAP_AI_CORE_BASE_URL` | Yes | - | SAP AI Core API base URL |
| `SAP_AI_CORE_RESOURCE_GROUP` | No | `default` | Resource group |
| `PORT` | No | `3001` | Server port |
| `LOG_LEVEL` | No | `info` | Log level (debug, info, warn, error) |

## License

MIT
