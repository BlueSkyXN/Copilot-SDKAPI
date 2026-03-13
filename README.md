# Copilot SDK API Gateway

基于官方 `copilot-sdk` Go SDK 实现的兼容网关，对外提供：

- OpenAI 风格 `POST /v1/chat/completions`
- Claude 风格 `POST /v1/messages`
- OpenAI 风格 `GET /v1/models`
- `GET /healthz`

对内统一走官方 Copilot SDK，会话、流式输出、模型列表和 CLI 生命周期都由 Go 版 SDK 承载。

## 当前实现特性

- 静态 API Key 鉴权
- OpenAI / Claude 文本对话兼容
- OpenAI / Claude 图片输入兼容（仅最新一条用户消息，安全 base64 / data URL 路径）
- OpenAI SSE 流式输出
- Claude SSE 流式输出
- `reasoning_effort` 控制透传到 Copilot SDK
- 通过 `x_copilot` 扩展输出 reasoning 与运行时事件（tool / permission / compaction / agent 等）
- `/v1/models` 返回可忽略的 `x_copilot` 扩展元数据，包含 vision / reasoning / limits
- 单进程内会话复用
- 默认拒绝所有工具权限请求，也可通过全局配置切到 allow 模式
- 可通过环境变量接入 Copilot SDK 的 Skills / MCP / 工具白名单 / custom agents / infinite sessions 配置
- 可通过 `x_copilot.agent` 与 `x_copilot.system_message_mode` 选择 custom agent 或切换 system message append / replace
- Release 构建时自动通过 bundler 为目标平台生成 embedded Copilot CLI

## 当前限制

- 暂不支持 tools / function calling / file upload
- 暂不支持 `max_tokens`、`temperature`、`top_p` 等 generation controls；传入会返回 `400 unsupported_feature`
- reasoning / runtime events 当前通过 `x_copilot` 扩展暴露，不伪装成标准 OpenAI / Claude thinking 协议
- OpenAI / Claude 图片输入当前只支持**最新一条用户消息**中的图片内容；更早轮次的图片会被拒绝，避免静默丢失
- OpenAI 图片仅支持 `data:` URL；Claude 图片仅支持 `base64` source
- 会话复用依赖 `X-Session-ID` 请求头，当前仅在进程内有效
- `ask_user` / user input handler 仍未做成 northbound 交互协议；如果上游流程依赖真实用户交互，当前网关还不能完整承接
- embedded CLI 不在仓库中提交，由 CI release 流程自动为各目标平台生成；本地开发通过 `GATEWAY_CLI_PATH` / `COPILOT_CLI_PATH` / PATH 中的 `copilot` 运行

## 运行前提

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
- `GATEWAY_MAX_ACTIVE_SESSIONS`：进程内允许同时保留的持久会话总数上限；超过后新的持久会话会被拒绝
- `GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY`：单个 northbound API Key 同时保留的持久会话上限；超过后该 API Key 新建持久会话会收到限流响应
- `GATEWAY_SDK_AVAILABLE_TOOLS` / `GATEWAY_SDK_EXCLUDED_TOOLS`：控制 Copilot SDK 会话中允许或排除的工具集合
- `GATEWAY_SDK_SKILL_DIRECTORIES` / `GATEWAY_SDK_DISABLED_SKILLS`：将 Copilot SDK skills 以全局配置方式加载到网关创建的会话中
- `GATEWAY_SDK_MCP_SERVERS_JSON`：将 MCP server 配置以 JSON 方式传给 Copilot SDK；建议仅在受信任的网关部署环境中使用
- `GATEWAY_SDK_CUSTOM_AGENTS_JSON` / `GATEWAY_SDK_DEFAULT_AGENT`：注册全局 custom agents，并指定默认 agent；请求也可以通过 `x_copilot.agent` 覆盖默认值
- `GATEWAY_SDK_PERMISSION_MODE`：全局权限策略，支持 `deny`（默认）或 `allow`；`allow` 会批准 Copilot 发起的 shell / 文件 / MCP 等权限请求，仅建议在可信环境启用
- `GATEWAY_SDK_INFINITE_SESSIONS_*`：显式控制 SDK infinite sessions 开关与压缩阈值；不设置时沿用 SDK 默认值

## 本地运行

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

OpenAI 侧可在**最新一条用户消息**中使用 `data:` URL 图片块：

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

Claude 侧可在**最新一条用户消息**中使用 `base64` image block：

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
          {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}}
        ]
      }
    ]
  }'
```

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

后续继续使用同一个 `X-Session-ID` 时，网关会复用进程内 Session，并只把最新用户消息发给 Copilot Session。

对于持久会话，`model`、`system` / system prompt、`x_copilot.system_message_mode`、`x_copilot.agent`，以及 `reasoning_effort` 这类会影响会话配置的字段需要保持一致；否则会返回会话规格冲突错误。

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
    "include_runtime_events": true
  }
}
```

- `agent`：选择已通过 `GATEWAY_SDK_CUSTOM_AGENTS_JSON` 注册的 custom agent
- `system_message_mode`：`append`（默认）或 `replace`
- `include_reasoning`：在非流式响应的 `x_copilot.reasoning` 中返回 reasoning；流式时输出 `x_copilot` reasoning 事件
- `include_runtime_events`：在非流式响应的 `x_copilot.runtime_events` 返回运行时事件；流式时输出额外的 `x_copilot` 事件帧

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
