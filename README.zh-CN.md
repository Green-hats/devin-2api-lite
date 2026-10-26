<div align="center">

<sub><a href="README.md">English</a> | <b>中文</b></sub>

<img src="docs/banner.png" alt="devin-2api lite" width="100%">

**把 Devin 接进任何 OpenAI 兼容客户端的最小网关**

<sub>英文版为主文档，本页为中文翻译。</sub>

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-2ea44f?style=flat-square)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-85%20passing-2ea44f?style=flat-square)](#开发)
[![protoc](https://img.shields.io/badge/protoc-not%20required-4c78a8?style=flat-square)](#为什么是-lite-版)
[![Dependencies](https://img.shields.io/badge/dependencies-1-f2c55c?style=flat-square)](go.mod)
[![Stars](https://img.shields.io/github/stars/Green-hats/devin-2api-lite?style=flat-square&logo=github)](https://github.com/Green-hats/devin-2api-lite/stargazers)

[简介](#简介) · [特性](#特性) · [架构](#架构) · [快速开始](#快速开始) · [配置](#配置) · [与原版的区别](#和原版的区别) · [来源与致谢](#来源与致谢)

</div>

---

> [!IMPORTANT]
> 本项目是 [**leookun/devin-2api**](https://github.com/leookun/devin-2api) 的裁剪重写版，**代码与思路均源自原项目**（MIT）。
> 原项目完成了最困难的部分：从 Devin 客户端二进制中提取 63 个 protobuf descriptor、还原 Connect 协议、
> 摸清工具描述与图片的历史行为。本仓库只是把这些成果裁剪成一个更小、无需 protoc 就能构建的形态。
> 详细的来源划分见 [来源与致谢](#来源与致谢) 与 [NOTICE](NOTICE)。

## 简介

**devin-2api lite** 对外暴露标准 OpenAI 接口，对内把请求翻译成 Devin 客户端使用的
Connect RPC（`GetChatMessage`），让你可以用任意 OpenAI 兼容客户端调用 Devin 的模型：

```text
OpenAI 客户端 ──HTTP/SSE──► devin-2api lite ──protobuf/Connect──► Devin
```

它不实现 agent loop：工具由客户端执行，结果再随下一轮请求传回来。

<details>
<summary><b>English</b></summary>

**devin-2api lite** is a minimal gateway that serves Devin through the OpenAI
Chat Completions and Responses APIs. It is a trimmed, partially rewritten fork
of [leookun/devin-2api](https://github.com/leookun/devin-2api) (MIT): the
dashboard, WebSocket transport and Anthropic endpoint are gone, and the
63-descriptor protobuf bindings are replaced by a hand-written wire codec — so
`go build` works without `protoc`, and the only dependency is `yaml.v3`.

</details>

## 为什么是 lite 版

原项目 clone 下来必须先 `task generate`（protoc + 两个插件）才能编译，因为上游协议是 63 个
descriptor 生成的 Connect 绑定；而自用场景里，面板、WebSocket、Anthropic 协议大多用不上。
lite 版做了三个取舍：

- **只保留真正会被用到的三个接口**：`/v1/chat/completions`、`/v1/responses`、`/v1/models`；
- **手写用到的 protobuf 字段**：把 63 个 descriptor 压缩成一份可读的字段表，删除生成步骤与 3 MB 协议产物；
- **单二进制、单依赖**：`git clone && go build` 即可，镜像构建也不再需要 protoc 工具链。

代价是失去原版的扩展面。如果你需要 Anthropic 协议、WebSocket 或管理面板，请使用
[原项目](https://github.com/leookun/devin-2api)；本仓库的 `llm` 中间层与协议抽象仍然保留，
加回一个新协议只需要补一个编解码包。

## 特性

| | 能力 |
| --- | --- |
| **双协议** | OpenAI Chat Completions 与 Responses，均支持流式 / 非流式 |
| **模型目录** | `/v1/models`、`/v1/models/{model}`，5 分钟缓存，附带 `supports_images` |
| **工具调用** | 清洗工具 Schema、把说明注入 system prompt，兼容客户端的 function calling |
| **多模态** | 当前轮图片（base64 data URL）；无视觉能力的模型会在本地提前拒绝 |
| **鉴权** | 可选 API Key，支持 `Authorization: Bearer` 与 `X-Api-Key` |
| **并发保护** | 信号量限流（默认 1024）+ 超时与连接池配置 |
| **网络** | HTTP/HTTPS 代理、强制 HTTP/1.1、600 秒首包超时 |
| **可观测** | 请求级分阶段调试日志、上游错误原文透传、错误码到 HTTP 状态映射 |
| **零构建依赖** | 无 protoc、无生成代码；唯一第三方依赖 `gopkg.in/yaml.v3` |

## 架构

![devin-2api lite 架构图](docs/architecture.png)

一次请求：

1. `POST /v1/responses`（或 `/v1/chat/completions`）进入 `internal/app`，经过并发信号量与可选鉴权；
2. 协议包把 JSON 解码成 `llm.RequestMessages`（系统提示、消息历史、工具定义）；
3. Devin 适配器组装 `GetChatMessageRequest`：客户端身份、随机设备指纹、固定采样参数、当前轮图片；
4. 手写 Connect 客户端把请求包成 5 字节长度前缀帧，逐帧读回响应；
5. 响应帧被翻译成 12 类中间事件，再由协议编码器写成 typed SSE 或聚合 JSON。

> 图的生成脚本是 [`docs/architecture.py`](docs/architecture.py)；上游字段表与帧格式见
> [`docs/upstream-protocol.md`](docs/upstream-protocol.md)。

## 快速开始

### 1. 获取 Devin token

devin-2api 使用你的 Devin 会话 token 向上游鉴权。macOS 下可从 Devin 应用本地状态提取：

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

输出形如 `devin-session-token$...`。

> [!WARNING]
> token 等同于你的账户凭据。它只应保存在本地 `config.yaml`（已被 `.gitignore` 忽略），
> 不要提交、不要发给他人、不要粘贴到公开 issue。

### 2. 配置

```bash
git clone https://github.com/Green-hats/devin-2api-lite.git
cd devin-2api-lite
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，至少填入 `devin.token`。`base_url` 与 `model` 已有可用示例值。

### 3. 启动

从源码：

```bash
go run ./cmd/devin-2api -config config.yaml
```

或者编译成单二进制：

```bash
go build -trimpath -ldflags="-s -w" -o devin-2api ./cmd/devin-2api
./devin-2api -config config.yaml
```

Docker：

```bash
docker build -t devin-2api-lite .
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  devin-2api-lite --config /app/config.yaml
```

打 `v*` tag 后，GitHub Actions 会自动构建多架构镜像并推送 GHCR（见
[`release.yml`](.github/workflows/release.yml)）。

### 4. 验证

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## 接入客户端

任何支持自定义 Base URL 的 OpenAI 兼容客户端都可以直连：

| 配置项 | 值 |
| --- | --- |
| Base URL | `http://localhost:8080/v1` |
| API Key | `auth.api_key` 的值；若留空，随便填一个非空字符串 |
| Model | `glm-5-2`（或 `/v1/models` 返回的任意 uid） |

<details>
<summary><b>OpenAI Python SDK</b></summary>

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="anything")

stream = client.chat.completions.create(
    model="glm-5-2",
    messages=[{"role": "user", "content": "用一句话解释什么是反向代理"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

</details>

<details>
<summary><b>curl：流式 Responses</b></summary>

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
<summary><b>Codex CLI / IDE 插件</b></summary>

在客户端里选择 OpenAI 兼容 / Responses 类型的 provider，把 Base URL 指向
`http://localhost:8080/v1`。不同客户端填写字段的位置不同，以各自文档为准；
若客户端会发送 `store`、`metadata` 等 Responses 扩展字段，本网关会忽略它们。

</details>

## API 兼容性

| 端点 | 方法 | 流式 | 说明 |
| --- | --- | --- | --- |
| `/v1/chat/completions` | POST | SSE | OpenAI Chat Completions，支持 `tools`、`stream_options.include_usage` |
| `/v1/responses` | POST | typed SSE | OpenAI Responses，支持 `input`、`instructions`、`tools` |
| `/v1/models` | GET | — | 模型目录，来自上游 `GetCascadeModelConfigs` |
| `/v1/models/{model}` | GET | — | 单个模型 |
| `/healthz` | GET | — | 探活，不经过鉴权与并发限制 |

请求体上限 32 MiB（给 base64 图片留足空间）；未知字段会被忽略。

## 配置

配置文件在启动时一次性加载，未知字段会被拒绝。

| 字段 | 说明 | 必填 |
| --- | --- | --- |
| `server.listen` | HTTP 监听地址 | 是 |
| `server.max_concurrency` | 同时处理的 `/v1/*` 请求数上限，默认 1024 | 否 |
| `devin.base_url` | Devin Connect 服务地址，示例 `https://server.codeium.com` | 有 token 时是 |
| `devin.token` | Devin 会话 token；为空时 `/v1/*` 不可用 | 否 |
| `devin.model` | 默认模型 UID；请求里的 `model` 优先 | 有 token 时是 |
| `devin.proxy` | 可选 HTTP/HTTPS 代理，留空走系统代理环境变量 | 否 |
| `devin.force_http1` | 强制 HTTP/1.1，默认 true | 否 |
| `debug.enabled` | 每个请求写调试日志到 `logs/` | 否 |
| `auth.api_key` | `/v1/*` 的访问密钥，留空不校验 | 否 |

`force_http1` 默认开启：上游对 HTTP/2 单连接的多 stream 复用会串行处理，导致并发时首字延迟飙升。
改成 HTTP/1.1 后每个请求走独立连接，行为与 Devin 客户端多窗口一致。

调试日志目录：

```text
logs/<时间>/
├── 01-http-request.json     原始 HTTP 请求
├── 02-request-messages.json 中间语义模型
├── 03-devin-request.json    发往上游的请求投影
├── 04-devin-response.jsonl  上游原始帧投影
├── 05-response-events.jsonl 中间事件流
├── 06-http-response.jsonl   写回客户端的 SSE / JSON
├── error.json               首个失败阶段
└── meta.json                状态码、模型、耗时
```

## 和原版的区别

| 能力 | 原版 | lite |
| --- | --- | --- |
| `/v1/chat/completions` | ✅ | ✅ |
| `/v1/responses` | ✅ | ✅ |
| `/v1/models` | ✅ | ✅ |
| `/v1/messages`（Anthropic） | ✅ | ❌ |
| Responses WebSocket 传输 | ✅ | ❌ |
| `/panel` 管理面板 | ✅ | ❌ |
| 分阶段调试日志 | ✅ | ✅（精简） |
| 代理 | HTTP/HTTPS/SOCKS5 | HTTP/HTTPS |
| 上游接入 | protoc 生成 Connect 绑定 | 手写 wire 编解码 + Connect 帧 |
| 构建前置步骤 | `task generate` | 无 |
| 第三方依赖 | connect、chi、gorilla、protobuf、x/net、yaml | yaml |
| 生产代码行数 | 8,851 | 5,588 |

设计细节：

- **工具说明不进 Schema**：上游会把 `description`/`title` 当作工具分类提示，适配器剥离注解，
  把说明改写为 `<tool name="...">` 段落注入 system prompt；
- **图片只在当前轮**：历史图片以 `[Image omitted from history]` 占位，避免上游 `invalid_argument`；
- **字段号集中管理**：`internal/adapter/devin/types.go` 只保留实际读写的字段，
  未知字段解码时跳过，上游新增字段通常不需要改代码。

## 项目结构

```text
.
├── cmd/devin-2api/          入口：配置 → 适配器 → HTTP 服务
├── internal/
│   ├── adapter/devin/       上游适配：wire.go / types.go / 请求组装 / 帧解码
│   ├── api/openai/          chat 与 responses 两套 HTTP 编解码
│   ├── app/                 路由、请求生命周期、错误映射
│   ├── connect/             Connect 协议最小实现（流式帧 + 一元调用）
│   ├── llm/                 供应商无关的中间模型
│   ├── config/              YAML 加载与校验
│   ├── debuglog/            请求级调试日志
│   └── httpproxy/           代理与连接池
├── docs/
│   ├── architecture.png     架构图
│   ├── architecture.py      架构图生成脚本
│   ├── banner.png           README 横幅
│   └── upstream-protocol.md 上游字段表与升级流程
├── config.example.yaml
├── Dockerfile
├── NOTICE                   来源与衍生说明
└── LICENSE
```

## 开发

```bash
go test ./...     # 单元测试 + 假上游端到端测试
go vet ./...
gofmt -l .
```

测试重点：

- **字段号锁定**：`internal/adapter/devin/devin_test.go` 断言请求编码与响应解码的字段；
- **事件状态机**：文本、思考、工具参数增量、停止原因、用量；
- **协议层**：Connect 帧、错误信封、gzip、截断流（`internal/connect/connect_test.go`）；
- **端到端**：`httptest` 假冒 Devin 上游，跑通「HTTP → 适配器 → 帧 → SSE/JSON」
  （`internal/app/app_upstream_test.go`）。

上游协议升级时，按 [`docs/upstream-protocol.md`](docs/upstream-protocol.md) 的流程在原项目中
重新提取 descriptor，再修改本仓库的字段表并跑测试。

## Roadmap

- [x] Chat Completions / Responses / Models
- [x] 手写 protobuf wire codec + Connect 客户端
- [x] 假上游端到端测试
- [ ] Anthropic `/v1/messages`
- [ ] Responses WebSocket 传输
- [ ] SOCKS5 代理
- [ ] 模型价格 / 用量面板
- [ ] 与上游 `all-protos.proto` 的字段自动校验

## FAQ

<details>
<summary><b>返回 401 或 <code>Invalid token</code>？</b></summary>

token 过期、复制不完整，或误加了 `Bearer ` 前缀。重新从 Devin 应用提取，确保是完整的
`devin-session-token$...`。若使用 `auth.api_key`，客户端的 Key 也要与之匹配。

</details>

<details>
<summary><b>返回 502 / 上游错误？</b></summary>

网关会原样透传上游错误文案。打开 `debug.enabled`，查看 `error.json` 与
`04-devin-response.jsonl` 定位是哪一跳失败。常见原因：token 失效、模型 uid 不存在、并发被限。

</details>

<details>
<summary><b>为什么 <code>temperature</code> / <code>max_tokens</code> 不生效？</b></summary>

上游采样参数固定为 `max_tokens=128000`、`temperature=1`、`top_k=40`、`top_p=0.95`，
本网关只做协议转换，不调整这些值。

</details>

<details>
<summary><b>支持图片吗？</b></summary>

支持当前轮图片的 base64 data URL。历史图片会以文本占位；无视觉能力的模型会在本地直接拒绝，
避免把无效请求发给上游。

</details>

<details>
<summary><b>能替客户端执行工具吗？</b></summary>

不能。网关只负责一轮模型响应，工具调用会以标准 `tool_calls` 返回，由客户端执行后把结果
作为下一轮消息传回。

</details>

## 已知限制

- 这是对 Devin 客户端的**非官方**协议模拟，上游随时可能变更；变更后需重新提取字段。
- 生成参数固定，客户端的 `temperature` / `max_tokens` 不生效。
- 图片只挂在最新一轮；历史图片以占位文本替代。
- 不支持 SOCKS5；需要时请让本地代理提供 HTTP 端口。
- 没有面板、WebSocket、Anthropic 协议（见 Roadmap 与原项目）。

## 免责声明

本项目与 Devin、Windsurf、Codeium、Cognition 没有任何关联，也未获其授权或支持。
它通过用户自行提供的会话 token 访问上游服务，**可能违反上游的服务条款**。
请仅用于学习与研究，自行评估并承担账号、数据与合规风险；不要将本项目用于商业转售、
批量薅取额度或任何违反上游条款的场景。

## 来源与致谢

**本项目不是从零实现的。** 协议逆向、字段语义、适配细节与大量代码都来自
[**leookun/devin-2api**](https://github.com/leookun/devin-2api)（MIT，Copyright © 2026 leookun）：

| 部分 | 来源 |
| --- | --- |
| `internal/llm/` | 原项目，仅修改 import path |
| `internal/api/openai/{chat,responses}/` | 原项目，仅修改 import path |
| `internal/api/common/` | 原项目，仅修改 import path |
| `internal/app/`、`config/`、`debuglog/` | 原项目，裁剪面板 / WebSocket / Anthropic |
| `internal/adapter/devin/` 请求组装、工具说明处理、帧解码 | 原项目，改为适配手写类型 |
| 协议字段与 descriptor 提取思路 | 原项目 `cmd/protoextract` 与 `outputs/devin-proto/` |
| `internal/connect/`、`wire.go`、`types.go` | 本仓库新写 |
| 端到端测试、架构图、README | 本仓库新写 |

特别感谢原作者完成了最困难的部分：从 Devin 语言服务器二进制中扫描 `FileDescriptorProto`、
重建 63 个 proto 文件、还原 Connect 帧与结束信封，并把这些经验以 MIT 协议开放出来。
没有原项目，就没有这个仓库。

上游协议属于 Devin / Windsurf / Codeium；本仓库仅包含为互操作而实现的最小字段子集。
完整的来源与衍生说明见 [NOTICE](NOTICE)。

## License

[MIT](LICENSE) © 2026 leookun（原项目）与 Green-hats（lite 版修改）。
