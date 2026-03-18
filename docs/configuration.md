# 配置默认值

> 目标读者：想确认哪些环境变量必须配、哪些可以省略、默认行为到底是什么。

## 快速结论

- 默认监听地址：`:38095`
- 默认 northbound API key：`test-key`（仅适合本地试用）
- 默认模型：`gpt-4.1`
- 默认日志级别：`error`
- 默认权限策略：`deny`

## 核心运行参数

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GATEWAY_LISTEN_ADDR` | `:38095` | 网关监听地址。传裸端口号（例如 `38095`）会自动规范化成 `:38095`。 |
| `GATEWAY_API_KEYS` | `test-key` | northbound API key 列表。未设置时自动回退到本地试用 key `test-key`；生产环境请显式设置。 |
| `GATEWAY_DEFAULT_MODEL` | `gpt-4.1` | 当请求不带 `model` 时的默认模型。 |
| `GATEWAY_WORKING_DIR` | 当前工作目录 | Copilot SDK 会话默认绑定的工作目录。 |
| `GATEWAY_CONFIG_DIR` | 空，交给 SDK 决定 | 未设置时由 Copilot SDK / CLI 使用其默认配置目录，通常是 `~/.copilot`。 |
| `GATEWAY_REQUEST_TIMEOUT` | `2m` | 单次请求总超时，同时也是 HTTP 读取与 keep-alive 空闲超时上界。 |
| `GATEWAY_STREAM_IDLE_TIMEOUT` | `2m` | 流式响应空闲超时；只要持续有事件输出就不会超时。 |
| `GATEWAY_SESSION_TTL` | `15m` | 持久会话在无活动时的保留时长。 |
| `GATEWAY_SESSION_STORE_PATH` | 用户配置目录下的 `copilot-sdkapi/sessions.json` | 持久会话元数据文件位置。 |
| `GATEWAY_ALLOW_PRIVATE_REMOTE_URLS` | `false` | 是否允许抓取私网、回环或 special-use 远程图片 / 附件地址。 |
| `GATEWAY_MAX_BODY_BYTES` | `1048576` | 请求体大小上限，单位字节。 |
| `GATEWAY_MAX_ACTIVE_SESSIONS` | `256` | 进程内持久会话总上限。 |
| `GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY` | `64` | 单个 API key 的持久会话上限。 |
| `GATEWAY_LOG_LEVEL` | `error` | 日志级别。 |
| `GATEWAY_CLIENT_NAME` | `copilot-sdkapi` | 传给 SDK / CLI 的客户端标识。 |

## CLI 与认证解析

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GATEWAY_CLI_PATH` | 空 | 未设置时按 `COPILOT_CLI_PATH` → embedded CLI → `copilot`（PATH）顺序解析。 |
| `COPILOT_CLI_PATH` | 空 | 仅在 `GATEWAY_CLI_PATH` 未设置时参与 CLI 解析。 |
| `GATEWAY_GITHUB_TOKEN` | 空 | 显式上游 GitHub / Copilot token；优先级最高。 |
| `COPILOT_GITHUB_TOKEN` | 空 | `GATEWAY_GITHUB_TOKEN` 为空时的首个回退 token。 |
| `GH_TOKEN` | 空 | 第二顺位 token 回退。 |
| `GITHUB_TOKEN` | 空 | 第三顺位 token 回退。 |
| `GATEWAY_USE_LOGGED_IN_USER` | 自动 | 默认在未解析到 token 时为 `true`，解析到 token 时为 `false`；也可以显式覆盖。 |

上游认证优先级可理解为：

1. `GATEWAY_GITHUB_TOKEN`
2. `COPILOT_GITHUB_TOKEN`
3. `GH_TOKEN`
4. `GITHUB_TOKEN`
5. 已登录用户凭据（取决于 `GATEWAY_USE_LOGGED_IN_USER`）

### 这些名字分别代表什么

- `GATEWAY_GITHUB_TOKEN`：本网关自己的上游认证变量名，最适合你想显式控制“网关到底用哪个 token”时使用。
- `COPILOT_GITHUB_TOKEN`：更贴近官方 Copilot SDK / CLI 习惯的变量名，适合直接复用官方示例或官方运行环境。
- `GH_TOKEN`：GitHub CLI 生态里常见的 token 变量名；如果你的终端或 CI 已经导出了它，网关会自动兼容。
- `GITHUB_TOKEN`：更通用的 GitHub token 变量名，在 GitHub Actions 或通用自动化脚本里很常见。
- `GATEWAY_USE_LOGGED_IN_USER`：不是 token，而是一个布尔开关；它控制的是“当没有 token 可用时，要不要回退到本机已经登录的 Copilot / GitHub 用户凭据”。

换句话说，`GATEWAY_GITHUB_TOKEN`、`COPILOT_GITHUB_TOKEN`、`GH_TOKEN`、`GITHUB_TOKEN` 这几个通常都可以承载**同一个**可用 token；它们的区别主要是**来源和优先级**，不是不同类型的接口账号。

### 官方实际支持什么名字、从哪来

按官方 Copilot SDK / Copilot CLI 文档，官方直接识别的环境变量名是：

1. `COPILOT_GITHUB_TOKEN`（推荐）
2. `GH_TOKEN`
3. `GITHUB_TOKEN`

本项目只是额外增加了一个 `GATEWAY_GITHUB_TOKEN` 作为更明确的网关专用别名，并把它放在最高优先级。

官方支持的 token 类型 / 常见前缀如下：

- `github_pat_...`：fine-grained PAT（v2），需要带 **Copilot Requests** 权限
- `gho_...`：OAuth user access token
- `ghu_...`：GitHub App user access token
- `ghp_...`：classic PAT，官方不支持

如果你用的是 `copilot login`，Copilot CLI 会把 OAuth 凭据存进系统 keychain，或在兜底情况下写到 `~/.copilot/`；这种模式下通常不需要你手工复制 token，只要让网关复用已登录用户即可。

### 官方 SDK 和当前网关要分开看

先把层次分清：

- **Copilot SDK**：你项目里引用的库
- **Copilot CLI**：单独安装的程序
- **当前官方关系**：SDK Client 通过 JSON-RPC 调 Copilot CLI 的 server mode

所以它们确实不是“同一个东西”，但**当前官方 SDK 的运行时后端就是 CLI**。也正因为如此，CLI 已登录用户凭据本来就是 SDK 的正式认证来源之一。

但要注意：**官方 SDK 的完整认证矩阵，比当前这个网关显式暴露出来的入口更大。**

- 官方 SDK 文档里除了 `githubToken` / 环境变量 / 已登录用户外，还提到 HMAC、direct API token、`gh auth` 等更底层或更高级的认证来源。
- 当前这个网关在代码里显式传给 SDK Client 的，主要是 `GitHubToken` 和 `UseLoggedInUser` 这两个入口；因此本文档重点讲的也是这两条最直接、最稳定的使用路径。

所以如果你的目标是**把这个网关跑起来并稳定使用**，最值得记住的其实只有两条：

1. **本地开发**：先 `copilot login`，再用 `GATEWAY_USE_LOGGED_IN_USER=true`
2. **服务器 / CI / 容器**：显式设置 `GATEWAY_GITHUB_TOKEN`（或官方命名 `COPILOT_GITHUB_TOKEN`）

## SDK 扩展参数

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GATEWAY_SDK_AVAILABLE_TOOLS` | 空 | 不额外限制工具集合，沿用 SDK / CLI 默认工具可见性。 |
| `GATEWAY_SDK_EXCLUDED_TOOLS` | 空 | 不额外排除工具。 |
| `GATEWAY_SDK_SKILL_DIRECTORIES` | 空 | 不额外加载全局 skills 目录。 |
| `GATEWAY_SDK_DISABLED_SKILLS` | 空 | 不禁用任何额外 skills。 |
| `GATEWAY_SDK_MCP_SERVERS_JSON` | 空 | 不额外注入 MCP server 配置。 |
| `GATEWAY_SDK_CUSTOM_AGENTS_JSON` | 空 | 不注册全局 custom agents。 |
| `GATEWAY_SDK_DEFAULT_AGENT` | 空 | 不强制默认 custom agent。 |
| `GATEWAY_SDK_PERMISSION_MODE` | `deny` | 服务端权限策略。支持 `deny`、`bridge`、`allow`。 |
| `GATEWAY_SDK_INFINITE_SESSIONS_ENABLED` | 空，沿用 SDK 默认 | 仅在显式设置时覆盖 SDK 的 infinite sessions 开关。 |
| `GATEWAY_SDK_INFINITE_SESSIONS_BACKGROUND_COMPACTION_THRESHOLD` | 空，沿用 SDK 默认 | 仅在显式设置时覆盖后台压缩阈值。 |
| `GATEWAY_SDK_INFINITE_SESSIONS_BUFFER_EXHAUSTION_THRESHOLD` | 空，沿用 SDK 默认 | 仅在显式设置时覆盖 buffer exhaustion 阈值。 |

## 建议

- **本地试用**：只配 `GATEWAY_USE_LOGGED_IN_USER=true` 即可，网关会监听 `:38095`，northbound key 默认 `test-key`。
- **受控开发环境**：显式设置 `GATEWAY_API_KEYS` 和 `GATEWAY_DEFAULT_MODEL`。
- **生产环境**：显式设置 `GATEWAY_LISTEN_ADDR`、`GATEWAY_API_KEYS`、认证方式与权限策略，不要依赖默认试用 key。
