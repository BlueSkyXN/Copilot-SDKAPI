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

这里要特别注意：`/v1/models` 当前是**直接透传当前 Copilot SDK / CLI 运行时列出来的模型集合**，网关本身没有再做隐藏 allowlist，也没有额外补全 Web / LMAPI 上可能出现的其它模型 ID。  
所以如果你发现某些网页端或其它入口可见的模型（例如某些 preview / internal / experiment 名称）没有出现在这里，差异通常来自**上游 SDK / CLI 的枚举结果**，不是这个网关在额外过滤。

补充一点：当前如果你在请求体里**手动填写一个没出现在 `/v1/models` 的 model ID**，纯文本请求仍会直接传给上游 SDK / CLI。  
但如果这个请求同时用了 `reasoning_effort` 或图片输入，网关默认会先返回 `400`，明确提示它无法为未知模型做能力校验；这时你要么换成 `/v1/models` 里已识别的模型，要么先让上游把该模型列出来。  
如果你显式提供了 `x_copilot.provider`，网关会把这类能力判断委托给你指定的上游 provider，而不是继续拿默认 `/v1/models` 目录硬拦。

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
- 请求标准 tools 并行执行的 `parallel_tool_calls=true`
- `structured_outputs=true`
- `response_format.json_schema.strict=true`
- `adaptive_thinking`
- `thinking_budget`
- OpenRouter `reasoning.max_tokens`
- OpenRouter `reasoning.exclude`

其中 `stream_options.include_usage=true` 是已经接通的例外：当 `stream=true` 时，网关会在 `[DONE]` 之前额外发出一个标准 OpenAI usage chunk，`choices` 为空数组，`usage` 中带 token 统计。

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

#### 标准 OpenAI / Claude tools 桥接

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

Claude 顶层 `tools[]` 现在也会桥接到同一条 continuation 通道，因此同样需要 `stream=true` 与 `X-Session-ID`：

```bash
curl http://127.0.0.1:38095/v1/messages \
  -H "Authorization: Bearer test-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "X-Session-ID: claude-tool-demo-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Look up issue 123 and summarize it."}
    ],
    "tools": [
      {
        "name": "lookup_issue",
        "description": "Fetch issue details",
        "input_schema": {
          "type": "object",
          "properties": {
            "id": {"type": "string"}
          },
          "required": ["id"]
        }
      }
    ]
  }'
```

注意：这里桥接的是**输入侧 tools 定义**。当前网关在真正进入 tool continuation 时，仍然沿用自己的 `/v1/copilot/respond` 续跑协议，而不是伪装成标准无状态 `tool_calls` / `tool_use` 循环。

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

如果你希望在流式末尾拿到标准 usage chunk，可加上：

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "stream": true,
    "stream_options": {"include_usage": true},
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

如果你走的是 OpenRouter 风格请求，也可以传：

```json
{
  "model": "gpt-4.1",
  "reasoning": {"effort": "high"},
  "messages": [{"role": "user", "content": "Think carefully and answer."}]
}
```

当前只支持把 `reasoning.effort` 映射到同一路径；`reasoning.max_tokens`、`reasoning.exclude` 等还没有对应的 Copilot SDK 上游通道，因此会明确返回 `400 unsupported_feature`，而不是静默忽略。

如果上游实际返回了 reasoning，OpenAI 非流式响应现在也会在标准位置镜像它：

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "Final answer",
        "reasoning": "intermediate reasoning text"
      }
    }
  ]
}
```

如果上游实际输出了 reasoning delta，OpenAI 流式响应现在也会在标准 chunk 里镜像它：

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion.chunk",
  "choices": [
    {
      "index": 0,
      "delta": {
        "reasoning": "intermediate reasoning token"
      },
      "finish_reason": null
    }
  ]
}
```

`x_copilot` 流式事件仍然保留，用于 richer runtime events；Claude 风格的 `thinking` / runtime payload 也仍通过 `x_copilot` 扩展暴露。

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

对于持久会话，`model`、`system` / system prompt、`x_copilot.system_message_mode`、`x_copilot.agent`、`x_copilot.interactive`、`x_copilot.tools`、`x_copilot.provider`，以及 `reasoning_effort` 这类会影响会话配置的字段需要保持一致；否则会返回会话规格冲突错误。
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
    "provider": {
      "type": "openai",
      "wire_api": "responses",
      "base_url": "https://api.openai.com/v1",
      "api_key": "sk-demo"
    },
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
- `include_reasoning`：额外在非流式响应的 `x_copilot.reasoning` 中返回 reasoning；流式时仍会补充 `x_copilot` reasoning 事件。OpenAI 标准 `message.reasoning` / `delta.reasoning` 镜像不依赖这个开关
- `include_runtime_events`：在非流式响应的 `x_copilot.runtime_events` 返回运行时事件；流式时输出额外的 `x_copilot` 事件帧
- `interactive`：开启 northbound interactive bridge；要求 `stream=true`，并且初始请求必须携带 `X-Session-ID`
- `permission_mode`：`inherit`（默认，跟随 `GATEWAY_SDK_PERMISSION_MODE`）、`deny`、`bridge`、`allow`；但请求值不能比服务端 `GATEWAY_SDK_PERMISSION_MODE` 更宽松。`bridge` 会把权限请求桥接到 `/v1/copilot/respond`
- `provider`：透传官方 SDK 支持的 custom provider / BYOK 配置。当前支持 `type=openai|azure|anthropic`，`base_url` 必填；`wire_api` 仅适用于 openai/azure（默认 `completions`），`type` 省略时默认 `openai`
- 安全边界：官方 trusted provider host（`api.openai.com`、`api.anthropic.com`、`*.openai.azure.com`）默认允许；任意其它自定义 hostname 目前不支持。如果你要连 `localhost`、私网或 special-use 地址（如本地 Ollama / vLLM），请直接使用 IP literal，并额外设置 `GATEWAY_ALLOW_PRIVATE_REMOTE_URLS=true`
- `tools`：注册 SDK-native custom tools；OpenAI / Claude 顶层标准 tools 当前都会桥接到这里，但 continuation 仍不伪装成标准无状态 `tool_calls` / `tool_use`
- `attachments`：上传附加文件，支持 inline `text`、base64 `data` 或远程 `url`

如果显式提供了 `x_copilot.provider`，网关会把 `reasoning_effort` / 图片输入这类能力判断委托给该 provider，而不是继续依赖默认 `/v1/models` 目录。因此 `/v1/models` 仍只代表当前默认 Copilot SDK / CLI 运行时视角，不会自动枚举你临时指定的外部 provider 模型目录。

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

OpenAI 流式时，标准 `data:` chunk 之外还会夹带带有 `x_copilot.event` 的 chunk；如果请求了 `stream_options.include_usage=true`，结束前还会再补一个标准 usage chunk。Claude 流式时会额外发送 `event: x_copilot` 的 SSE 帧。

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
