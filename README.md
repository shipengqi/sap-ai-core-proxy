# SAP AI Core LLM Proxy

A TypeScript proxy server that provides an OpenAI-compatible API for SAP AI Core's LLM deployments. This allows you to use any OpenAI SDK or compatible client to interact with models deployed on SAP AI Core.

## Features

- **OpenAI-compatible API**: Drop-in replacement for OpenAI API endpoints
- **Multi-model support**: OpenAI GPT, Anthropic Claude, Google Gemini, Meta Llama, Mistral, and Perplexity models
- **Streaming support**: Full Server-Sent Events (SSE) streaming for real-time responses
- **Automatic authentication**: OAuth token management with automatic refresh
- **Deployment discovery**: Automatically discovers running model deployments from SAP AI Core
- **Converse Stream API**: Support for newer Claude models using the Converse Stream endpoint
- **Gemini native API**: Native support for Gemini models with proper format conversion

## Supported Models

### OpenAI Models
- gpt-4o, gpt-4o-mini, gpt-4
- gpt-4.1, gpt-4.1-nano
- gpt-5, gpt-5-nano, gpt-5-mini
- o1, o3-mini, o3, o4-mini

### Anthropic Models (Claude)
- anthropic--claude-4.6-sonnet
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
PORT=3000
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

## Usage

### Using with OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3000/v1",
    api_key="not-needed"  # Authentication is handled by the proxy
)

# Chat completion
response = client.chat.completions.create(
    model="gpt-4o",  # Use any deployed model name
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
  baseURL: 'http://localhost:3000/v1',
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
    base_url="http://localhost:3000/v1",
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
curl http://localhost:3000/v1/models

# Chat completion
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'

# Streaming chat completion
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/v1/models` | GET | List available models |
| `/v1/models/:modelId` | GET | Get specific model info |
| `/v1/chat/completions` | POST | Chat completion |
| `/admin/refresh-deployments` | POST | Force refresh deployment cache |

## Project Structure

```
src/
├── index.ts              # Main entry point and Express server
├── auth.ts               # OAuth authentication manager
├── deployments.ts        # Deployment discovery and caching
├── logger.ts             # Logging utility
├── types.ts              # TypeScript type definitions
└── handlers/
    ├── openai.ts         # OpenAI-compatible model handler
    ├── anthropic.ts      # Anthropic/Claude model handler
    └── gemini.ts         # Google Gemini model handler
```

## How It Works

1. **Authentication**: The proxy authenticates with SAP AI Core using OAuth 2.0 client credentials flow
2. **Deployment Discovery**: On startup (and periodically), it fetches available model deployments
3. **Request Translation**: Incoming OpenAI-format requests are translated to the appropriate format for each model provider
4. **Response Translation**: Responses from SAP AI Core are converted back to OpenAI format
5. **Streaming**: For streaming requests, SSE streams are properly forwarded and formatted

## Model Routing

The proxy automatically routes requests to the appropriate handler based on the model name:

- **OpenAI models** (gpt-*, o1, o3-*): Use the standard OpenAI chat completions API
- **Anthropic models** (anthropic--claude-*): 
  - Newer models (claude-4.x, claude-3.7): Use Converse Stream API with caching
  - Older models (claude-3.x): Use Invoke API
- **Gemini models** (gemini-*): Use Gemini's native generateContent API

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SAP_AI_CORE_CLIENT_ID` | Yes | - | OAuth client ID |
| `SAP_AI_CORE_CLIENT_SECRET` | Yes | - | OAuth client secret |
| `SAP_AI_CORE_TOKEN_URL` | Yes | - | OAuth token URL |
| `SAP_AI_CORE_BASE_URL` | Yes | - | SAP AI Core API base URL |
| `SAP_AI_CORE_RESOURCE_GROUP` | No | `default` | Resource group |
| `PORT` | No | `3000` | Server port |
| `LOG_LEVEL` | No | `info` | Log level (debug, info, warn, error) |

## License

MIT