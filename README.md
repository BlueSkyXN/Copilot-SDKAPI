# copilot-sdkapi

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

- 想最快跑通：看 [docs/getting-started.md](./docs/getting-started.md)
- 想查看配置默认值：看 [docs/configuration.md](./docs/configuration.md)
- 想直接抄请求：看 [docs/api-examples.md](./docs/api-examples.md)
- 想按目录浏览：看 [docs/README.md](./docs/README.md)
- 想确认能力边界：看 [当前实现特性](#当前实现特性) 与 [当前限制](#当前限制)

## 运行前提

依赖通过 `go mod download` 自动拉取（`copilot-sdk` 为公开模块）。

运行时仍需要保证：

1. 有可用的 GitHub / Copilot 认证方式
2. 有可用于 northbound 访问的 API Key；未设置 `GATEWAY_API_KEYS` 时会自动回退到本地试用 key `test-key`
3. 默认监听地址为 `:38095`，可通过 `GATEWAY_LISTEN_ADDR` 覆盖

## 当前实现特性

- 静态 API Key 鉴权
- OpenAI / Claude 文本对话兼容
- OpenAI / Claude 图片输入兼容（仅最新一条用户消息；支持 inline 与远程 URL 拉取，默认拒绝私网与 special-use 目标）；图片通过 SDK blob attachment 以 inline base64 传输，无需写临时文件到磁盘
- OpenAI SSE 流式输出
- OpenAI `stream_options.include_usage` 会在流式结束前追加标准 usage chunk
- Claude SSE 流式输出
- `reasoning_effort` 控制透传到 Copilot SDK；OpenRouter 风格的 `reasoning.effort` 也会映射到同一路径
- OpenAI / OpenRouter 常见请求字段可宽容接受；除 `messages` 外，也支持 `prompt` 回退、`models[]` 回退、`response_format` JSON 指令注入，以及 `max_tokens`、`temperature`、`top_p`、`presence_penalty`、`frequency_penalty`、`metadata` 等常见参数的 no-op 兼容
- OpenAI 标准 `tools` / `functions` 与 Claude 顶层 `tools` 定义都可桥接到现有 `x_copilot.tools` 运行时通道；OpenAI `tool_choice` / `function_call` 的 `none` 与指定函数名也会做合理过滤
- OpenAI 非流式响应会在上游返回 reasoning 时镜像到 `choices[].message.reasoning`；流式响应会把 reasoning delta 额外镜像到标准 `choices[].delta.reasoning`
- `/v1/models` 直接反映当前 Copilot SDK / CLI 运行时实际公开的模型列表，并附带可忽略的 `x_copilot` 扩展元数据（vision / reasoning / limits 等）
- 可通过 `x_copilot.provider` 透传官方 SDK 已支持的 BYOK / custom provider 配置；官方 trusted provider host（如 `api.openai.com`、`api.anthropic.com`、`*.openai.azure.com`）默认可用
- 基于 `X-Session-ID` 的持久会话复用，可跨网关重启恢复
- 默认拒绝所有工具权限请求；服务端可显式配置为 `bridge` 或 `allow`，请求侧只能在该上限内选择更严格模式
- 可通过环境变量接入 Copilot SDK 的 Skills / MCP / 工具白名单 / custom agents / infinite sessions 配置
- 可通过 `x_copilot.agent` 与 `x_copilot.system_message_mode` 选择 custom agent 或切换 system message append / replace / customize
- `x_copilot.system_message_mode` 支持 `customize` 模式：通过 `x_copilot.system_message_sections` 可对 9 个 CLI 系统提示词分段（identity / tone / tool_efficiency / environment_context / code_change_rules / guidelines / safety / tool_instructions / custom_instructions）分别执行 replace / remove / append / prepend 操作
- 可通过 `x_copilot.tools`、`x_copilot.interactive`、`x_copilot.permission_mode` 与 `/v1/copilot/respond` 暴露 SDK-native tools / ask_user / permission 交互能力；工具定义支持 `skip_permission` 字段跳过权限检查
- 可通过 `x_copilot.attachments` 上传通用文件附件
- 可通过 `GATEWAY_OTEL_*` 环境变量启用 OpenTelemetry 集成（需 SDK 支持 OTLP 或 file exporter）；`GATEWAY_OTEL_CAPTURE_CONTENT` 控制是否在 trace 中捕获消息内容
- SDK 会话事件覆盖率：除核心对话事件外，还为 subagent 生命周期（started/completed/failed）、assistant intent、turn start/end、session warning、model change、tool progress/partial result 提供结构化转发；其余 SDK 事件类型通过 generic passthrough 透传到 `x_copilot` runtime events
- Release 构建时自动通过 bundler 为目标平台生成 embedded Copilot CLI

## 当前限制

- OpenAI Chat Completions 的标准 `tools` / `functions` 字段，以及 Claude Messages 的顶层 `tools` 字段，现在都会桥接到 `x_copilot.tools`；但真正发生 tool / ask_user / permission continuation 时，仍要求 `stream=true` + `X-Session-ID` + `/v1/copilot/respond`，当前并不伪装成标准无状态 `tool_calls` / `tool_use` 循环
- OpenAI Chat Completions 的常见 generation / metadata 字段当前会被宽容接受；但 `n>1`、音频输出（如 `modalities=["audio"]` / `audio`）、请求标准 tools 并行执行的 `parallel_tool_calls=true`、`structured_outputs=true`、`response_format.json_schema.strict=true`、`adaptive_thinking`、`thinking_budget`，以及 OpenRouter `reasoning.max_tokens` / `reasoning.exclude` 当前会显式返回 `400 unsupported_feature`
- `/v1/models` 当前只反映 SDK / CLI 运行时实际列出的模型；它不保证覆盖 Copilot Web、LMAPI 或其它前端入口里出现的全部模型 ID
- 当前仍允许客户端手动传入未出现在 `/v1/models` 里的 model ID；纯文本请求会直接透传到上游 SDK / CLI，但如果同时使用 `reasoning_effort` 或图片输入，网关默认会先返回 `400`，要求该模型必须先能在 `/v1/models` 中被识别。**唯一例外**是显式提供 `x_copilot.provider` 时，这类能力校验会委托给自定义上游 provider
- 出于安全原因，`x_copilot.provider.base_url` 当前只支持官方 trusted provider host（如 `api.openai.com`、`api.anthropic.com`、`*.openai.azure.com`）或 IP literal；任意自定义 hostname 目前不支持。如果目标是 `localhost`、私网或 special-use 地址，还需要额外设置 `GATEWAY_ALLOW_PRIVATE_REMOTE_URLS=true`
- OpenAI 非流式响应当前会镜像 plain-text reasoning 到标准 `message.reasoning`，流式响应也会镜像 `reasoning_delta` 到标准 `delta.reasoning`；但 richer runtime events 与 Claude `thinking` block 仍通过 `x_copilot` 扩展暴露
- OpenAI / Claude 图片输入当前只支持**最新一条用户消息**中的图片内容；更早轮次的图片会被拒绝，避免静默丢失
- fresh session 下历史图片仍无法无损重建为原始多轮附件语义，因此不会静默接受更早轮次图片
- tool / ask_user / permission northbound 交互当前要求 `stream=true`，且初始请求必须带 `X-Session-ID`
- 同一会话内 `ask_user` 与 permission continuation 目前按类型串行；如果 SDK 在同一会话里并发抛出第二个同类请求，网关会显式拒绝该第二个请求，避免 FIFO 错配
- `elicitation.requested`、`exit_plan_mode.requested`、`command.queued` 事件目前仍只能作为 runtime event 观察，尚未桥成 northbound continuation；当前 Go SDK 公共 typed RPC 未暴露对应 responder
- embedded CLI 不在仓库中提交，由 CI release 流程自动为各目标平台生成；本地开发通过 `GATEWAY_CLI_PATH` / `COPILOT_CLI_PATH` / PATH 中的 `copilot` 运行

## 安全边界

`Authorization: Bearer <gateway-api-key>` 是一个受信任的 northbound 集成凭据，而不是细粒度终端用户权限模型。

- 默认配置下，网关仍会拒绝 Copilot 发起的权限请求。
- 一旦启用 `GATEWAY_SDK_PERMISSION_MODE=bridge` 或 `allow`，并实际批准了权限，请把该 API Key 视为可代表调用方使用该网关工作区、skills、MCP、工具配置的高权限凭据。
- 建议仅在受信任内网、反向代理、或你自己控制的应用后面暴露此网关。

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
