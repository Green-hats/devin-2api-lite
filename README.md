<div align="center">

<sub><b>English</b> | <a href="README.zh-CN.md">中文</a></sub>

<img src="docs/banner.en.png" alt="devin-2api lite" width="100%">

**A minimal gateway that puts Devin behind any OpenAI-compatible client**

<sub>The English README is the primary version.</sub>

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-2ea44f?style=flat-square)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-85%20passing-2ea44f?style=flat-square)](#development)
[![protoc](https://img.shields.io/badge/protoc-not%20required-4c78a8?style=flat-square)](#why-lite)
[![Dependencies](https://img.shields.io/badge/dependencies-1-f2c55c?style=flat-square)](go.mod)
[![Stars](https://img.shields.io/github/stars/Green-hats/devin-2api-lite?style=flat-square&logo=github)](https://github.com/Green-hats/devin-2api-lite/stargazers)

[Overview](#overview) · [Features](#features) · [Architecture](#architecture) · [Quick start](#quick-start) · [Connect a client](#connect-a-client) · [Configuration](#configuration) · [Differences](#differences-from-the-original) · [Credits](#credits--attribution)

</div>

---

> [!IMPORTANT]
> This project is a trimmed, partially rewritten fork of [**leookun/devin-2api**](https://github.com/leookun/devin-2api).
> **Both the code and the ideas come from the original project** (MIT). The original did the hard part: extracting
> 63 protobuf descriptors from the Devin client binary, reverse-engineering the Connect protocol, and pinning down
> the tool-description and image quirks. This repository only trims that work into a smaller form that builds
> without protoc. See [Credits & attribution](#credits--attribution) and [NOTICE](NOTICE).

## Overview

**devin-2api lite** exposes standard OpenAI endpoints and translates requests into the Connect RPC
(`GetChatMessage`) used by the Devin client, so any OpenAI-compatible client can call Devin's models:

```text
OpenAI client ──HTTP/SSE──► devin-2api lite ──protobuf/Connect──► Devin
```

It does not implement an agent loop: tools run in the client, and results are sent back with the next request.

<details>
<summary><b>中文</b></summary>

**devin-2api lite** 对外暴露标准 OpenAI 接口，对内把请求翻译成 Devin 客户端使用的 Connect RPC
（`GetChatMessage`），让你可以用任意 OpenAI 兼容客户端调用 Devin 的模型。它不实现 agent loop：
工具由客户端执行，结果再随下一轮请求传回来。

</details>

## Why lite?

The original project needs `task generate` (protoc + two plugins) before it compiles, because its upstream
protocol is generated from 63 descriptors. For self-hosted use, the panel, WebSocket transport and Anthropic
endpoint are usually not needed. lite makes three trade-offs:

- **Keep only the endpoints clients actually use**: `/v1/chat/completions`, `/v1/responses`, `/v1/models`;
- **Hand-write only the protobuf fields in use**: compress 63 descriptors into one readable field table and drop
  the generation step plus 3 MB of protocol artifacts;
- **One binary, one dependency**: `git clone && go build`, and Docker builds no longer need a protoc toolchain.

The cost is the original's extension surface. If you need the Anthropic endpoint, WebSocket transport or the
admin panel, use the [original project](https://github.com/leookun/devin-2api). The `llm` layer and protocol
abstraction are still here, so adding a protocol means adding one codec package.

## Features

| | Capability |
| --- | --- |
| **Two protocols** | OpenAI Chat Completions and Responses, streaming and non-streaming |
| **Model catalog** | `/v1/models`, `/v1/models/{model}`, 5-minute cache, `supports_images` |
| **Tool calling** | Sanitizes tool schemas and injects descriptions into the system prompt |
| **Multimodal** | Current-turn images (base64 data URL); models without vision are rejected locally |
| **Auth** | Optional API key via `Authorization: Bearer` or `X-Api-Key` |
| **Concurrency** | Semaphore limit (default 1024), timeouts and a tuned connection pool |
| **Networking** | HTTP/HTTPS proxy, forced HTTP/1.1, 600 s response-header timeout |
| **Observability** | Per-request staged debug logs, upstream error passthrough, error-to-status mapping |
| **Zero build deps** | No protoc, no generated code; the only dependency is `gopkg.in/yaml.v3` |

## Architecture

![devin-2api lite architecture](docs/architecture.en.png)

One request:

1. `POST /v1/responses` (or `/v1/chat/completions`) enters `internal/app`, passing the concurrency semaphore
   and optional API-key check;
2. the protocol package decodes JSON into `llm.RequestMessages` (system prompt, message history, tool definitions);
3. the Devin adapter assembles a `GetChatMessageRequest`: client identity, random device fingerprint, fixed
   sampling parameters, current-turn images;
4. the hand-written Connect client frames the request into a 5-byte length-prefixed message and reads frames back;
5. frames are translated into 12 intermediate event types, then encoded as typed SSE or aggregated JSON.

> The diagram is generated by [`docs/architecture.py`](docs/architecture.py). Upstream field numbers and framing
> are documented in [`docs/upstream-protocol.md`](docs/upstream-protocol.md).

## Quick start

### 1. Get a Devin token

devin-2api authenticates to the upstream with your Devin session token. On macOS, extract it from the Devin app's
local state:

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

The output looks like `devin-session-token$...`.

> [!WARNING]
> The token is equivalent to your account credentials. Keep it in a local `config.yaml` (ignored by
> `.gitignore`); never commit it, share it, or paste it into public issues.

### 2. Configure

```bash
git clone https://github.com/Green-hats/devin-2api-lite.git
cd devin-2api-lite
cp config.example.yaml config.yaml
```

Edit `config.yaml` and fill in at least `devin.token`. `base_url` and `model` already have usable defaults.

### 3. Run

From source:

```bash
go run ./cmd/devin-2api -config config.yaml
```

Or build a single binary:

```bash
go build -trimpath -ldflags="-s -w" -o devin-2api ./cmd/devin-2api
./devin-2api -config config.yaml
```

Docker:

```bash
docker build -t devin-2api-lite .
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  devin-2api-lite --config /app/config.yaml
```

Pushing a `v*` tag makes GitHub Actions build multi-arch images and push them to GHCR (see
[`release.yml`](.github/workflows/release.yml)).

### 4. Verify

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## Connect a client

Any OpenAI-compatible client that accepts a custom Base URL can connect directly:

| Setting | Value |
| --- | --- |
| Base URL | `http://localhost:8080/v1` |
| API Key | The value of `auth.api_key`; if empty, any non-empty string |
| Model | `glm-5-2` (or any uid returned by `/v1/models`) |

<details>
<summary><b>OpenAI Python SDK</b></summary>

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="anything")

stream = client.chat.completions.create(
    model="glm-5-2",
    messages=[{"role": "user", "content": "Explain a reverse proxy in one sentence."}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

</details>

<details>
<summary><b>curl: streaming Responses</b></summary>

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "input": "Hello",
    "stream": true
  }'
```

</details>

<details>
<summary><b>Codex CLI / IDE plugins</b></summary>

Pick an OpenAI-compatible / Responses-style provider in the client and point its Base URL at
`http://localhost:8080/v1`. Field names differ between clients, so follow their docs. Extra Responses fields
such as `store` or `metadata` are ignored by this gateway.

</details>

## API compatibility

| Endpoint | Method | Streaming | Notes |
| --- | --- | --- | --- |
| `/v1/chat/completions` | POST | SSE | Chat Completions; supports `tools`, `stream_options.include_usage` |
| `/v1/responses` | POST | typed SSE | Responses; supports `input`, `instructions`, `tools` |
| `/v1/models` | GET | — | Model catalog from upstream `GetCascadeModelConfigs` |
| `/v1/models/{model}` | GET | — | Single model |
| `/healthz` | GET | — | Liveness; bypasses auth and the concurrency limit |

Request bodies are capped at 32 MiB (to leave room for base64 images); unknown fields are ignored.

## Configuration

The YAML file is loaded once at startup; unknown fields are rejected.

| Field | Description | Required |
| --- | --- | --- |
| `server.listen` | HTTP listen address | Yes |
| `server.max_concurrency` | Max concurrent `/v1/*` requests, default 1024 | No |
| `devin.base_url` | Devin Connect base URL, e.g. `https://server.codeium.com` | When a token is set |
| `devin.token` | Devin session token; `/v1/*` is unavailable until set | No |
| `devin.model` | Default model uid; a request's `model` wins | When a token is set |
| `devin.proxy` | Optional HTTP/HTTPS proxy; empty uses the system proxy env | No |
| `devin.force_http1` | Force HTTP/1.1, default true | No |
| `debug.enabled` | Write per-request debug logs under `logs/` | No |
| `auth.api_key` | API key for `/v1/*`; empty disables auth | No |

`force_http1` is on by default: the upstream handles HTTP/2 multiplexed streams serially, which spikes
time-to-first-token under concurrency. HTTP/1.1 gives each request its own connection, matching the Devin
client's multi-window behavior.

Debug log layout:

```text
logs/<timestamp>/
├── 01-http-request.json     Raw HTTP request
├── 02-request-messages.json Intermediate request model
├── 03-devin-request.json    Upstream request projection
├── 04-devin-response.jsonl  Upstream frame projection
├── 05-response-events.jsonl Intermediate event stream
├── 06-http-response.jsonl   SSE / JSON written to the client
├── error.json               First failing stage
└── meta.json                Status code, model, duration
```

## Differences from the original

| Capability | Original | lite |
| --- | --- | --- |
| `/v1/chat/completions` | ✅ | ✅ |
| `/v1/responses` | ✅ | ✅ |
| `/v1/models` | ✅ | ✅ |
| `/v1/messages` (Anthropic) | ✅ | ❌ |
| Responses WebSocket transport | ✅ | ❌ |
| `/panel` admin UI | ✅ | ❌ |
| Staged debug logs | ✅ | ✅ (trimmed) |
| Proxy | HTTP/HTTPS/SOCKS5 | HTTP/HTTPS |
| Upstream integration | Generated Connect bindings (protoc) | Hand-written wire codec + Connect framing |
| Build prerequisite | `task generate` | None |
| Third-party deps | connect, chi, gorilla, protobuf, x/net, yaml | yaml |
| Production LOC | 8,851 | 5,588 |

Design notes:

- **Tool descriptions stay out of the schema**: the upstream treats `description`/`title` as tool-classification
  hints, so the adapter strips those annotations and injects `<tool name="...">` blocks into the system prompt;
- **Images only on the current turn**: historical images become `[Image omitted from history]` to avoid upstream
  `invalid_argument`;
- **Field numbers live in one place**: `internal/adapter/devin/types.go` only covers the fields actually read or
  written; unknown fields are skipped, so new upstream fields usually need no code change.

## Project layout

```text
.
├── cmd/devin-2api/          Entrypoint: config → adapter → HTTP server
├── internal/
│   ├── adapter/devin/       Upstream adapter: wire.go / types.go / assembly / frame decoding
│   ├── api/openai/          HTTP codecs for chat and responses
│   ├── app/                 Routing, request lifecycle, error mapping
│   ├── connect/             Minimal Connect protocol client (frames + unary calls)
│   ├── llm/                 Vendor-neutral intermediate model
│   ├── config/              YAML loading and validation
│   ├── debuglog/            Per-request debug logs
│   └── httpproxy/           Proxy and connection pool
├── docs/
│   ├── architecture.png     Chinese architecture diagram
│   ├── architecture.en.png  English architecture diagram
│   ├── architecture.py      Diagram generator
│   ├── banner.png           Chinese README banner
│   ├── banner.en.png        English README banner
│   └── upstream-protocol.md Upstream field table and upgrade guide
├── config.example.yaml
├── Dockerfile
├── NOTICE                   Derivation and attribution notes
└── LICENSE
```

## Development

```bash
go test ./...     # unit tests + fake-upstream end-to-end tests
go vet ./...
gofmt -l .
```

Test focus:

- **Field numbers are locked**: `internal/adapter/devin/devin_test.go` asserts request encoding and response
  decoding;
- **Event state machine**: text, thinking, incremental tool arguments, stop reasons, usage;
- **Protocol layer**: Connect frames, error envelopes, gzip, truncated streams
  (`internal/connect/connect_test.go`);
- **End-to-end**: an `httptest` fake Devin upstream exercises "HTTP → adapter → frame → SSE/JSON"
  (`internal/app/app_upstream_test.go`).

When the upstream protocol changes, re-run the extraction in the original project and update the field table
here; the procedure is documented in [`docs/upstream-protocol.md`](docs/upstream-protocol.md).

## Roadmap

- [x] Chat Completions / Responses / Models
- [x] Hand-written protobuf wire codec + Connect client
- [x] Fake-upstream end-to-end tests
- [ ] Anthropic `/v1/messages`
- [ ] Responses WebSocket transport
- [ ] SOCKS5 proxy
- [ ] Model pricing / usage dashboard
- [ ] Automatic field validation against upstream `all-protos.proto`

## FAQ

<details>
<summary><b>401 or <code>Invalid token</code>?</b></summary>

The token is expired, truncated, or prefixed with `Bearer `. Extract it again from the Devin app and make sure
it is the full `devin-session-token$...`. If you set `auth.api_key`, the client key must match it.

</details>

<details>
<summary><b>502 / upstream error?</b></summary>

The gateway passes upstream error messages through verbatim. Enable `debug.enabled` and inspect `error.json`
and `04-devin-response.jsonl` to find the failing hop. Common causes: an expired token, an unknown model uid,
or rate limiting.

</details>

<details>
<summary><b>Why don't <code>temperature</code> / <code>max_tokens</code> take effect?</b></summary>

Upstream sampling is fixed at `max_tokens=128000`, `temperature=1`, `top_k=40`, `top_p=0.95`. This gateway only
translates protocols; it does not change those values.

</details>

<details>
<summary><b>Are images supported?</b></summary>

Yes, for the current turn, as base64 data URLs. Historical images are replaced with placeholder text. Models
without vision are rejected locally instead of sending an invalid request upstream.

</details>

<details>
<summary><b>Does it execute tools?</b></summary>

No. The gateway returns one model response at a time. Tool calls come back as standard `tool_calls`; the client
executes them and sends the results as the next turn.

</details>

## Known limitations

- This is an **unofficial** protocol impersonation of the Devin client; the upstream may change at any time.
- Sampling parameters are fixed; client-side `temperature` / `max_tokens` are ignored.
- Images are attached to the current turn only; historical images become placeholder text.
- SOCKS5 is not supported; use a local HTTP proxy port instead.
- No admin panel, WebSocket transport or Anthropic endpoint (see the Roadmap and the original project).

## Disclaimer

This project is not affiliated with, endorsed by, or supported by Devin, Windsurf, Codeium, or Cognition.
It accesses the upstream service with a user-supplied session token and **may violate the upstream terms of
service**. Use it for learning and research only, and evaluate the account, data and compliance risks yourself.
Do not use it for commercial resale, quota farming, or any purpose that violates the upstream terms.

## Credits & attribution

**This project was not built from scratch.** The protocol reverse-engineering, field semantics, adapter details
and a large part of the code come from [**leookun/devin-2api**](https://github.com/leookun/devin-2api)
(MIT, Copyright © 2026 leookun):

| Part | Origin |
| --- | --- |
| `internal/llm/` | Original project; import path changed |
| `internal/api/openai/{chat,responses}/` | Original project; import path changed |
| `internal/api/common/` | Original project; import path changed |
| `internal/app/`, `config/`, `debuglog/` | Original project; panel / WebSocket / Anthropic trimmed |
| `internal/adapter/devin/` request assembly, tool handling, frame decoding | Original project; adapted to the hand-written types |
| Protocol fields and descriptor-extraction approach | Original project's `cmd/protoextract` and `outputs/devin-proto/` |
| `internal/connect/`, `wire.go`, `types.go` | New in this repository |
| End-to-end tests, diagrams, README | New in this repository |

Special thanks to the original author for the hardest part: scanning `FileDescriptorProto` values out of the
Devin language-server binary, rebuilding 63 proto files, and reconstructing the Connect framing and end-stream
envelope, then publishing all of it under MIT. Without that project, this repository would not exist.

The upstream protocol belongs to Devin / Windsurf / Codeium; this repository contains only the minimal field
subset needed for interoperability. Full derivation notes are in [NOTICE](NOTICE).

## License

[MIT](LICENSE) © 2026 leookun (original project) and Green-hats (lite modifications).
