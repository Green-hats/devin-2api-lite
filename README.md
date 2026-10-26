<div align="center">

<sub><b>English</b> | <a href="README.zh-CN.md">中文</a></sub>

<img src="docs/banner.en.png" alt="devin-2api lite" width="100%">

**A minimal gateway that puts Devin behind any OpenAI-compatible client**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-2ea44f?style=flat-square)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-85%20passing-2ea44f?style=flat-square)](#development)
[![protoc](https://img.shields.io/badge/protoc-not%20required-4c78a8?style=flat-square)](#what-it-is)
[![Dependencies](https://img.shields.io/badge/dependencies-1-f2c55c?style=flat-square)](go.mod)

[Quick start](#quick-start) · [Endpoints](#endpoints) · [Configuration](#configuration) · [Credits](#credits)

</div>

---

> [!IMPORTANT]
> A trimmed, partially rewritten fork of [**leookun/devin-2api**](https://github.com/leookun/devin-2api).
> The protocol reverse-engineering, field semantics and most of the `llm` / HTTP / adapter code come from that
> project (MIT). This fork adds a hand-written protobuf + Connect layer.
> Per-file attribution is in [NOTICE](NOTICE).

## What it is

devin-2api lite speaks OpenAI (Chat Completions and Responses) and talks to Devin over its Connect RPC
`GetChatMessage`. Tools run in the client; the gateway translates one model turn at a time.

```text
OpenAI client ──HTTP/SSE──► devin-2api lite ──protobuf/Connect──► Devin
```

## Highlights

- **Two protocols** — Chat Completions and Responses, streaming or buffered.
- **Model catalog** — `/v1/models` with a 5-minute cache and `supports_images`.
- **Tool-aware** — strips schema annotations and moves descriptions into the system prompt.
- **Current-turn images** — base64 data URLs; rejected locally for non-vision models.
- **No protoc** — 63 upstream descriptors become a ~250-line wire codec; the only dependency is `yaml.v3`.

## Architecture

![devin-2api lite architecture](docs/architecture.en.png)

Request JSON → `llm.RequestMessages` → `GetChatMessage` frames → 12 event types → typed SSE or one JSON
response. Field numbers and framing are documented in
[`docs/upstream-protocol.md`](docs/upstream-protocol.md).

## Quick start

### 1. Get a token

On macOS, read the Devin session token from the app's local state:

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

> [!WARNING]
> The token is your account credential. Keep it in a local `config.yaml` (git-ignored) and never share it.

### 2. Configure

```bash
git clone https://github.com/Green-hats/devin-2api-lite.git
cd devin-2api-lite
cp config.example.yaml config.yaml   # fill in devin.token
```

### 3. Run

```bash
go run ./cmd/devin-2api -config config.yaml
```

Or with Docker:

```bash
docker build -t devin-2api-lite .
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  devin-2api-lite --config /app/config.yaml
```

### 4. Verify

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## Use it

| Setting | Value |
| --- | --- |
| Base URL | `http://localhost:8080/v1` |
| API key | `auth.api_key`; any non-empty string if unset |
| Model | `glm-5-2` or any uid from `/v1/models` |

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

Any OpenAI-compatible client (OpenAI SDK, Codex CLI, IDE plugins) can point its Base URL here.

## Endpoints

| Endpoint | Method | Streaming | Notes |
| --- | --- | --- | --- |
| `/v1/chat/completions` | POST | SSE | `tools`, `stream_options.include_usage` |
| `/v1/responses` | POST | typed SSE | `input`, `instructions`, `tools` |
| `/v1/models` | GET | — | Cached upstream catalog |
| `/v1/models/{model}` | GET | — | Single model |
| `/healthz` | GET | — | No auth, no concurrency limit |

Request bodies are capped at 32 MiB; unknown fields are ignored.

## Configuration

| Field | Default | Notes |
| --- | --- | --- |
| `server.listen` | `:8080` | HTTP listen address |
| `server.max_concurrency` | `1024` | Concurrent `/v1/*` requests |
| `devin.base_url` | — | Devin Connect URL, e.g. `https://server.codeium.com` |
| `devin.token` | — | Session token; `/v1/*` is unavailable until set |
| `devin.model` | — | Default uid; a request's `model` wins |
| `devin.proxy` | — | Optional HTTP/HTTPS proxy |
| `devin.force_http1` | `true` | Avoids the upstream's HTTP/2 multiplexing bottleneck |
| `debug.enabled` | `false` | Writes per-request logs under `logs/` |
| `auth.api_key` | — | Empty disables auth |

Unknown YAML fields are rejected. Debug logs capture the HTTP request, intermediate request, upstream frames,
events and response, plus the first failing stage.

## Differences from the original

| | Original | lite |
| --- | --- | --- |
| Endpoints | chat, responses, models, Anthropic, WebSocket, panel | chat, responses, models, health |
| Upstream bindings | Generated Connect bindings | Hand-written wire + Connect |
| Build step | `task generate` | None |
| Dependencies | 7 | 1 (`yaml.v3`) |
| Production LOC | 8,851 | 5,588 |

The `llm` layer and protocol abstraction are unchanged; adding Anthropic or WebSocket back means adding one
codec package.

## Caveats

- **Unofficial.** This impersonates the Devin client protocol and may stop working at any time; it may also violate
  upstream terms. Not affiliated with Devin, Windsurf, Codeium or Cognition.
- **Fixed sampling.** `max_tokens=128000`, `temperature=1`, `top_k=40`, `top_p=0.95`; client values are ignored.
- **Current-turn images only.** Historical images are replaced with placeholder text.
- **No SOCKS5.** Use a local HTTP proxy port if needed.

## Development

```bash
go test ./...     # full suite, including a fake-upstream end-to-end test
go vet ./...
gofmt -l .
```

## Credits

Protocol reverse-engineering, field semantics and much of `internal/llm`, `internal/api` and
`internal/adapter/devin` come from [**leookun/devin-2api**](https://github.com/leookun/devin-2api)
(MIT, © 2026 leookun). New here: `internal/connect`, the wire codec, the trimmed app/config, tests and docs.
Per-file details are in [NOTICE](NOTICE).

## License

[MIT](LICENSE) © 2026 leookun (original project) and Green-hats (lite modifications).
