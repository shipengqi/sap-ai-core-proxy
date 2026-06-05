# sap-ai-core-proxy

A Go reverse proxy that exposes SAP AI Core LLM deployments through standard API surfaces:
**OpenAI**, **Anthropic**, **Gemini**, and **LiteLLM/Orchestration**.

## Features

- **4 API surfaces** — OpenAI-compatible, Anthropic-native, Gemini-native, SAP Orchestration (LiteLLM)
- **Streaming support** — SSE for OpenAI & Anthropic, raw chunked JSON for Gemini
- **Automatic auth** — OAuth 2.0 client-credentials token exchange with in-memory caching
- **Deployment discovery** — queries SAP AI Core for RUNNING deployments; 12-hour cache; 3-tier model matching (exact → fuzzy → family prefix fallback)
- **Model aliases** — translates standard model names (e.g. `claude-3-5-sonnet-latest`) to SAP AI Core deployment names automatically
- **Claude Code / VSCode / IDE compatible** — works with any client that speaks OpenAI or Anthropic APIs

## Quick Start

### 1. Build

```bash
git clone https://github.com/shipengqi/sap-ai-core-proxy
cd sap-ai-core-proxy
make build
```

### 2. Configure

Copy the example config and fill in your credentials:

```bash
mkdir -p ~/.aicoreproxy
cp config.json.example ~/.aicoreproxy/config.json
```

Edit `~/.aicoreproxy/config.json`:

```json
{
  "sap_ai_core": {
    "base_url": "https://your-tenant.hana.ondemand.com",
    "token_url": "https://your-tenant.authentication.eu10.hana.ondemand.com",
    "client_id": "your-client-id",
    "client_secret": "your-client-secret",
    "resource_group": "default"
  },
  "server": {
    "port": 3001,
    "log_level": "info"
  }
}
```

### 3. Run

```bash
./sap-ai-core-proxy
# or
make run
```

The proxy starts on `http://localhost:3001` (default).

## Configuration

### Config File

**Path:** `~/.aicoreproxy/config.json`

| Field | Required | Default | Description |
|---|---|---|---|
| `sap_ai_core.base_url` | Yes | — | SAP AI Core API base URL |
| `sap_ai_core.token_url` | Yes | — | OAuth 2.0 token endpoint base URL |
| `sap_ai_core.client_id` | Yes | — | OAuth client ID from service binding |
| `sap_ai_core.client_secret` | Yes | — | OAuth client secret |
| `sap_ai_core.resource_group` | No | `"default"` | SAP AI Core resource group |
| `server.port` | No | `3001` | Listen port |
| `server.log_level` | No | `"info"` | Log level: `debug`, `info`, `warn`, `error` |

### Environment Variable Overrides

All config values can be overridden by environment variables (higher priority than the JSON file):

| Env Var | Overrides |
|---|---|
| `SAP_AI_CORE_BASE_URL` | `sap_ai_core.base_url` |
| `SAP_AI_CORE_TOKEN_URL` | `sap_ai_core.token_url` |
| `SAP_AI_CORE_CLIENT_ID` | `sap_ai_core.client_id` |
| `SAP_AI_CORE_CLIENT_SECRET` | `sap_ai_core.client_secret` |
| `SAP_AI_CORE_RESOURCE_GROUP` | `sap_ai_core.resource_group` |
| `PORT` | `server.port` |
| `LOG_LEVEL` | `server.log_level` |

## API Endpoints

### OpenAI Surface

| Method | Path | Description |
|---|---|---|
| GET | `/openai/v1/models` | List all RUNNING deployments in OpenAI format |
| POST | `/openai/v1/chat/completions` | Chat completions (streaming supported) |
| POST | `/openai/v1/embeddings` | Embeddings |
| POST | `/openai/v1/responses` | Create a response (stateful) |
| GET | `/openai/v1/responses/{id}` | Get a response by ID |
| DELETE | `/openai/v1/responses/{id}` | Delete a response |

### Anthropic Surface

| Method | Path | Description |
|---|---|---|
| GET | `/anthropic/v1/models` | List all RUNNING deployments |
| POST | `/anthropic/v1/messages` | Messages API — native Anthropic format, streaming supported |

### Gemini Surface

| Method | Path | Description |
|---|---|---|
| GET | `/gemini/v1/models` | List all RUNNING deployments |
| POST | `/gemini/v1beta/models/{model}:{op}` | `generateContent` or `streamGenerateContent` |

### LiteLLM / SAP Orchestration Surface

| Method | Path | Description |
|---|---|---|
| GET | `/litellm/v1/models` | List all RUNNING deployments |
| GET | `/litellm/v1/model/info` | Model info via Orchestration |
| POST | `/litellm/v1/chat/completions` | Chat via SAP Orchestration |
| POST | `/litellm/v1/completions` | Completions via SAP Orchestration |
| POST | `/litellm/v1/embeddings` | Embeddings (direct deployment) |

## Model Routing & Fallback

### Deployment Discovery

On every request, the proxy resolves the upstream SAP AI Core deployment URL in three passes:

1. **Exact match** — `deployment.modelName == normalizedModel`
2. **Fuzzy substring match** — either name contains the other
3. **Family prefix fallback** — strips the version suffix and matches the family (e.g. `claude-opus-4.8` → `claude-opus` matches any `*claude-opus*` deployment)

A warning is logged when falling back to a non-exact match. The deployment list is cached for 12 hours.

### Anthropic Model Aliases

The proxy automatically translates standard Anthropic model names to SAP AI Core deployment names:

| User-Facing Name | SAP AI Core Deployment Name |
|---|---|
| `claude-opus-latest` | `anthropic--claude-4.7-opus` |
| `claude-opus-4.8` / `claude-opus-4-8` | `anthropic--claude-4.8-opus` |
| `claude-opus-4.5` / `claude-opus-4-5` | `anthropic--claude-4.5-opus` |
| `claude-sonnet-latest` | `anthropic--claude-4.5-sonnet` |
| `claude-sonnet-4.5` / `claude-sonnet-4-5` | `anthropic--claude-4.5-sonnet` |
| `claude-haiku-latest` | `anthropic--claude-4.5-haiku` |
| `claude-3-7-sonnet-latest` | `anthropic--claude-3.7-sonnet` |
| `claude-3-5-sonnet-latest` | `anthropic--claude-3.5-sonnet` |
| `claude-3-5-haiku-latest` | `anthropic--claude-3.5-haiku` |
| `claude-3-opus-latest` | `anthropic--claude-3-opus` |

## Streaming

| Surface | Wire Format |
|---|---|
| OpenAI | Server-Sent Events (`data: {...}\n\n`) — forwarded verbatim |
| Anthropic | SSE with `event:` lines; injected automatically if SAP omits them |
| Gemini | Raw chunked JSON bytes (not SSE) |

## Usage Examples

### OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3001/openai/v1",
    api_key="not-used",
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)
```

### Anthropic Python SDK

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:3001/anthropic",
    api_key="not-used",
)

message = client.messages.create(
    model="claude-3-5-sonnet-latest",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}],
)
print(message.content[0].text)
```

### Streaming (curl)

```bash
curl -X POST http://localhost:3001/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### Claude Code CLI

Add to `~/.claude/settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:3001/anthropic",
    "ANTHROPIC_AUTH_TOKEN": "any-value"
  }
}
```

## Docker

### Build

```bash
make build
docker build -t sap-ai-core-proxy:latest .
```

### Run

```bash
docker run -d \
  -p 3001:3001 \
  -v ~/.aicoreproxy:/root/.aicoreproxy:ro \
  sap-ai-core-proxy:latest
```

Or pass credentials via environment variables instead of mounting the config file:

```bash
docker run -d \
  -p 3001:3001 \
  -e SAP_AI_CORE_BASE_URL=https://... \
  -e SAP_AI_CORE_TOKEN_URL=https://... \
  -e SAP_AI_CORE_CLIENT_ID=... \
  -e SAP_AI_CORE_CLIENT_SECRET=... \
  sap-ai-core-proxy:latest
```

## Makefile Targets

| Target | Description |
|---|---|
| `make build` | Build the binary (`./sap-ai-core-proxy`) |
| `make run` | Run with `go run ./...` |
| `make test` | Run unit tests |
| `make test.integration` | Run integration tests (mock mode if no env vars set) |
| `make vet` | Run `go vet` |
| `make lint` | Run `golangci-lint` |
| `make docker.build` | Build Docker image (set `TAG=` to override) |
| `make clean` | Remove build artifacts |

## Project Structure

```
sap-ai-core-proxy/
├── main.go                          # Server startup, config wiring
├── internal/
│   ├── config/config.go             # Load ~/.aicoreproxy/config.json + env overrides
│   ├── sapclient/
│   │   ├── auth.go                  # OAuth2 client-credentials + singleflight token cache
│   │   ├── client.go                # Signed HTTP client (injects Bearer + AI-Resource-Group)
│   │   └── deployments.go           # Deployment discovery, 12h TTL cache, 3-tier matching
│   ├── catalogue/catalogue.go       # Model alias map + IsOpenAI/IsAnthropic/IsGemini
│   ├── stream/
│   │   ├── openai.go                # Pipe OpenAI SSE chunks
│   │   ├── anthropic.go             # Pipe Anthropic SSE, inject event: lines if missing
│   │   └── gemini.go                # Pipe Gemini raw chunked bytes
│   ├── handler/
│   │   ├── openai/                  # /openai/v1/* handlers
│   │   ├── anthropic/               # /anthropic/v1/* handlers
│   │   ├── gemini/                  # /gemini/v1/* handlers
│   │   └── litellm/                 # /litellm/v1/* handlers
│   ├── router/router.go             # Route registration
│   └── middleware/                  # CORS, structured JSON request logging
└── test/                            # Integration tests (mock + real SAP AI Core)
```

## How It Works

1. **Config load** — reads `~/.aicoreproxy/config.json`, applies env var overrides
2. **Token exchange** — on first request, POSTs client credentials to `token_url/oauth/token`; cached until 60s before expiry; concurrent requests deduplicated via singleflight
3. **Deployment resolution** — fetches all RUNNING deployments from SAP AI Core at startup; cached 12 hours; resolved via 3-tier lookup per request
4. **Request translation** — Anthropic: filters to allowed fields, promotes system messages; LiteLLM: transforms to SAP Orchestration `config.modules` envelope
5. **Upstream call** — all requests signed with `Authorization: Bearer <token>` and `AI-Resource-Group: <resource_group>`
6. **Response forwarding** — non-streaming: body piped as-is; streaming: per-provider flush strategy
