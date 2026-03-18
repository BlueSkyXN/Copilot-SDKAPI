# API 示例

> 下列示例默认使用本地试用地址 `http://127.0.0.1:38095` 和默认 API key `test-key`。如果你显式覆盖了端口或 `GATEWAY_API_KEYS`，请同步替换示例中的值。

### 1. 健康检查

```bash
curl http://127.0.0.1:38095/healthz
```

### 2. 列模型

```bash
curl http://127.0.0.1:38095/v1/models \
  -H "Authorization: Bearer test-key"
```

返回中的每个模型项除标准字段外，还会包含可忽略的 `x_copilot` 扩展元数据，例如 `supports.vision`、`supports.reasoning_effort`、`limits.max_context_window_tokens`、`supported_reasoning_efforts`。

### 3. OpenAI 非流式

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {"role": "system", "content": "Be concise."},
      {"role": "user", "content": "Say hello in one sentence."}
    ]
  }'
```

### 3.1 OpenAI 兼容补充

#### 常见 controls 的当前语义

OpenAI Chat Completions 的常见标准字段现在会以**兼容模式**接受，例如 `max_tokens`、`max_completion_tokens`、`temperature`、`top_p`、`presence_penalty`、`frequency_penalty`、`response_format`、`stream_options`、`metadata` 等。

当前如果底层 Copilot SDK 没有一一对应能力，这些字段会按 **no-op** 处理，而不是直接 `400`。但以下情况仍会显式拒绝：

- `n != 1`
- 请求音频输出（例如 `modalities` 包含非 `text`，或 `audio` 非空）

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "temperature": 0.2,
    "max_tokens": 128,
    "top_p": 0.9,
    "metadata": {"source": "demo"},
    "messages": [
      {"role": "user", "content": "Say hello in one sentence."}
    ]
  }'
```

#### 标准 `tools` / `functions` 桥接

OpenAI 顶层 `tools[]` 与 legacy `functions[]` 定义现在会桥接到现有的 `x_copilot.tools` 运行时通道。  
但如果模型真的发起 tool / ask_user / permission continuation，客户端仍需要沿用当前网关的 `stream=true` + `X-Session-ID` + `/v1/copilot/respond` 模式。

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "X-Session-ID: tool-demo-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Look up issue 123 and summarize it."}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "lookup_issue",
          "description": "Fetch issue details",
          "parameters": {
            "type": "object",
            "properties": {
              "id": {"type": "string"}
            },
            "required": ["id"]
          }
        }
      }
    ]
  }'
```

如果你只想临时禁用这些标准 tools，可直接传：

```json
{"tool_choice":"none"}
```

#### `developer` / `tool` role 与常见消息字段

OpenAI 的 `developer` role 现在会合并进 system prompt；`tool` role，以及 `name`、`tool_calls`、`tool_call_id`、`function_call`、`refusal` 这些常见消息字段也会被兼容解析，而不是直接报错。

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {"role": "developer", "content": "Follow company policy."},
      {"role": "user", "name": "alice", "content": "Need issue details."},
      {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_1",
            "type": "function",
            "function": {
              "name": "lookup_issue",
              "arguments": "{\"id\":\"123\"}"
            }
          }
        ]
      },
      {
        "role": "tool",
        "tool_call_id": "call_1",
        "name": "lookup_issue",
        "content": "Issue 123 is open."
      },
      {"role": "user", "content": "Summarize that in one sentence."}
    ]
  }'
```

### 4. OpenAI 流式

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Count to three."}
    ]
  }'
```

### 5. Claude Messages

`/v1/messages` 当前要求请求头 `anthropic-version: 2023-06-01`，缺失或使用其它版本会返回 `400`。

```bash
curl http://127.0.0.1:38095/v1/messages \
  -H "Authorization: Bearer test-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "Summarize the value of this gateway in one paragraph."}
    ]
  }'
```

### 6. 图片输入

OpenAI 侧可在**最新一条用户消息**中使用 `data:` URL 或远程 `http(s)` URL 图片块：

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "Describe this image."},
          {"type": "image_url", "image_url": {"url": "data:image/png;base64,aGVsbG8="}}
        ]
      }
    ]
  }'
```

Claude 侧可在**最新一条用户消息**中使用 `base64` 或 `url` image block：

```bash
curl http://127.0.0.1:38095/v1/messages \
  -H "Authorization: Bearer test-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "Describe this image."},
          {"type": "image", "source": {"type": "url", "url": "https://example.com/demo.png"}}
        ]
      }
    ]
  }'
```

出于安全考虑，远程 `http(s)` 图片 / 附件默认会拒绝私网、回环、benchmark/test-net、link-local、文档地址段等 special-use 目标；若确实需要在受信任环境中抓取内网资源，可显式设置 `GATEWAY_ALLOW_PRIVATE_REMOTE_URLS=true`。

### 7. Reasoning Effort

OpenAI / Claude 两个 northbound 接口都支持网关扩展字段 `reasoning_effort`，可传 `low`、`medium`、`high`、`xhigh`，前提是当前模型在 `/v1/models` 的 `x_copilot.supports.reasoning_effort` 中声明支持。

```json
{
  "model": "gpt-4.1",
  "reasoning_effort": "high",
  "messages": [{"role": "user", "content": "Think carefully and answer."}]
}
```

### 8. 会话复用

向同一会话持续发送请求时，增加 `X-Session-ID`：

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "X-Session-ID: my-session-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {"role": "user", "content": "Remember that my project codename is Aurora."}
    ]
  }'
```

后续继续使用同一个 `X-Session-ID` 时，网关会复用同一个 SDK Session，并只把最新用户消息发给 Copilot Session。会话元数据持久化到 `GATEWAY_SESSION_STORE_PATH`，因此在网关重启后仍可恢复。

对于持久会话，`model`、`system` / system prompt、`x_copilot.system_message_mode`、`x_copilot.agent`、`x_copilot.interactive`、`x_copilot.tools`，以及 `reasoning_effort` 这类会影响会话配置的字段需要保持一致；否则会返回会话规格冲突错误。
如果使用 `x_copilot.permission_mode`，它同样属于会话规格的一部分。

`X-Session-ID` 目前要求：

- 最大长度 128
- 仅允许字母、数字、`.`、`_`、`:`、`-`
- 无效格式返回 `400`
- 超过单 API Key 或全局持久会话上限时，分别返回 `429` 或 `503`

### 9. `x_copilot` 扩展能力

两个 northbound 接口都支持可选的 `x_copilot` 扩展对象：

```json
{
  "x_copilot": {
    "agent": "reviewer",
    "system_message_mode": "replace",
    "include_reasoning": true,
    "include_runtime_events": true,
    "interactive": true,
    "permission_mode": "bridge",
    "tools": [
      {
        "name": "lookup_issue",
        "description": "Fetch issue details",
        "parameters": {
          "type": "object",
          "properties": {
            "id": {"type": "string"}
          },
          "required": ["id"]
        }
      }
    ],
    "attachments": [
      {
        "name": "notes.txt",
        "text": "hello world",
        "media_type": "text/plain"
      }
    ]
  }
}
```

- `agent`：选择已通过 `GATEWAY_SDK_CUSTOM_AGENTS_JSON` 注册的 custom agent
- `system_message_mode`：`append`（默认）或 `replace`
- `include_reasoning`：在非流式响应的 `x_copilot.reasoning` 中返回 reasoning；流式时输出 `x_copilot` reasoning 事件
- `include_runtime_events`：在非流式响应的 `x_copilot.runtime_events` 返回运行时事件；流式时输出额外的 `x_copilot` 事件帧
- `interactive`：开启 northbound interactive bridge；要求 `stream=true`，并且初始请求必须携带 `X-Session-ID`
- `permission_mode`：`inherit`（默认，跟随 `GATEWAY_SDK_PERMISSION_MODE`）、`deny`、`bridge`、`allow`；但请求值不能比服务端 `GATEWAY_SDK_PERMISSION_MODE` 更宽松。`bridge` 会把权限请求桥接到 `/v1/copilot/respond`
- `tools`：注册 SDK-native custom tools；不伪装成标准 OpenAI / Claude `tool_calls`
- `attachments`：上传附加文件，支持 inline `text`、base64 `data` 或远程 `url`

OpenAI 非流式示例：

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {"role": "system", "content": "You are a strict reviewer."},
      {"role": "user", "content": "Review this change."}
    ],
    "x_copilot": {
      "agent": "reviewer",
      "system_message_mode": "replace",
      "include_reasoning": true,
      "include_runtime_events": true
    }
  }'
```

当 `include_reasoning` 或 `include_runtime_events` 开启时，返回体会额外包含 `x_copilot`：

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "choices": [...],
  "usage": {...},
  "x_copilot": {
    "agent": "reviewer",
    "system_message_mode": "replace",
    "reasoning": "reasoning trace...",
    "runtime_events": [
      {
        "type": "tool_execution_start",
        "tool_name": "view",
        "call_id": "tool-1"
      }
    ]
  }
}
```

OpenAI 流式时，标准 `data:` chunk 之外还会夹带带有 `x_copilot.event` 的 chunk；Claude 流式时会额外发送 `event: x_copilot` 的 SSE 帧。

当 `interactive=true`、声明了 `x_copilot.tools`，或解析后的 `permission_mode=bridge` 时，网关会自动强制打开运行时事件输出，用于承载 pending request。continuation 成功提交后也会刷新流式空闲计时器，避免等待人工批准时被误判为空闲：

```json
{
  "x_copilot": {
    "event": {
      "type": "external_tool_requested",
      "request_id": "req-1",
      "call_id": "tool-call-1",
      "tool_name": "lookup_issue",
      "arguments": {"id": "123"}
    }
  }
}
```

客户端收到上述事件后，可通过 continuation endpoint 回填结果：

```bash
curl http://127.0.0.1:38095/v1/copilot/respond \
  -H "Authorization: Bearer test-key" \
  -H "X-Session-ID: my-session-1" \
  -H "Content-Type: application/json" \
  -d '{
    "request_id": "req-1",
    "kind": "tool_result",
    "text_result": "Issue 123 is open",
    "result_type": "success"
  }'
```

目前 continuation `kind` 支持：

- `tool_result`
- `tool_error`
- `user_input`
- `permission_result`
