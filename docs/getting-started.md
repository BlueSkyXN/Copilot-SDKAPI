# 快速上手

> 默认本地监听地址是 `http://127.0.0.1:38095`。如果未设置 `GATEWAY_API_KEYS`，默认 northbound API key 为 `test-key`。

### 第 1 步：准备 Copilot 认证

网关启动时需要能代表你访问官方 Copilot SDK。当前支持两种常用方式：

先把概念分清：

- **Copilot SDK**：你在代码里依赖的库
- **Copilot CLI**：单独安装的可执行程序
- **当前官方架构**：SDK Client 通过 JSON-RPC 与 Copilot CLI 的 server mode 通信

所以它们确实是两层不同东西，但**当前官方 SDK 并不是完全绕开 CLI 自己直连另一套独立运行时**。这也是为什么 `copilot login` 的登录状态会和 SDK 可用性直接相关。

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

这些名字都是**环境变量名**，不是四套不同的账号体系。最容易理解的方式是：

- `GATEWAY_GITHUB_TOKEN`：**这个网关项目自己的首选变量名**。如果你想最明确地告诉网关“就用这个 token”，优先设置它。
- `COPILOT_GITHUB_TOKEN`：**官方 Copilot SDK / CLI 更贴近的变量名**。如果你本来就按官方命名管理凭据，可以直接复用。
- `GH_TOKEN`：**GitHub CLI 生态常见变量名**。如果你的 shell、CI 或别的工具已经导出了它，网关会顺手复用。
- `GITHUB_TOKEN`：**更通用的 GitHub token 变量名**，在 GitHub Actions 或一些脚本里很常见，网关把它当作更靠后的通用兜底。
- `GATEWAY_USE_LOGGED_IN_USER`：这**不是 token**，而是一个布尔开关，表示“如果没找到 token，就尝试复用这台机器上已经登录好的 Copilot / GitHub 用户凭据”。

也就是说，前四个本质上都只是“把一个 GitHub / Copilot 可用 token 放到哪个环境变量里”的区别；差别主要在**命名习惯**和**优先级**，不是协议不同。

按**官方 Copilot SDK / Copilot CLI** 文档，官方直接支持的环境变量名其实是：

1. `COPILOT_GITHUB_TOKEN`（推荐）
2. `GH_TOKEN`
3. `GITHUB_TOKEN`

本项目额外增加了一个更明确的别名 `GATEWAY_GITHUB_TOKEN`，并把它放在最前面，方便你显式告诉“这个网关就用这个 token”。

官方支持的 token 来源 / 前缀可以这样记：

- `github_pat_...`：fine-grained PAT（v2），并且要带 **Copilot Requests** 权限
- `gho_...`：OAuth user access token
- `ghu_...`：GitHub App user access token
- `ghp_...`：classic PAT，**不支持**

如果你走 `copilot login`，最简单：它会把登录后的 OAuth 凭据存到系统 keychain 或 `~/.copilot/`，这时你**通常不需要手动看到 token 值**，只要让网关复用已登录用户即可。

#### 方式 B：复用本机已登录用户

最适合本地开发和快速试跑。只要你的本机环境已经具备可用的 GitHub / Copilot 登录状态，就可以让网关直接复用：

```bash
export GATEWAY_USE_LOGGED_IN_USER=true
```

如果你同时设置了 `GATEWAY_GITHUB_TOKEN`，则优先走 token。

### 第 2 步：设置网关自己的 northbound API Key

这是你的应用访问本网关时使用的 Bearer Key，不是 GitHub Token。首次本地试用时如果不设置，网关会自动回退到默认试用 key `test-key` 并在启动时打印告警；生产环境请显式设置：

```bash
export GATEWAY_API_KEYS=test-key
```

### 第 3 步：启动网关

首次试跑建议顺手打开 `info` 日志；否则默认日志级别是 `error`，程序正常启动时几乎不会输出任何提示，这看起来会像“没反应”，其实只是**安静启动**了。

```bash
cd /Users/sky/Github/Copilot-SDKAPI
export GATEWAY_LOG_LEVEL=info
go run ./cmd/copilot-sdkapi
```

最小可运行组合通常是：

```bash
export GATEWAY_DEFAULT_MODEL=gpt-4.1
export GATEWAY_USE_LOGGED_IN_USER=true
go run ./cmd/copilot-sdkapi
```

如果你更希望显式指定 token，则把 `GATEWAY_USE_LOGGED_IN_USER=true` 换成 `GATEWAY_GITHUB_TOKEN=...`。  
如果你想显式控制 northbound key，再补上 `export GATEWAY_API_KEYS=...`；如果省略，调用接口时继续使用 `Authorization: Bearer test-key`。

### 第 4 步：先验证网关是否正常

```bash
curl http://127.0.0.1:38095/healthz
curl http://127.0.0.1:38095/v1/models -H "Authorization: Bearer test-key"
```

`/healthz` 返回 `ok`，`/v1/models` 能列出模型时，就说明从认证到 SDK 连接这条链路已经打通。

### 第 5 步：发出第一次 OpenAI 兼容请求

```bash
curl http://127.0.0.1:38095/v1/chat/completions \
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
2. 再试 OpenAI 标准 `tools` / `functions`、`developer` role、常见 controls 的兼容行为
3. 再试 `X-Session-ID` 持久会话复用
4. 再试图片输入与 `x_copilot.attachments`
5. 最后再试 `x_copilot.tools`、`x_copilot.interactive`、`x_copilot.permission_mode`

这样最容易把“登录 → 启动 → 基础调用 → OpenAI 兼容桥接 → 持久会话 → 进阶交互”一条线跑通。

## 本地运行补充

如果你已经按上面的 5 分钟上手完成认证准备，这里给一个更明确的本地启动模板：

```bash
cd /Users/sky/Github/Copilot-SDKAPI
GATEWAY_API_KEYS=test-key \
GATEWAY_LOG_LEVEL=info \
GATEWAY_DEFAULT_MODEL=gpt-4.1 \
go run ./cmd/copilot-sdkapi
```
