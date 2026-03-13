# Copilot SDK API Gateway

基于官方 `copilot-sdk` Go SDK 实现的兼容网关，对外提供：

- OpenAI 风格 `POST /v1/chat/completions`
- Claude 风格 `POST /v1/messages`
- OpenAI 风格 `GET /v1/models`
- `GET /healthz`

对内统一走官方 Copilot SDK，会话、流式输出、模型列表和 CLI 生命周期都由 Go 版 SDK 承载。

这套网关当前已经可以覆盖一条完整的实践链路：

1. 准备 Copilot 凭据（`GATEWAY_GITHUB_TOKEN` 或复用本机已登录用户）
2. 启动网关并暴露 northbound API Key
3. 通过 OpenAI / Claude 兼容接口完成文本、图片、会话复用、interactive/tool/permission bridge 等调用

> 注意：网关本身**不提供独立的 northbound 登录接口**。这里的“登录”指的是网关进程连接官方 Copilot SDK 时使用的 GitHub / Copilot 凭据准备过程。

## 文档导航

- 想最快跑通：先看 [5 分钟上手：从认证到第一次调用](#5-分钟上手从认证到第一次调用)
- 想确认能力边界：看 [当前实现特性](#当前实现特性) 与 [当前限制](#当前限制)
- 想配环境：看 [环境变量](#环境变量)
- 想直接抄请求：看 [API 示例](#api-示例)
- 想做 interactive / tools / permission：看 [`x_copilot` 扩展能力](#9-x_copilot-扩展能力)

## 5 分钟上手：从认证到第一次调用

### 第 1 步：准备 Copilot 认证

网关启动时需要能代表你访问官方 Copilot SDK。当前支持两种常用方式：

#### 方式 A：直接提供 Token

最适合服务器、CI、容器或你希望显式控制凭据来源的环境：

```bash
export GATEWAY_GITHUB_TOKEN=gho_xxx
```

如果没有设置 `GATEWAY_GITHUB_TOKEN`，程序还会继续尝试：

1. `COPILOT_GITHUB_TOKEN`
2. `GH_TOKEN`
3. `GITHUB_TOKEN`
4. 已登录用户凭据（取决于 `GATEWAY_USE_LOGGED_IN_USER`）

#### 方式 B：复用本机已登录用户

最适合本地开发和快速试跑。只要你的本机环境已经具备可用的 GitHub / Copilot 登录状态，就可以让网关直接复用：

```bash
export GATEWAY_USE_LOGGED_IN_USER=true
```

如果你同时设置了 `GATEWAY_GITHUB_TOKEN`，则优先走 token。

### 第 2 步：设置网关自己的 northbound API Key

这是你的应用访问本网关时使用的 Bearer Key，不是 GitHub Token：

```bash
export GATEWAY_API_KEYS=test-key
```

### 第 3 步：启动网关

```bash
cd /Users/sky/Github/Copilot-SDKAPI
go run ./cmd/gateway
```

最小可运行组合通常是：

```bash
export GATEWAY_API_KEYS=test-key
export GATEWAY_DEFAULT_MODEL=gpt-4.1
export GATEWAY_USE_LOGGED_IN_USER=true
go run ./cmd/gateway
```

如果你更希望显式指定 token，则把 `GATEWAY_USE_LOGGED_IN_USER=true` 换成 `GATEWAY_GITHUB_TOKEN=...`。

### 第 4 步：先验证网关是否正常

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer test-key"
```

`/healthz` 返回 `ok`，`/v1/models` 能列出模型时，就说明从认证到 SDK 连接这条链路已经打通。

### 第 5 步：发出第一次 OpenAI 兼容请求

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1",
    "messages": [
      {"role": "user", "content": "Say hello in one sentence."}
    ]
  }'
```

### 第 6 步：继续练习的推荐顺序

1. 先试 `/v1/chat/completions` 与 `/v1/messages`
2. 再试 `X-Session-ID` 持久会话复用
3. 再试图片输入与 `x_copilot.attachments`
4. 最后再试 `x_copilot.tools`、`x_copilot.interactive`、`x_copilot.permission_mode`

这样最容易把“登录 → 启动 → 基础调用 → 持久会话 → 进阶交互”一条线跑通。

## 当前实现特性

- 静态 API Key 鉴权
- OpenAI / Claude 文本对话兼容
- OpenAI / Claude 图片输入兼容（仅最新一条用户消息；支持 inline 与远程 URL 拉取，默认拒绝私网与 special-use 目标）
- OpenAI SSE 流式输出
- Claude SSE 流式输出
- `reasoning_effort` 控制透传到 Copilot SDK
- 通过 `x_copilot` 扩展输出 reasoning 与运行时事件（tool / permission / compaction / agent 等）
- `/v1/models` 返回可忽略的 `x_copilot` 扩展元数据，包含 vision / reasoning / limits
- 基于 `X-Session-ID` 的持久会话复用，可跨网关重启恢复
- 默认拒绝所有工具权限请求；服务端可显式配置为 `bridge` 或 `allow`，请求侧只能在该上限内选择更严格模式
- 可通过环境变量接入 Copilot SDK 的 Skills / MCP / 工具白名单 / custom agents / infinite sessions 配置
- 可通过 `x_copilot.agent` 与 `x_copilot.system_message_mode` 选择 custom agent 或切换 system message append / replace
- 可通过 `x_copilot.tools`、`x_copilot.interactive`、`x_copilot.permission_mode` 与 `/v1/copilot/respond` 暴露 SDK-native tools / ask_user / permission 交互能力
- 可通过 `x_copilot.attachments` 上传通用文件附件
- Release 构建时自动通过 bundler 为目标平台生成 embedded Copilot CLI

## 当前限制

- 标准 OpenAI / Claude 顶层 `tools` / function calling 字段仍不兼容；请改用 `x_copilot.tools` + `stream=true` + `/v1/copilot/respond`
- 暂不支持 `max_tokens`、`temperature`、`top_p` 等 generation controls；传入会返回 `400 unsupported_feature`
- reasoning / runtime events 当前通过 `x_copilot` 扩展暴露，不伪装成标准 OpenAI / Claude thinking 协议
- OpenAI / Claude 图片输入当前只支持**最新一条用户消息**中的图片内容；更早轮次的图片会被拒绝，避免静默丢失
- fresh session 下历史图片仍无法无损重建为原始多轮附件语义，因此不会静默接受更早轮次图片
- tool / ask_user / permission northbound 交互当前要求 `stream=true`，且初始请求必须带 `X-Session-ID`
- 同一会话内 `ask_user` 与 permission continuation 目前按类型串行；如果 SDK 在同一会话里并发抛出第二个同类请求，网关会显式拒绝该第二个请求，避免 FIFO 错配
- `elicitation.requested`、`exit_plan_mode.requested`、`command.queued` 事件目前仍只能作为 runtime event 观察，尚未桥成 northbound continuation；当前 Go SDK 公共 typed RPC 未暴露对应 responder
- embedded CLI 不在仓库中提交，由 CI release 流程自动为各目标平台生成；本地开发通过 `GATEWAY_CLI_PATH` / `COPILOT_CLI_PATH` / PATH 中的 `copilot` 运行

## 认证与运行前提

依赖通过 `go mod download` 自动拉取（`copilot-sdk` 为公开模块）。

运行时仍需要保证：

1. 有可用的 GitHub / Copilot 认证方式
2. 有至少一个外部 API Key 用于 northbound 访问

## 环境变量

最少需要：

```bash
GATEWAY_API_KEYS=test-key
```

常用变量如下：

```bash
GATEWAY_LISTEN_ADDR=:8080
GATEWAY_API_KEYS=test-key,internal=another-key
GATEWAY_DEFAULT_MODEL=gpt-4.1
GATEWAY_WORKING_DIR=/absolute/path/to/workspace
GATEWAY_CONFIG_DIR=/absolute/path/to/copilot-config
GATEWAY_REQUEST_TIMEOUT=2m
GATEWAY_STREAM_IDLE_TIMEOUT=2m
GATEWAY_SESSION_TTL=15m
GATEWAY_SESSION_STORE_PATH=/absolute/path/to/gateway-sessions.json
GATEWAY_ALLOW_PRIVATE_REMOTE_URLS=false
GATEWAY_MAX_ACTIVE_SESSIONS=256
GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY=64
GATEWAY_LOG_LEVEL=error
GATEWAY_CLIENT_NAME=copilot-sdkapi
GATEWAY_CLI_PATH=/absolute/path/to/copilot
GATEWAY_GITHUB_TOKEN=gho_xxx
GATEWAY_USE_LOGGED_IN_USER=false
GATEWAY_SDK_AVAILABLE_TOOLS=view,edit
GATEWAY_SDK_EXCLUDED_TOOLS=shell
GATEWAY_SDK_SKILL_DIRECTORIES=/opt/skills/review,/opt/skills/docs
GATEWAY_SDK_DISABLED_SKILLS=legacy-skill
GATEWAY_SDK_MCP_SERVERS_JSON={"filesystem":{"type":"local","command":"node","args":["./mcp.js"],"tools":["*"]}}
GATEWAY_SDK_CUSTOM_AGENTS_JSON=[{"name":"reviewer","displayName":"Reviewer","prompt":"Review carefully.","tools":["view","bash"]}]
GATEWAY_SDK_DEFAULT_AGENT=reviewer
GATEWAY_SDK_PERMISSION_MODE=deny
GATEWAY_SDK_INFINITE_SESSIONS_ENABLED=true
GATEWAY_SDK_INFINITE_SESSIONS_BACKGROUND_COMPACTION_THRESHOLD=0.80
GATEWAY_SDK_INFINITE_SESSIONS_BUFFER_EXHAUSTION_THRESHOLD=0.95
```

如果不设置 `GATEWAY_CLI_PATH`，SDK 的 CLI 解析顺序是：

1. `COPILOT_CLI_PATH`
2. embedded CLI
3. `copilot`（PATH 中可执行文件）

如果不设置 `GATEWAY_GITHUB_TOKEN`，SDK 会继续尝试：

1. `COPILOT_GITHUB_TOKEN`
2. `GH_TOKEN`
3. `GITHUB_TOKEN`
4. 已登录用户凭据（取决于 `GATEWAY_USE_LOGGED_IN_USER`）

超时语义如下：

- `GATEWAY_REQUEST_TIMEOUT`：所有请求的总超时，同时也作为 HTTP 请求体读取超时与 keep-alive 空闲超时的上界
- `GATEWAY_STREAM_IDLE_TIMEOUT`：仅流式请求的空闲超时；只要持续有事件输出，就不会因为该值而中断
- `GATEWAY_SESSION_STORE_PATH`：`X-Session-ID` 持久会话元数据文件；默认落到用户配置目录下的 `copilot-sdkapi/sessions.json`
- `GATEWAY_ALLOW_PRIVATE_REMOTE_URLS`：是否允许远程图片 / 附件抓取访问私网、回环和其他 special-use 地址；默认 `false`，仅建议本地开发或受信任内网开启
- `GATEWAY_MAX_ACTIVE_SESSIONS`：进程内允许同时保留的持久会话总数上限；超过后新的持久会话会被拒绝
- `GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY`：单个 northbound API Key 同时保留的持久会话上限；超过后该 API Key 新建持久会话会收到限流响应
- `GATEWAY_SDK_AVAILABLE_TOOLS` / `GATEWAY_SDK_EXCLUDED_TOOLS`：控制 Copilot SDK 会话中允许或排除的工具集合
- `GATEWAY_SDK_SKILL_DIRECTORIES` / `GATEWAY_SDK_DISABLED_SKILLS`：将 Copilot SDK skills 以全局配置方式加载到网关创建的会话中
- `GATEWAY_SDK_MCP_SERVERS_JSON`：将 MCP server 配置以 JSON 方式传给 Copilot SDK；建议仅在受信任的网关部署环境中使用
- `GATEWAY_SDK_CUSTOM_AGENTS_JSON` / `GATEWAY_SDK_DEFAULT_AGENT`：注册全局 custom agents，并指定默认 agent；请求也可以通过 `x_copilot.agent` 覆盖默认值
- `GATEWAY_SDK_PERMISSION_MODE`：全局权限策略，支持 `deny`（默认）、`bridge`、`allow`；请求侧的 `x_copilot.permission_mode` 不能比该值更宽松。`bridge` 允许把权限请求桥接到 `/v1/copilot/respond`，`allow` 会自动批准 Copilot 发起的 shell / 文件 / MCP 等权限请求，仅建议在可信环境启用
- `GATEWAY_SDK_INFINITE_SESSIONS_*`：显式控制 SDK infinite sessions 开关与压缩阈值；不设置时沿用 SDK 默认值

## 安全边界

`Authorization: Bearer <gateway-api-key>` 是一个受信任的 northbound 集成凭据，而不是细粒度终端用户权限模型。

- 默认配置下，网关仍会拒绝 Copilot 发起的权限请求。
- 一旦启用 `GATEWAY_SDK_PERMISSION_MODE=bridge` 或 `allow`，并实际批准了权限，请把该 API Key 视为可代表调用方使用该网关工作区、skills、MCP、工具配置的高权限凭据。
- 建议仅在受信任内网、反向代理、或你自己控制的应用后面暴露此网关。

## 本地运行补充

如果你已经按上面的 5 分钟上手完成认证准备，这里给一个更明确的本地启动模板：

```bash
cd /Users/sky/Github/Copilot-SDKAPI
GATEWAY_API_KEYS=test-key \
GATEWAY_DEFAULT_MODEL=gpt-4.1 \
go run ./cmd/gateway
```

## API 示例

### 1. 健康检查

```bash
curl http://127.0.0.1:8080/healthz
```

### 2. 列模型

```bash
curl http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer test-key"
```

返回中的每个模型项除标准字段外，还会包含可忽略的 `x_copilot` 扩展元数据，例如 `supports.vision`、`supports.reasoning_effort`、`limits.max_context_window_tokens`、`supported_reasoning_efforts`。

### 3. OpenAI 非流式

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
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

### 4. OpenAI 流式

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
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
curl http://127.0.0.1:8080/v1/messages \
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
curl http://127.0.0.1:8080/v1/chat/completions \
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
curl http://127.0.0.1:8080/v1/messages \
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
curl http://127.0.0.1:8080/v1/chat/completions \
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
curl http://127.0.0.1:8080/v1/chat/completions \
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
curl http://127.0.0.1:8080/v1/copilot/respond \
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

## 构建与测试

```bash
go test ./...
go build ./...
```

## GitHub Actions 自动化

仓库已补充两条 GitHub Actions 流水线：

- `ci.yml`：在 `push`、`pull_request`、`workflow_dispatch`、`merge_group` 时自动执行 `gofmt` 检查、`go test ./...`、Linux 定向 `-race` 测试、`go build ./...`，并在 Linux runner 上交叉编译发布目标平台
- `release.yml`：在推送 `v*` tag 时先执行 `go test ./...` 与定向 `-race` 校验，再为各目标平台动态生成 embedded Copilot CLI 后构建并打包 `linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`，生成 `sha256` 校验文件并发布到 GitHub Release；手动触发时则产出 workflow artifact 方便预演

两个 workflow 均通过 `go mod download` 拉取依赖，版本由 `go.sum` 锁定，保证构建可复现。

## 重新生成 embedded CLI

仓库不提交 embedded CLI 二进制，release workflow 会自动为各目标平台调用 bundler 生成。

本地开发如需手动生成（例如调试 embedded 路径），可执行：

```bash
go tool bundler --platform darwin/arm64 --output ./internal/embeddedcli
```

生成的 `.zst`、`.license` 和平台 Go 文件已在 `.gitignore` 中排除，不会被意外提交。
