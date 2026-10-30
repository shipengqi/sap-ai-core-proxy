# sap-ai-core-proxy

A Go reverse proxy that exposes SAP AI Core LLM deployments through standard API surfaces:
**OpenAI**, **Anthropic**, **Gemini**, and **LiteLLM/Orchestration**.

## Features

- **4 API surfaces** — OpenAI-compatible, Anthropic-native, Gemini-native, SAP Orchestration (LiteLLM)
- **Streaming support** — SSE for OpenAI & Anthropic, raw chunked JSON for Gemini
- **Automatic auth** — OAuth 2.0 client-credentials token exchange with in-memory caching
- **Deployment discovery** — queries SAP AI Core for RUNNING deployments; 12-hour cache; exact model names, explicit aliases and vendor-prefix normalization; same-model failover on deployment retirement
- **Model aliases** — translates standard model names (e.g. `claude-3-5-sonnet-latest`) to SAP AI Core deployment names automatically
- **Tool calling** — OpenAI `tools` / `tool_calls` / `role: "tool"` and Anthropic `tool_use` / `tool_result` blocks are translated into each other, so a tool-calling loop works across either surface regardless of which model family backs it
- **Claude Code / VSCode / IDE compatible** — works with any client that speaks OpenAI or Anthropic APIs

## Quick Start

### 1. Build

```bash
git clone https://github.com/shipengqi/sap-ai-core-proxy
cd sap-ai-core-proxy
make build
```

Confirm what you built. The version is stamped from `VERSION`, the git commit, and the build timestamp, and it does not require a config file:

```bash
./aicoreproxy -version
# sap-ai-core-proxy 1.2.0 (commit 445d1d2, built 2026-10-22T03:10:12Z, go1.26.0 darwin/arm64)
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
    "log_level": "info",
    "anthropic_auto_cache": false
  }
}
```

### 3. Run

```bash
./aicoreproxy
# or
make run
```

The proxy starts on `http://localhost:3001` (default).

### Command Line

The Cobra CLI groups server and diagnostic tools under one executable:

```bash
./aicoreproxy                         # default: start the proxy
./aicoreproxy serve --no-tui           # explicitly start without the embedded dashboard
./aicoreproxy tui --url http://localhost:3001 --interval 5s
./aicoreproxy tui --plain             # one remote snapshot, then exit
./aicoreproxy cacheprobe --models gpt-5.6-terra,gpt-4o
./aicoreproxy version                 # full version and build identity
./aicoreproxy --help
./aicoreproxy cacheprobe --help
```

No-argument startup remains compatible with Docker and systemd. `--version` is also supported on the root and `serve` commands. Version and help never load configuration or contact SAP. Known legacy single-dash flags (such as `-version`, `-no-tui`, `-models` and `-interval`) remain accepted, including `=value` forms. Flag values and tokens after `--` are not rewritten. Unlike the old parser, unexpected positional arguments are rejected instead of silently ignored; syntax errors exit 2, runtime errors exit 1. Put tool-specific flags after their subcommand; server flags work on both the root and `serve` commands.

The standalone `go run ./cmd/aicore-tui` and `go run ./cmd/aicore-cacheprobe` entry points remain available and share the same tool commands. Configuration precedence remains defaults → JSON file → environment variables; no new configuration flags or Viper layer are introduced. `cacheprobe` sends real model requests and can incur usage charges.

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
| `server.metrics` | No | `true` | Register `GET /metrics`, `GET /stats`, and the OTLP receiver `POST /otel/v1/metrics` |
| `server.compress_logs` | No | `false` | Deterministically compress every recognisable `tool_result` log in the request, history included. The transform is a pure function of the input text, so a rewritten history is byte-identical from turn to turn and the prompt cache keeps hitting; the model therefore sees compressed history logs in long sessions. Leave disabled unless validated |
| `server.anthropic_auto_cache` | No | `false` | Add `{"type":"ephemeral"}` cache control to the final tool sent to Claude Bedrock only when the client provided no `cache_control` anywhere in its request |

### Environment Variable Overrides

All config values can be overridden by environment variables (higher priority than the JSON file):

| Env Var | Overrides |
|---|---|
| `AICOREPROXY_CONFIG_FILE` | Config file path (default: `~/.aicoreproxy/config.json`) |
| `SAP_AI_CORE_BASE_URL` | `sap_ai_core.base_url` |
| `SAP_AI_CORE_TOKEN_URL` | `sap_ai_core.token_url` |
| `SAP_AI_CORE_CLIENT_ID` | `sap_ai_core.client_id` |
| `SAP_AI_CORE_CLIENT_SECRET` | `sap_ai_core.client_secret` |
| `SAP_AI_CORE_RESOURCE_GROUP` | `sap_ai_core.resource_group` |
| `PORT` | `server.port` |
| `LOG_LEVEL` | `server.log_level` |
| `METRICS_ENABLED` | `server.metrics` |
| `COMPRESS_LOGS` | `server.compress_logs` |
| `ANTHROPIC_AUTO_CACHE` | `server.anthropic_auto_cache` |
| `COMPRESS_LOGS_SKIP` | Compression skip strategies (comma-separated, for example `log,json`) |

## API Endpoints

### OpenAI Surface

| Method | Path | Description |
|---|---|---|
| GET | `/openai/v1/models` | List all RUNNING deployments in OpenAI format |
| POST | `/openai/v1/chat/completions` | Chat completions (streaming supported) |
| POST | `/openai/v1/embeddings` | Embeddings |
| POST | `/openai/v1/responses` | Create a response (native streaming and response affinity) |
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
| POST | `/litellm/v1/chat/completions` | Non-streaming chat via SAP Orchestration |
| POST | `/litellm/v1/completions` | Same messages-based adapter; legacy prompt requests return 400 |
| POST | `/litellm/v1/embeddings` | Embeddings (direct deployment) |

### Service Endpoints

Always registered, regardless of `server.metrics`.

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness probe: `{"status":"ok","service":"sap-ai-core-proxy"}` |
| GET | `/version` | Build identity: version, commit, build date, Go version, platform |

`/version` exists because the release image is distroless — no shell, no files to read — so it is the only way to ask a running instance which build it is. Both routes are GETs, so they never reach the usage counters.

### Usage Metrics

Token accounting is on by default; set `server.metrics: false` (or `METRICS_ENABLED=false`) to stop registering these routes. The OTLP endpoint is an acknowledge-only compatibility sink for clients such as agent CLIs: it does not forward telemetry to SAP AI Core or mix arbitrary client metrics and labels into the proxy's Prometheus registry.

| Method | Path | Description |
|---|---|---|
| GET | `/stats` | JSON: totals, per-surface and per-model tokens, cache hit ratio |
| GET | `/metrics` | Prometheus text exposition (hand-written, no extra dependency) |
| POST | `/otel/v1/metrics` | Standard OTLP/HTTP Protobuf compatibility sink; validates and acknowledges client telemetry without storing or exposing it |
| GET | `/stats/cache` | Cache attribution: hit/rewrite/no-cache/no-usage, drift, volatile patterns, and tracked sessions |
| GET | `/stats/compress` | Compression block counts, before/after bytes, and skip reasons |

Three things to know before reading the numbers:

- `sap_proxy_requests_total{status}` is the **upstream-effective** status, not what the client received. Failures received before SSE starts retain the upstream HTTP status and retry/request-ID headers. Failures after streaming starts use a protocol error event; metrics record the upstream error (or `502`) even though the HTTP status is already `200`.
- `usage_missing` means "the response was inspected but the upstream reported no usage". It never counts failed requests or endpoints that are not instrumented.
- `uninstrumented` means "the request succeeded but no observation path read the response". A non-zero value points at a proxy code path missing accounting, not at a silent upstream.

## Model Routing & Fallback

### Deployment Discovery

On every request, the proxy resolves the upstream SAP AI Core deployment URL in these passes:

0. **`-latest` family resolution** — a bare `claude-<family>-latest` alias is matched against the live deployment list and bound to the highest version of that family (see below)
1. **Exact match** — `deployment.modelName == normalizedModel`
2. **Vendor-prefix normalization** — `openai--gpt-6.1-sol` and `gpt-6.1-sol` refer to the same model. Explicit catalogue aliases also preserve model identity.

Missing models return an error; the proxy does not select a different model family or version. The deployment list is cached for 12 hours. Explicit retirement (410, an error envelope naming Gone, or deployment_not_found) blacklists that instance for one hour. Bounded failover selects instances of the initially resolved model, including when the request used a moving `-latest` alias. A generic endpoint 404 does not blacklist the deployment. When all known instances retire, discovery can refresh at most once per minute. Streaming failover happens before any downstream content is sent.

### Anthropic Model Aliases

The proxy automatically translates standard Anthropic model names to SAP AI Core deployment names:

| User-Facing Name | SAP AI Core Deployment Name |
|---|---|
| `claude-opus-latest` | newest running `opus` deployment — resolved dynamically |
| `claude-opus-4.8` / `claude-opus-4-8` | `anthropic--claude-4.8-opus` |
| `claude-opus-4.5` / `claude-opus-4-5` | `anthropic--claude-4.5-opus` |
| `claude-sonnet-latest` | newest running `sonnet` deployment — resolved dynamically |
| `claude-sonnet-4.6` / `claude-sonnet-4-6` | `anthropic--claude-4.6-sonnet` |
| `claude-sonnet-4.5` / `claude-sonnet-4-5` | `anthropic--claude-4.5-sonnet` |
| `claude-haiku-latest` | newest running `haiku` deployment — resolved dynamically |
| `claude-haiku-4.5` / `claude-haiku-4-5` | `anthropic--claude-4.5-haiku` |
| `claude-3-7-sonnet-latest` | `anthropic--claude-3.7-sonnet` |
| `claude-3-5-sonnet-latest` | `anthropic--claude-3.5-sonnet` |
| `claude-3-5-haiku-latest` | `anthropic--claude-3.5-haiku` |
| `claude-3-opus-latest` | `anthropic--claude-3-opus` |

`claude-<family>-latest` is resolved against the live deployment list, so it always means the
newest running version of that family — no table edit is needed when a new release appears, and
these aliases are deliberately absent from the table so they cannot be frozen by accident.
Version-pinned legacy aliases such as `claude-3-5-sonnet-latest` stay fixed, because they name
one specific version rather than a moving target. If no deployment of the family is running,
the request fails with a 404 naming the requested model.

Names not in the table are translated by shape rather than by lookup, so newly released
Claude versions work without a code change: `claude-<family>-<major>-<minor>` becomes
`anthropic--claude-<major>.<minor>-<family>` (e.g. `claude-sonnet-4-9` →
`anthropic--claude-4.9-sonnet`), and the legacy `claude-<major>-<minor>-<family>` /
`claude-<major>-<family>` shapes are handled too. This is necessary because SAP puts the
version first while Anthropic puts the family first. Alias normalization handles this naming difference without changing the requested version.

## Streaming

| Surface | Wire Format |
|---|---|
| OpenAI Chat | SSE; native Chat is forwarded, Claude/GLM/Gemini/Responses are converted to Chat chunks |
| OpenAI Responses | Native SSE events, forwarded immediately |
| Anthropic | SSE with `event:` lines; injected automatically if SAP omits them |
| Gemini | Native chunked JSON array or SSE (`alt=sse`); forwarded as received |

### Compatibility boundaries

OpenAI Chat supports GPT, Claude, Gemini, Qwen, GLM and Perplexity in both response modes. Function tools are mapped for Claude/GLM and Gemini, including call IDs and results. For GPT deployments that explicitly reject Chat function tools and require Responses, a bounded 400 fallback uses the Responses API. Explicit `reasoning_effort:"none"` can still use Chat after this capability is cached. Other 400 errors are returned to the caller. If a Chat rejection also forces an explicit `reasoning_effort:"none"` request onto Responses, the bridge returns 400 because its current mapping cannot preserve that setting.

The Responses bridge accepts text, images and function tools. It preserves image detail, refusal, incomplete status and usage, and defaults translated function tools to `strict:false`. Unsupported options such as `stop`, `n>1`, logprobs, and other tool/content types return 400. Claude and GLM reject unsupported Chat options; Claude rejects reasoning budgets that exceed an explicit token limit. Gemini supports function tools and structured JSON output; remote images, nonzero reasoning-effort translation, and enforcing `parallel_tool_calls:false` require the native API.

The Anthropic surface supports Claude natively in both modes. Non-Claude requests share provider routing and Responses fallback for non-streaming calls; streaming and Anthropic thinking on those routes return 400. LiteLLM supports messages, tools and response_format through the non-streaming Orchestration adapter; streaming, legacy prompt and unimplemented generation options return 400.

Responses IDs are kept per proxy instance, with a 24-hour TTL and a 10,000-route cap. `previous_response_id` stays on the owning deployment; failed DELETE retains that route. Restarting the proxy loses the routes, and multiple replicas require sticky routing. Shared durable affinity is not implemented. Use the native provider surface for signed reasoning history or model-specific adaptive-thinking features; Chat conversion does not provide full fidelity for those fields.

Inference request bodies are limited to 32 MiB. SSE frames and Gemini JSON stream values are limited to 8 MiB; oversized or broken streams report an error. Streaming uses a 90-second response-header timeout and a 180-second read-idle timeout without a total generation timeout. Client write failure stops upstream consumption. Capability caches expire after one hour and are scoped to deployment address/model, resource group, endpoint and API version.

These behaviors are covered by local mock-upstream tests. Availability and provider-specific constraints of individual SAP deployments still require live verification.

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

## Systemd Deployment

For production deployments on Linux (including Kylin / 信创 systems), running as a
systemd service avoids container network policy restrictions and provides automatic
restart on failure.

A ready-to-use unit file is provided at [`deploy/aicoreproxy.service`](deploy/aicoreproxy.service).

### Install

```bash
sudo make deploy
```

This single command (idempotent — safe to re-run for updates):

1. Builds the binary and installs it to `/usr/local/bin/aicoreproxy`
2. Creates the `aicoreproxy` system user and group if they don't exist
3. Creates `/etc/aicoreproxy/` with correct permissions
4. Copies `config.json.example` → `/etc/aicoreproxy/config.json` **only if the file doesn't exist yet**
5. Installs `deploy/aicoreproxy.service` and runs `systemctl enable`
6. Restarts the service and prints its status

On first install, the script will print a reminder to edit the config file before the
service can start successfully:

```
*** Edit /etc/aicoreproxy/config.json with your SAP AI Core credentials ***
```

**Permission summary:**

| Path | Owner | Mode | Reason |
|---|---|---|---|
| `/usr/local/bin/aicoreproxy` | `root:root` | `755` | Executable by all, writable only by root |
| `/etc/aicoreproxy/` | `root:aicoreproxy` | `750` | Traversable by service user, hidden from others |
| `/etc/aicoreproxy/config.json` | `root:aicoreproxy` | `640` | Readable by service user only; contains secrets |

The service reads config from `/etc/aicoreproxy/config.json` by default (as set in the
unit file). To use a different path, edit the `AICOREPROXY_CONFIG_FILE` line in
[`deploy/aicoreproxy.service`](deploy/aicoreproxy.service) before installing.

### Manage

```bash
sudo systemctl status aicoreproxy     # check status
sudo systemctl restart aicoreproxy    # restart
sudo journalctl -u aicoreproxy -f     # tail logs
```

## Terminal Dashboard

The repository includes a read-only terminal dashboard in the same Go module. `make run` starts the Gin proxy and dashboard together by default:

```bash
make run
```

The dashboard starts in the same process as the proxy and refreshes metrics automatically. To run only the proxy, disable it explicitly:

```bash
make run WITH_TUI=0
# or run the binary directly:
./aicoreproxy --no-tui
# equivalent target:
make server
```

To run the dashboard against an already-running proxy:

```bash
make tui
# or
./aicoreproxy tui --url http://localhost:3001 --interval 5s
# standalone entry point is also supported:
go run ./cmd/aicore-tui --url http://localhost:3001 --interval 5s
```

The dashboard polls `/stats`, `/stats/cache`, `/stats/compress`, and `/version`, and shows request totals, failures, token/cache counters, cache hit ratio, per-surface totals, compression counters, and endpoint health. The build version sits in the header: the in-process dashboard reads it directly, while `make tui` fetches it from the remote instance and prints `unknown` when that proxy predates the route. It defaults to `http://localhost:3001`; `--plain` prints one snapshot and exits, which is useful for scripts:

```bash
./aicoreproxy tui --plain
```

When launched through `make run` or `./aicoreproxy` in an interactive terminal, the dashboard and proxy run together in one process. The dashboard is automatically skipped when stdout is redirected or captured (for example by WorkBuddy, systemd, Docker, or another process), preventing ANSI terminal control sequences from corrupting the caller's output. The `-no-tui` flag disables the dashboard explicitly. The standalone `make tui` target remains available for connecting to an already-running proxy.

## Cache Capability Probe

Prompt caching is not one mechanism, and whether a given deployment supports it — or supports explicit breakpoints — cannot be read off the model name. `cmd/aicore-cacheprobe` asks the deployment directly:

```bash
./aicoreproxy cacheprobe
./aicoreproxy cacheprobe --models gpt-5.6-terra,gpt-5.4,gpt-4o
# standalone entry point is also supported:
go run ./cmd/aicore-cacheprobe -models gpt-5.6-terra,gpt-4o
```

It sends a ~2000 token prefix five times per model and reports what the usage block says: whether implicit caching hits with no marker at all, whether `prompt_cache_options` / `prompt_cache_breakpoint` are accepted and a repeat cache hit is observed (a hit alone cannot distinguish explicit breakpoints from automatic caching), and whether `prompt_cache_key` is accepted. It talks to SAP AI Core directly, so the proxy's own field filtering cannot mask the answer.

Two findings worth knowing before writing a client against these deployments: explicit breakpoints are rejected with `400 invalid_parameter` on models before GPT-5.6, and `prompt_cache_key` participates in cache identity, so changing it turns a warm prefix into a miss.

## Makefile Targets

| Target | Description |
|---|---|
| `make build` | Build the binary (`./aicoreproxy`) |
| `make run` | Run the Gin proxy and TUI together (default) |
| `make run WITH_TUI=0` | Run only the Gin proxy |
| `make server` | Run only the Gin proxy |
| `make tui` | Run the read-only terminal dashboard against an existing proxy |
| `make test` | Run unit tests |
| `make test.integration` | Run integration tests (mock mode if no env vars set) |
| `make vet` | Run `go vet` |
| `make lint` | Run `golangci-lint` |
| `make deploy` | Build + install/update as a systemd service (run as root) |
| `make docker.build` | Build Docker image (set `TAG=` to override) |
| `make clean` | Remove build artifacts |

`make build` and `make docker.build` both stamp the build identity from `VERSION`, the current git commit, and the build timestamp. Override `VERSION=`, `COMMIT=`, or `BUILD_DATE=` to reproduce a specific build.

## Project Structure

```
sap-ai-core-proxy/
├── main.go                          # Thin Cobra CLI entry point
├── cmd/aicore-tui/                   # Read-only terminal dashboard
│   ├── main.go
│   ├── stats/                        # Observation endpoint client
│   └── format/                       # Pure display formatting helpers
├── cmd/aicore-cacheprobe/            # Per-deployment prompt-cache capability probe
├── internal/
│   ├── cli/                         # Cobra commands and server startup
│   ├── dashboard/                   # Shared remote dashboard runner
│   ├── cacheprobe/                  # Shared prompt-cache probe runner
│   ├── config/config.go             # Load ~/.aicoreproxy/config.json + env overrides
│   ├── version/version.go           # Build identity for -version, /version and both dashboards
│   ├── usage/                       # Token accounting, /stats and Prometheus metrics
│   ├── diagnostics/                 # Cache attribution and /stats/cache
│   ├── compress/                    # Deterministic tool_result log compression and /stats/compress
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
├── deploy/
│   └── aicoreproxy.service          # systemd unit file
└── test/                            # Integration tests (mock + real SAP AI Core)
```

## How It Works

1. **Config load** — reads `~/.aicoreproxy/config.json`, applies env var overrides
2. **Token exchange** — on first request, POSTs client credentials to `token_url/oauth/token`; cached until 60s before expiry; concurrent requests deduplicated via singleflight
3. **Deployment resolution** — fetches all RUNNING deployments from SAP AI Core on first use; cached 12 hours; resolved via 3-tier lookup per request
4. **Request translation** — Anthropic: filters to allowed fields, promotes system messages; LiteLLM: transforms to SAP Orchestration `config.modules` envelope
5. **Upstream call** — all requests signed with `Authorization: Bearer <token>` and `AI-Resource-Group: <resource_group>`
6. **Response forwarding** — non-streaming: body piped as-is; streaming: per-provider flush strategy
