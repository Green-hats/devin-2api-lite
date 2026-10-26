# Configuration reference

[Back to README](../README.md) · [中文](configuration.zh-CN.md)

`config.yaml` is loaded once at startup. Unknown YAML fields are rejected. Environment variables
override the file for containers and systemd; nested maps such as `devin.reasoning_tiers` must stay
in YAML.

## Minimal config

```yaml
devin:
  base_url: "https://server.codeium.com"
  token: "devin-session-token$..."
  model: "glm-5-2"
```

`server`, `catalog`, `debug` and `auth` are optional. See
[`config.example.yaml`](../config.example.yaml) for a complete starting point.

## Fields

| Field | Default | Description |
| --- | --- | --- |
| `server.listen` | `:18089` | HTTP listen address |
| `server.max_concurrency` | `1024` | Concurrent `/v1/*` requests; `0` uses the default |
| `devin.base_url` | — | Devin Connect URL, e.g. `https://server.codeium.com` |
| `devin.token` | — | Session token; `/v1/*` is unavailable until set |
| `devin.model` | — | Default uid; a request's `model` wins |
| `devin.proxy` | — | Optional HTTP/HTTPS proxy; empty uses direct/system proxy |
| `devin.force_http1` | `true` | Avoids the upstream HTTP/2 multiplexing bottleneck |
| `devin.stream_idle_timeout` | `5m` | 504 if no upstream frame arrives in time; `0` disables |
| `devin.reasoning_tiers` | — | Maps a base id + effort to an actual uid (see below) |
| `catalog.snapshot` | — | Catalog snapshot path, relative to `config.yaml`; empty disables |
| `catalog.refresh` | `5m` | Catalog cache lifetime; `0` uses the default |
| `catalog.model_mode` | `raw` | `raw`, `openai` or `both` (see below) |
| `catalog.auto_tiers` | `false` | Derives base models/efforts from family metadata |
| `debug.enabled` | `false` | Writes per-request logs under `logs/` |
| `auth.api_key` | — | Empty disables authentication |

## Environment overrides

| Variable | Field |
| --- | --- |
| `SERVER_LISTEN` | `server.listen` |
| `SERVER_MAX_CONCURRENCY` | `server.max_concurrency` |
| `DEVIN_BASE_URL` | `devin.base_url` |
| `DEVIN_TOKEN` | `devin.token` |
| `DEVIN_MODEL` | `devin.model` |
| `DEVIN_PROXY` | `devin.proxy` |
| `DEVIN_FORCE_HTTP1` | `devin.force_http1` |
| `DEVIN_STREAM_IDLE_TIMEOUT` | `devin.stream_idle_timeout` |
| `CATALOG_SNAPSHOT` | `catalog.snapshot` |
| `CATALOG_REFRESH` | `catalog.refresh` |
| `CATALOG_MODEL_MODE` | `catalog.model_mode` |
| `CATALOG_AUTO_TIERS` | `catalog.auto_tiers` |
| `DEBUG_ENABLED` | `debug.enabled` |
| `AUTH_API_KEY` | `auth.api_key` |

`devin.reasoning_tiers` is a nested map and has no environment variable; use YAML.

## Reasoning tiers

Some Devin models encode the reasoning level in the uid (`swe-2-medium`, `swe-2-high`). Map them
to a stable base id:

```yaml
devin:
  reasoning_tiers:
    swe-2:
      medium: swe-2-medium
      high: swe-2-high
      max: swe-2-max
```

```bash
curl -s localhost:18089/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"swe-2","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}'
# upstream receives chat_model_uid=swe-2-high
```

- Responses use `reasoning: {"effort": "high"}`.
- Chat also accepts `thinking: true` as shorthand for `high`; `thinking: {"type": "enabled"}` works too.
- A configured base id without an effort returns `400` with the supported efforts.
- An explicit tier id such as `swe-2-high` is passed through unchanged.
- The gateway never invents suffixes; only configured or generated mappings apply.

With `catalog.auto_tiers: true`, suggestions come from upstream family metadata. Explicit
`devin.reasoning_tiers` entries win over generated ones.

## Model catalog

### Snapshot

```yaml
catalog:
  snapshot: "state/models.json"
  refresh: "5m"
```

The file is written atomically (`0600`) and is not a config file. On upstream failure the last
snapshot is served and marked `stale`. `GET /v1/model-manifest` exposes `schema_version`,
`generated_at`, `catalog_hash`, `stale`, the raw model list and generated `families` suggestions.

Refresh on demand:

```bash
go run ./cmd/devin-2api -config config.yaml -sync-models
```

### Model modes

| Mode | `/v1/models` |
| --- | --- |
| `raw` | Upstream ids such as `swe-2-high` (default) |
| `openai` | Virtual base models such as `swe-2`, with `reasoning_efforts` and `requires_reasoning_effort`; actual tier ids are hidden but still accepted |
| `both` | Base models and actual ids |

In `openai` mode, models without family metadata and fast/lightning variants stay visible.
Hidden tier ids remain valid in requests, but `/v1/models/{id}` returns 404 for them.

## Debug and auth

- `debug.enabled` writes request, intermediate, upstream frame and response logs under `logs/`.
- Failures are logged to stderr with a request ID even when debug is off.
- `auth.api_key` enables `Authorization: Bearer <key>` and `X-Api-Key: <key>`; empty disables auth.
- The session token is never written to debug logs.

## Validation

```bash
go run ./cmd/devin-2api -config config.yaml -check
```

`-check` validates the config, token and upstream catalog. It exits non-zero if only a stale
snapshot is available. `-sync-models` requires `catalog.snapshot` and exits non-zero if the
upstream cannot be refreshed.
