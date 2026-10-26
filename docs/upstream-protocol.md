# 上游协议速查

lite 版不再携带上游 descriptor，只实现两个 RPC 实际用到的字段。本文件记录这些字段号、
枚举值和 Connect 帧格式，升级上游时按最后一节操作。

字段来源：原仓库 `outputs/devin-proto/all-protos.proto`（从 Devin 语言服务器二进制提取的
63 个 `FileDescriptorProto`），包名 `exa.api_server_pb` / `exa.chat_pb` / `exa.codeium_common_pb`。

## 端点

| RPC | 路径 | 类型 |
| --- | --- | --- |
| `GetChatMessage` | `/exa.api_server_pb.ApiServerService/GetChatMessage` | 服务端流 |
| `GetCascadeModelConfigs` | `/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs` | 一元 |

## 请求头

```http
authorization: Basic <token>-<token>
content-type: application/connect+proto     # 流式
content-type: application/proto             # 一元
connect-protocol-version: 1
accept: application/connect+proto           # 或 application/proto
```

token 形如 `devin-session-token$...`，直接拼成 `Basic <token>-<token>`（不是 Bearer）。

## Connect 帧

流式响应的每个消息是一帧：

```text
+--------+------------------+-----------------+
| flags  | length (uint32)  | protobuf 负载    |
| 1 byte | 4 bytes 大端      | length 字节      |
+--------+------------------+-----------------+
```

- `flags & 0x01`：负载是 gzip；
- `flags & 0x02`：流结束信封，负载是 JSON：
  - 正常：`{"metadata":{}}`
  - 出错：`{"error":{"code":"resource_exhausted","message":"..."}}`

一元调用没有帧封装：请求体是裸消息，200 响应体也是裸消息；非 200 时响应体是
`{"code":"...","message":"..."}`。

## 错误码到 HTTP 状态

| Connect code | HTTP |
| --- | --- |
| `invalid_argument` | 400 |
| `unauthenticated` | 401 |
| `permission_denied` | 403 |
| `not_found` | 404 |
| `resource_exhausted` | 429 |
| 其他 | 502 |

## GetChatMessageRequest

| 字段号 | 名称 | 类型 | 本适配器的值 |
| --- | --- | --- | --- |
| 1 | `metadata` | message | 见下 |
| 2 | `prompt` | string | 系统提示 + 工具说明 |
| 3 | `chat_message_prompts` | repeated message | 完整历史 |
| 7 | `request_type` | enum | 5 = `CASCADE` |
| 8 | `configuration` | message | 固定采样参数 |
| 10 | `tools` | repeated message | 工具定义 |
| 15 | `trajectory_reference` | message | 随机 trajectory |
| 16 | `cascade_id` | string | 随机 UUID |
| 20 | `planner_mode` | enum | 1 = `DEFAULT` |
| 21 | `chat_model_uid` | string | 请求的 model |
| 22 | `execution_id` | string | 随机 UUID |

### Metadata

| 字段号 | 名称 | 值 |
| --- | --- | --- |
| 1 | `ide_name` | `chisel` |
| 2 | `extension_version` | `3000.2.17` |
| 3 | `api_key` | 同一个 token |
| 4 | `locale` | `en` |
| 5 | `os` | 对话 `mac`，模型目录 `win` |
| 7 | `ide_version` | `3000.2.17` |
| 12 | `extension_name` | `chisel` |
| 31 | `f` | 随机 366 字节的 hex（732 字符） |

### ChatMessagePrompt

| 字段号 | 名称 | 说明 |
| --- | --- | --- |
| 1 | `message_id` | 随机 UUID |
| 2 | `source` | 1 user / 2 system（助手历史）/ 4 tool |
| 3 | `prompt` | 文本正文 |
| 6 | `tool_calls` | 助手历史里的工具调用 |
| 7 | `tool_call_id` | 工具结果对应的调用 ID |
| 9 | `tool_result_is_error` | 工具是否失败 |
| 10 | `images` | 仅最新一轮 |
| 11 | `thinking` | 思考正文 |
| 12 | `signature` | 思考签名 |
| 13 | `thinking_redacted` | 思考是否被隐藏 |

### 其余嵌套消息

| 消息 | 字段 |
| --- | --- |
| `ChatToolCall` | 1 `id`、2 `name`、3 `arguments_json` |
| `ChatToolDefinition` | 1 `name`、2 `description`、3 `json_schema_string` |
| `ImageData` | 1 `base64_data`（不含 data URL 前缀）、2 `mime_type` |
| `CompletionConfiguration` | 1 `num_completions`、2 `max_tokens`、3 `max_newlines`、5 `temperature`、7 `top_k`、8 `top_p` |
| `CortexTrajectoryReference` | 1 `trajectory_id`、2 `step_index`、3 `trajectory_type`、4 `step_type` |

固定生成参数：`num_completions=1`、`max_tokens=128000`、`max_newlines=400`、
`temperature=1`、`top_k=40`、`top_p=0.95`。

## GetChatMessageResponse

| 字段号 | 名称 | 说明 |
| --- | --- | --- |
| 1 | `message_id` | 上游响应 ID |
| 2 | `timestamp` | 1 `seconds`、2 `nanos` |
| 3 | `delta_text` | 文本增量 |
| 4 | `delta_tokens` | 本帧 token 数（未使用） |
| 5 | `stop_reason` | 见枚举 |
| 6 | `delta_tool_calls` | 工具调用/参数增量 |
| 7 | `usage` | 见 `ModelUsageStats` |
| 8 | `redact` | 未使用 |
| 9 | `delta_thinking` | 思考增量 |
| 10 | `delta_signature` | 思考签名增量 |
| 11 | `thinking_redacted` | 思考是否被隐藏 |
| 12 | `latency` | 未使用 |
| 17 | `request_id` | 调试用 |
| 23 | `actual_model_uid` | 实际模型，覆盖请求里的 model |

`ModelUsageStats`：2 `input_tokens`、3 `output_tokens`、4 `cache_write_tokens`、
5 `cache_read_tokens`、6 `api_provider`、9 `model_uid`。

## 模型目录

| 消息 | 字段 |
| --- | --- |
| `GetCascadeModelConfigsRequest` | 1 `metadata` |
| `GetCascadeModelConfigsResponse` | 1 `client_model_configs` |
| `ClientModelConfig` | 2 `model_or_alias`、4 `disabled`、5 `supports_images`、10 `provider`、22 `model_uid` |
| `ModelOrAlias` | 3 `model_uid`（oneof 的字符串分支） |

## 枚举

`StopReason`：0 未指定、1 `INCOMPLETE`、3 `MAX_TOKENS`、9 `PARTIAL`、10 `FUNCTION_CALL`、13 `ERROR`。

|lite 映射|取值|
| --- | --- |
| `length` | 1、3、9 |
| `toolUse` | 10 |
| `error` | 13 |
| `stop` | 其他/未提供 |

其他枚举：

| 枚举 | 值 |
| --- | --- |
| `ChatMessageSource` | 1 user、2 system、4 tool |
| `ChatMessageRequestType` | 5 = `CASCADE` |
| `ConversationalPlannerMode` | 1 = `DEFAULT` |
| `CortexTrajectoryType` | 4 = `CASCADE` |
| `CortexStepType` | 14 = `USER_INPUT` |
| `ModelProvider` | 1 windsurf、2 openai、3 anthropic、4 google、5 xai、6 deepseek、7 moonshot、8 qwen、9 zai、10 minimax、11 nvidia、12 thinking-machines |

## 设计约束

- **无会话状态**：每次请求都带完整历史，并新生成 message/trajectory/cascade/execution ID，
  上游不保存会话，网关可以水平扩容与重放。
- **工具说明不进 schema**：上游会把 `description` / `title` 当作工具分类提示，容易误判；
  适配器剥离 schema 里的自然语言注解，把说明改写成 `<tool name="...">` 段落追加到 system prompt。
- **图片只在当前轮**：历史图片会被上游判为 `invalid_argument`，适配器改成文本占位。
- **响应是增量帧**：文本、思考、工具参数都是 delta，思考正文在块结束时才 materialize，
  工具参数在停止时一次性拼成 JSON。

## 升级流程

```bash
# 1. 在原仓库重新提取（需要 Devin 语言服务器二进制）
task extract BINARY=/Applications/Devin.app/Contents/Resources/app/extensions/windsurf/bin/language_server_macos_arm

# 2. 对比字段
diff <(grep '"name"' outputs/devin-proto/manifest.json | sort) \
     <(grep '"name"' /tmp/extract-test/manifest.json | sort)
```

3. 修改 `internal/adapter/devin/types.go` 中对应的字段号或枚举；
4. 跑 `go test ./internal/adapter/devin`——测试会锁定已实现的字段号，改错会直接失败；
5. 如果新增了整条 RPC，再补 `internal/connect` 的调用与 `internal/app` 的路由。

上游**新增**字段不需要改代码：解码器对未知字段一律跳过。只有字段号变更、语义变化或新增
必需字段时才需要适配。

## 验证技巧

- 原仓库 `outputs/exa.api_server_pb.ApiServerService/GetChatMessage/` 下有 7 组抓包样本
  （request/response 的 protojson 文本），可直接对照字段语义；
- 打开 `debug.enabled`，`03-devin-request.json` 与 `04-devin-response.jsonl` 分别是
  发往上游的请求投影与原始帧投影；
- `internal/app/app_upstream_test.go` 用 httptest 假冒上游，改动帧格式后会立刻暴露问题。
