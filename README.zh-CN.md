<div align="center">

<img src="docs/banner.png" alt="devin-2api lite" width="100%">

**把 Devin 接进任何 OpenAI 兼容客户端的最小网关**

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-2ea44f?style=flat-square)](LICENSE)
[![CI](https://github.com/Green-hats/devin-2api-lite/actions/workflows/ci.yml/badge.svg)](https://github.com/Green-hats/devin-2api-lite/actions/workflows/ci.yml)
[![protoc](https://img.shields.io/badge/protoc-not%20required-4c78a8?style=flat-square)](#这是什么)
[![Dependencies](https://img.shields.io/badge/dependencies-1-f2c55c?style=flat-square)](go.mod)

<a href="README.md">English</a> | <b>中文</b>

[快速开始](#快速开始) · [接口](#接口) · [配置](#配置) · [来源](#来源)

</div>

---

> [!IMPORTANT]
> 这是 [**leookun/devin-2api**](https://github.com/leookun/devin-2api) 的裁剪重写版。协议逆向、字段语义与
> `llm` / HTTP / 适配器的大部分代码都来自原项目（MIT）。本仓库新增了手写的 protobuf + Connect 层。
> 逐文件署名见 [NOTICE](NOTICE)。

## 这是什么

devin-2api lite 对外说 OpenAI（Chat Completions 与 Responses），对内通过 Devin 的 Connect RPC
`GetChatMessage` 通信。工具由客户端执行，网关只负责一轮模型调用的协议转换。

```text
OpenAI 客户端 ──HTTP/SSE──► devin-2api lite ──protobuf/Connect──► Devin
```

## 亮点

- **双协议** —— Chat Completions 与 Responses，流式或非流式。
- **模型目录** —— `/v1/models`，5 分钟缓存，附带 `supports_images`。
- **工具调用** —— 剥离 Schema 注解，把工具说明注入 system prompt。
- **当前轮图片** —— base64 data URL；无视觉能力的模型在本地直接拒绝。
- **无 protoc** —— 63 个上游 descriptor 收敛为约 250 行 wire codec；唯一依赖 `yaml.v3`。

## 架构

![devin-2api lite 架构图](docs/architecture.png)

请求 JSON → `llm.RequestMessages` → `GetChatMessage` 帧 → 12 类事件 → typed SSE 或一条 JSON 响应。
字段号与帧格式见 [`docs/upstream-protocol.md`](docs/upstream-protocol.md)。

## 快速开始

### 1. 获取 token

macOS 下从 Devin 应用本地状态读取：

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

> [!WARNING]
> token 等同于账户凭据。只保存在本地 `config.yaml`（已被 git 忽略），不要分享或提交。

### 2. 配置

```bash
git clone https://github.com/Green-hats/devin-2api-lite.git
cd devin-2api-lite
cp config.example.yaml config.yaml   # 填入 devin.token
```

### 3. 自检

```bash
go run ./cmd/devin-2api -config config.yaml -check
# config ... / upstream ... / models N (vision M) / configured found=true
```

它会调用一次上游模型目录后退出，是确认 token 是否仍然可用的最快方式。

### 4. 运行

```bash
go run ./cmd/devin-2api -config config.yaml
```

或使用 Docker：

```bash
docker build -t devin-2api-lite .
docker run --rm -p 18089:18089 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  devin-2api-lite --config /app/config.yaml
```

### 5. 验证

```bash
curl http://localhost:18089/healthz
# {"status":"ok"}
```

## 使用

| 配置项 | 值 |
| --- | --- |
| Base URL | `http://localhost:18089/v1` |
| API Key | `auth.api_key`；未设置时任意非空字符串 |
| Model | `glm-5-2` 或 `/v1/models` 返回的任意 uid |

```bash
curl -N http://localhost:18089/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

OpenAI SDK、Codex CLI、IDE 插件等 OpenAI 兼容客户端，把 Base URL 指向这里即可。

## 接口

| 接口 | 方法 | 流式 | 说明 |
| --- | --- | --- | --- |
| `/v1/chat/completions` | POST | SSE | `tools`、`stream_options.include_usage` |
| `/v1/responses` | POST | typed SSE | `input`、`instructions`、`tools` |
| `/v1/models` | GET | — | 上游模型目录（带缓存） |
| `/v1/models/{model}` | GET | — | 单个模型 |
| `/healthz` | GET | — | 不鉴权、不占并发 |

请求体上限 32 MiB；未知字段会被忽略。状态码：未配置 token 返回 503，上游流超过
`stream_idle_timeout` 无帧返回 504，请求体过大返回 413。

## 配置

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `server.listen` | `:18089` | HTTP 监听地址 |
| `server.max_concurrency` | `1024` | `/v1/*` 并发上限 |
| `devin.base_url` | — | Devin Connect 地址，如 `https://server.codeium.com` |
| `devin.token` | — | 会话 token；未设置时 `/v1/*` 不可用 |
| `devin.model` | — | 默认模型；请求里的 `model` 优先 |
| `devin.proxy` | — | 可选 HTTP/HTTPS 代理 |
| `devin.force_http1` | `true` | 规避上游 HTTP/2 多路复用瓶颈 |
| `devin.stream_idle_timeout` | `5m` | 上游超时无帧则返回 504；`0` 表示不限制 |
| `devin.reasoning_tiers` | — | 把「基础模型 + reasoning_effort」映射为实际 uid，如 `swe-2` + `high` → `swe-2-high` |
| `debug.enabled` | `false` | 在 `logs/` 下记录每请求日志 |
| `auth.api_key` | — | 为空则不校验 |

### 推理档位映射

Devin 的部分模型把推理档位写在 uid 里（`swe-2-medium`、`swe-2-high`）。
配置 `devin.reasoning_tiers` 后，客户端可以传基础 id 加档位：

```bash
curl -s localhost:18089/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"swe-2","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}'
# 实际发给上游的 chat_model_uid 是 swe-2-high
```

Responses 用 `reasoning.effort`，Chat 也接受 `thinking: true` 作为 `high` 的简写。
只有配置过的映射才会生效，网关不会自行拼后缀；客户端传显式档位（如 `swe-2-high`）时保持原样。
基础 id（如 `swe-2`）是虚拟名称，可能不在 `/v1/models` 中，需要客户端直接填写。

未知 YAML 字段会被拒绝。所有字段都可用环境变量覆盖（`SERVER_LISTEN`、`DEVIN_TOKEN`、
`DEVIN_STREAM_IDLE_TIMEOUT`、`AUTH_API_KEY` 等），见 `config.example.yaml`。
调试日志包含 HTTP 请求、中间请求、上游帧、事件流、响应以及首个失败阶段；即使关闭 debug，
失败也会带请求 ID 输出到 stderr。

## 与原版的区别

| | 原版 | lite |
| --- | --- | --- |
| 接口 | chat、responses、models、Anthropic、WebSocket、面板 | chat、responses、models、health |
| 上游绑定 | protoc 生成 Connect 绑定 | 手写 wire + Connect |
| 构建前置 | `task generate` | 无 |
| 依赖 | 7 个 | 1 个（`yaml.v3`） |
| 生产代码 | 8,851 行 | 6,552 行 |

分层边界没有变化；要加回 Anthropic 或 WebSocket，只需补一个编解码包。

## 注意事项

- **非官方。** 这是对 Devin 客户端协议的模拟，随时可能失效，也可能违反上游条款；与 Devin、Windsurf、
  Codeium、Cognition 无关联。
- **采样参数固定。** `max_tokens=128000`、`temperature=1`、`top_k=40`、`top_p=0.95`，客户端传值不生效。
- **图片仅当前轮。** 历史图片会替换为占位文本。
- **不支持 SOCKS5。** 需要时请使用本地 HTTP 代理端口。

## 开发

```bash
go test -race ./...
go vet ./...
go install honnef.co/go/tools/cmd/staticcheck@2026.2.2
staticcheck ./...
gofmt -l .

# 对手写协议解析器做模糊测试
go test ./internal/adapter/devin -run=^$ -fuzz=FuzzDecodeChatResponse -fuzztime=10s
go test ./internal/connect -run=^$ -fuzz=FuzzReadFrame -fuzztime=10s

# 校验硬编码字段号是否与最新提取的 proto 一致
go run ./cmd/fieldcheck /path/to/all-protos.proto
```

## 来源

协议逆向、字段语义以及 `internal/llm`、`internal/api`、`internal/adapter/devin` 的大部分代码来自
[**leookun/devin-2api**](https://github.com/leookun/devin-2api)（MIT，© 2026 leookun）。
本仓库新增：`internal/connect`、wire codec、裁剪后的 app/config、测试与文档。逐文件说明见 [NOTICE](NOTICE)。

## License

[MIT](LICENSE) © 2026 leookun（原项目）与 Green-hats（lite 版修改）。
