# Octopus

Octopus 是一个面向多模型服务的 LLM API 聚合、协议转换和负载均衡服务。它提供统一的管理面板、API Key、渠道与分组管理、故障切换、熔断、统计和运行日志，并把不同上游的请求与响应转换为客户端熟悉的协议。

## 功能概览

- 提供 OpenAI Chat Completions、Responses、Embeddings 和 Anthropic Messages 客户端接口，上游支持 OpenAI、Anthropic 和 Gemini 协议。
- 支持 Responses WebSocket、Responses Compact，以及 OpenAI 兼容的图片生成、编辑和变体接口。
- 按分组配置渠道与模型映射，支持轮询、故障切换、加权策略和多上游密钥选择；提供路由预览、会话亲和、熔断、健康评分与统一重试预算，详见[中继路由](RELAY_ROUTING.md)。
- 提供 Web 管理面板，管理账号、API Key、渠道、分组预设、模型价格、代理池和运行设置。
- 支持 NewAPI、AnyRouter、OneAPI、OneHub、DoneHub、Sub2API 等订阅站的账号、Key、模型和余额同步，生成托管渠道，并支持 ALL-API-Hub / metapi 导入。
- API Key 支持 RPM 与费用限额；费用预留、输出上限要求和支持范围见 [API Key 限额](API_KEY_LIMITS.md)。
- 独立“签到”页面支持平台签到、自定义 HTTP / Cookie 签到、状态接口确认、待确认恢复、批量任务和收益统计；Sub2API 可复用订阅账号 Access Token，并在配置 Refresh Token 后自动续期。
- 签到结果可通过 Webhook、Bark、Server酱、Telegram 和 SMTP 邮件通知，支持模板、Markdown、冷却和低余额提醒；可选启用 Turnstile 求解，详见[签到能力、收益与通知](CHECKIN.md)。
- 支持 SSE、WebSocket、工具调用、推理内容和多模态请求的协议适配。
- 提供请求正文捕获、搜索、逐次尝试记录和时间线诊断，详见[请求记录](REQUEST_LOGGING.md)。
- 支持 SQLite、MySQL 和 PostgreSQL，以及 JSON 数据导入 / 导出、ZIP 导出和 WebDAV 定时备份与恢复。
- 发布包使用 SHA-256 校验和与 Ed25519 签名保护自更新流程。

## 客户端接口

在管理面板中配置渠道、分组和 API Key 后，客户端的 `model` 填写分组名称，由分组映射到上游模型。OpenAI 兼容客户端的 Base URL 通常为 `https://你的域名/v1`，使用本项目签发的 API Key。

| 接口 | 方法与路径 |
| --- | --- |
| 模型列表 | `GET /v1/models` |
| Chat Completions | `POST /v1/chat/completions` |
| Responses | `POST /v1/responses` |
| Responses WebSocket | `GET /v1/responses`（WebSocket 升级） |
| Responses Compact | `POST /v1/responses/compact` |
| Anthropic Messages | `POST /v1/messages` |
| Embeddings | `POST /v1/embeddings` |
| Images | `POST /v1/images/generations`、`/v1/images/edits`、`/v1/images/variations` |

Gemini 当前作为上游适配器使用，未提供 Gemini 原生客户端入口。图片与 Compact 需要兼容的上游接口；它们不属于 Polywire 的通用跨协议转换操作。具体能力边界见 [Polywire](polywire/README.md)。

## 技术栈

| 层次 | 技术 |
| --- | --- |
| 后端语言 | Go 1.25+ |
| HTTP 服务 | Gin 1.12、SSE、WebSocket |
| 数据访问 | GORM；SQLite（默认）、MySQL、PostgreSQL |
| 配置与 CLI | Viper、Cobra |
| 日志与缓存 | Zap 结构化日志、分片内存缓存、xxhash |
| 协议适配 | Polywire 独立 Go 模块，支持 OpenAI、Anthropic、Gemini 协议转换 |
| 前端框架 | Next.js 16、React 19、TypeScript |
| 前端样式与组件 | Tailwind CSS 4、Radix UI、shadcn/ui、Lucide |
| 前端状态 | Zustand、TanStack React Query |
| 国际化 | next-intl；简体中文、繁体中文、英文 |
| 构建与发布 | Node.js 24、pnpm（版本声明见 web/package.json）、Python 3、GitHub Actions、多架构 Docker、Ed25519 |

## 架构

```text
请求：客户端 → Gin 路由 / 鉴权 / CORS → Inbound 解析客户端请求
     → Relay 路由 / 负载均衡 / 熔断 → Outbound 编码上游请求 → 上游 LLM 服务

响应：上游 LLM 服务 → Outbound 解析上游响应 → Relay 流处理 / 统计
     → Inbound 编码客户端响应 → 客户端
```

后端入口为 `main.go`，启动流程依次初始化配置、数据库、缓存、HTTP 服务和后台任务。前端使用 Next.js 静态导出，生产构建后嵌入 Go 二进制的 `static/out` 目录。

协议转换由 [Polywire](polywire/README.md) 提供，模块路径为 `github.com/suprelory/octopus-plus/polywire`。根模块通过 `replace => ./polywire` 使用工作区内的代码。`internal/transformer` 负责注入日志、token 估算和进程内签名缓存，并记录能力决策统计；路由、降级策略、重试、连接管理和计费仍由 Octopus 负责。

协议转换优先选择能够无损保留请求语义的渠道。未知顶层字段、Responses 原生工具与输入项，以及 Anthropic MCP、容器和服务端工具等语义无法由其他协议完整表达时，会优先尝试可保留这些语义的渠道；没有可用的保留渠道时，允许有损转换以维持可用性，并在运行日志中记录具体损失。严格降级策略也允许仅涉及这些语义的回退，其他语义损失仍遵循严格策略。同协议恢复仍校验所需的原始数据，缺失或损坏的数据不会被标记为无损恢复。

## 快速部署

### Docker Compose

仓库中的 `compose.yaml` 使用 `ghcr.io/suprelory/octopus-plus:latest` 镜像，并把宿主机目录挂载到容器的 `/app/data`。使用前请将其中的 `/path/to/data` 替换为实际持久化目录；下例使用 `./data`。默认镜像基于 Debian；Alpine 版本使用 `latest-alpine` 标签。

```yaml
services:
  octopus:
    image: ghcr.io/suprelory/octopus-plus:latest
    environment:
      - OCTOPUS_ADMIN_PASSWORD=${OCTOPUS_ADMIN_PASSWORD:?set OCTOPUS_ADMIN_PASSWORD}
    ports:
      - "8080:8080"
    volumes:
      - "./data:/app/data"
    restart: unless-stopped
```

启动服务：

```sh
export OCTOPUS_ADMIN_PASSWORD='至少 12 个字符的强密码'
docker compose up -d
```

访问 `http://服务器地址:8080`。查看日志和状态：

```sh
docker compose logs -f octopus
docker compose ps
```

升级镜像前备份挂载的数据目录，然后执行：

```sh
docker compose pull
docker compose up -d
```

生产环境建议在 Octopus 前配置 HTTPS 反向代理，并限制容器端口只允许来自反向代理的流量。

### 发布二进制

从 [Releases](https://github.com/suprelory/octopus-plus/releases) 下载对应平台的压缩包，解压后在可持久化的数据目录旁运行：

```sh
mkdir -p data
OCTOPUS_ADMIN_PASSWORD='至少 12 个字符的强密码' ./octopus start
```

默认监听 `0.0.0.0:8080`，默认配置文件为 `data/config.json`。可以使用自定义配置文件：

```sh
./octopus start --config /etc/octopus/config.json
```

### 从源码构建

开发环境需要 Go 1.25+、Node.js 24 和 pnpm；使用构建脚本还需要 Python 3、Bash、Git、curl、zip、unzip、tar 和 md5sum（macOS 可用 md5）。Node.js 的版本范围和 pnpm 的精确版本统一声明在 [web/package.json](web/package.json) 的 `engines.node` 和 `packageManager` 中，构建、CI 和发布工作流均从该文件读取；开发环境也应使用相同版本。

前端开发服务器与后端分开运行。全新检出时 `static/out` 不在版本控制中，需先在项目根目录创建占位文件，以满足 Go 的静态资源嵌入要求，再启动后端：

```sh
mkdir -p static/out
touch static/out/.keep
OCTOPUS_ADMIN_PASSWORD='至少 12 个字符的强密码' go run . start
```

占位文件只用于后端开发和检查，不包含管理页面。后端默认拒绝跨域，先按 [Web 开发说明](web/README.md) 将 `http://localhost:3000` 加入 `cors_allow_origins`，再在另一个终端从项目根目录启动前端：

```sh
cd web
pnpm install --frozen-lockfile
NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm dev
```

访问 `http://localhost:3000`。前端环境变量、跨域初始化和回归测试说明见 [web/README.md](web/README.md)。

构建生产二进制时，在项目根目录开始执行以下步骤，先生成前端静态文件，再嵌入 Go 二进制。生产前端默认使用同源 API；构建时不要沿用开发环境的 `NEXT_PUBLIC_API_BASE_URL`：

```sh
cd web
pnpm install --frozen-lockfile
unset NEXT_PUBLIC_API_BASE_URL
pnpm build
cd ..
rm -rf static/out
mv web/out static/out
mkdir -p build
go build -tags=jsoniter -o build/octopus .
```

也可以使用项目构建脚本：

```sh
./scripts/build.sh build linux x86_64
```

构建脚本会安装前端依赖、构建并嵌入静态文件、联网更新模型价格，再将二进制写入 `build/bin/`。完整的多平台发布流程使用 `./scripts/build.sh release`，需要签名密钥，见下方的发布签名说明。也可在 GitHub Actions 手动运行 `Build` 工作流，下载二进制和 Docker 镜像归档。

## 管理员首次初始化

新安装不会创建固定的默认密码。管理员密码至少需要 12 个 Unicode 字符，同时不能超过 72 个字节。

### 容器部署

首次启动前通过 secret 注入初始密码：

```sh
OCTOPUS_ADMIN_PASSWORD='<strong password>' docker compose up -d
```

`OCTOPUS_ADMIN_USERNAME` 可选，默认值为 `admin`。管理员写入数据库后，后续重启不会覆盖已有凭据。Compose 配置会在每次执行时检查密码变量是否存在，这是部署安全措施；请持续配置该 secret，或在完成初始化后按部署策略移除 Compose 中的变量插值检查。

### 裸机部署

如果 `OCTOPUS_ADMIN_PASSWORD` 未设置或为空，启动日志会输出一个加密随机的一次性 bootstrap token。打开 Web 界面，输入该 token 并创建管理员账户。token 在初始化成功后立即失效；服务在尚未初始化时每次重启都会轮换 token。

不要把启动日志公开给不可信人员。初始化完成前，所有需要认证的管理接口都不可用。

### 登录有效期

Web 后台的账号登录使用 30 天 JWT。令牌剩余不足 7 天、页面可见且最近 5 分钟有点击或键盘操作时，会自动续期；同一次登录最长保持 90 天。令牌已经过期时需要重新登录。刷新页面遇到临时网络故障会保留凭据，并提供重新验证入口。

旧版 JWT 按原到期时间使用，重新登录后启用续期；API Key 和显式申请固定有效期的 JWT 不参与续期。修改密码会使旧 JWT 和日志订阅失效。接口约定与边界见[登录续期](AUTHENTICATION.md)。

## 配置

首次启动会自动创建 `data/config.json`。配置项也可以使用 `OCTOPUS_` 前缀的环境变量覆盖，例如：

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8080,
    "max_request_body_mb": 32
  },
  "database": {
    "type": "sqlite",
    "path": "data/data.db"
  },
  "log": {
    "level": "info",
    "format": "console"
  }
}
```

常用环境变量包括：

```sh
OCTOPUS_SERVER_HOST=0.0.0.0
OCTOPUS_SERVER_PORT=8080
OCTOPUS_DATABASE_TYPE=sqlite
OCTOPUS_DATABASE_PATH=data/data.db
OCTOPUS_LOG_LEVEL=info
```

管理操作、认证、转发拒绝、站点同步和备份清理的日志事件与级别见[诊断日志](LOGGING.md)。

数据库类型支持 `sqlite`、`mysql` 和 `postgres`（也接受 `postgresql`）。SQLite 是默认选项；使用 MySQL 或 PostgreSQL 时，把 `OCTOPUS_DATABASE_PATH` 设置为对应驱动所需的 DSN，并确保数据库已创建且应用用户拥有读写、建表和迁移权限。启动时会创建缺失表并执行已有数据所需的定向兼容迁移，升级前请备份数据库。

### 可信代理

如果应用位于 Nginx、云负载均衡器或 Docker 反向代理之后，请在“设置 → 连接与任务 → 网络与服务”中配置实际代理的 IP 或 CIDR，例如 `172.24.0.1` 或 `172.24.0.0/16`。设置保存后立即生效，不需要重启。

只有 TCP 对端命中可信代理列表时，应用才会读取 `X-Forwarded-For` 和 `X-Real-IP`。反向代理也必须正确转发这些请求头：

```nginx
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

只信任实际反向代理的地址。不要在应用端口仍可被公网直连时盲目信任整个地址空间，否则客户端可能伪造转发头。

JSON 导入和 WebDAV 恢复都会重新应用可信代理配置。恢复值无效时，应用会停止信任转发头并报告恢复后的运行态应用失败；已导入的数据保留，需要在设置中修正无效值。

### 跨域访问

`cors_allow_origins` 默认为空，即不允许浏览器跨域访问。在“设置 → 连接与任务 → 网络与服务”中填写完整来源，例如 `http://localhost:3000` 或 `https://panel.example.com`；多个来源以逗号分隔，保存后立即生效。协议、主机名和端口均属于来源，`localhost` 与 `127.0.0.1` 需要分别配置。设为 `*` 时允许所有来源，但不允许携带浏览器凭证。

该项存储在数据库中，通过管理页面或设置接口修改；分离前后端的首次配置示例见 [Web 开发说明](web/README.md)。

### 请求保活

在“设置 → 连接与任务 → 网络与服务”中配置保活。以下设置存储在数据库中，默认均为 `0`（关闭）；保存后对新 HTTP 请求和新 WebSocket 连接生效，无需重启。已有连接继续使用建立时的配置。

| 设置 | 数据库键 | 行为 |
| --- | --- | --- |
| 流式保活间隔（秒） | `sse_heartbeat_interval` | SSE 定时发送注释 `:\n\n` 并刷新；WebSocket 定时发送 Ping，覆盖请求执行和轮次间等待。 |
| 建流前首次心跳延迟（秒） | `sse_pre_stream_heartbeat_delay` | 等待上游 SSE 建流时的首次心跳延迟，需同时启用流式保活。可单独设为 `0` 关闭建流前心跳。 |
| 非流式保活间隔（秒） | `nonstream_keepalive_interval` | 在最终 JSON 响应前发送换行空白，覆盖普通请求、图片和 `/responses/compact`。 |

反向代理场景可以从 `30` 秒开始配置，间隔应小于代理的空闲超时。SSE 在流处理、失败重试和重新连接之间共享心跳调度；透传响应只在完整 SSE 事件之间插入心跳，避免破坏分片 JSON。WebSocket 会持续处理 Ping/Pong；Ping 在 10 秒内未完成时关闭连接并取消当前请求。

心跳不代表上游已经输出有效内容，不阻止尚未交付有效内容时的安全重试，也不会延长首 token 超时或故障转移预算。首次保活字节会提交 HTTP 200：之后发生的错误通过对应的 SSE 事件或 JSON 响应体返回，HTTP 状态无法再改变。非流式保活只在 JSON 正文开始前发送空白；正文交付后不再插入保活字节。

### 定时保存与运行设置

模型价格、模型同步、站点同步和 WebDAV 备份的间隔单位为小时，`stats_save_interval` 的单位为分钟。这些间隔必须是非负整数，`0` 表示暂停，重新设置正数即可启用；修改和备份恢复后立即应用，无需重启。停用时已经开始的任务会执行完毕，同一任务不会因重新启用而重叠执行。

统计保存任务同时持久化渠道密钥的累计费用、状态码和最近使用时间。配置编辑与仅配置恢复会保留存活密钥尚未落库的运行态。

## 目录结构

```text
cmd/                  CLI 与启动流程
internal/conf/        Viper 配置与构建信息
internal/db/          GORM 初始化、缺失表创建和定向兼容迁移
internal/model/       数据模型
internal/op/          业务逻辑与缓存
internal/relay/       API 代理、负载均衡、熔断和流处理
internal/transformer/ Polywire 宿主集成与能力统计
internal/server/      路由、处理器、中间件和认证
internal/sitesync/    订阅站同步、托管渠道、签到和 Sub2API 会话管理
internal/notify/      Webhook、Bark、Server酱、Telegram 和邮件通知
internal/turnstile/   签到 Turnstile 求解平台集成
internal/task/        后台定时任务
internal/webdav/      WebDAV 备份与恢复
internal/update/      发布校验、签名验证与自更新
polywire/             独立协议转换模块、通用 SSE 解码与契约测试
scripts/              构建、价格同步、发布签名脚本
web/src/              Next.js 管理面板
web/tests/            前端辅助逻辑测试和 Playwright 浏览器回归
static/out/           前端生产静态文件
```

## 测试与质量检查

[CI 工作流](.github/workflows/ci.yml) 在向 `dev` / `master` 推送或创建 PR 时运行后端与前端检查，发布流程也会复用这些检查。后端包含格式、vet、测试、竞态检查、独立 Polywire 消费者验证和构建；前端包含 lint、辅助逻辑测试、类型检查、Playwright 回归和生产构建。资源受限的环境可使用远程 CI 完成这些验证。

以下命令用于允许构建的开发或 CI 环境。运行宿主与 Polywire 的测试前，先准备静态资源目录。根目录的 `./...` 不会遍历嵌套 Go 模块，需要分别执行：

```sh
mkdir -p static/out
touch static/out/.keep
GOWORK=off go vet ./...
GOWORK=off go test ./...
cd polywire
GOWORK=off go vet ./...
GOWORK=off go test ./...
cd ..
```

PowerShell 下先执行 `$env:GOWORK = 'off'`，再在两个目录分别运行 `go test ./...`。Polywire 可单独运行 `go vet ./...`、`go build ./...` 和 `go run ./examples/convert`，无需启动 Octopus。

运行前端检查和构建（从项目根目录开始）：

```sh
cd web
pnpm install --frozen-lockfile
pnpm lint
pnpm test
pnpm exec tsc --noEmit
pnpm exec playwright install chromium
pnpm test:e2e
pnpm build
```

Playwright 会自动启动 `127.0.0.1:4173` 的 Next.js 开发服务器，使用模拟认证和拦截的 API 响应，无需运行 Go 后端。CI 使用内置 Chromium 的 Playwright 镜像；测试覆盖范围与失败诊断见 [web/tests/README.md](web/tests/README.md)。

在项目根目录运行价格生成器与发布校验的 Python 测试：

```sh
python3 -m unittest discover -s scripts -p 'test_*.py'
```

## 发布签名与自更新

GitHub Actions 的 `release` 工作流统一负责版本发布：推送 `v[0-9]*` 标签后，先检查该标签对应的确切提交，检查通过后构建产物、生成更新说明并发布。独立的 `changelog` 工作流在 `v*` 标签推送后，将已确认属于 `dev` 历史的标签提交合并到 `master`；它不负责创建 Release 或上传附件。

容器镜像发布到 GitHub Container Registry（GHCR），使用 GitHub Actions 自动提供的 `GITHUB_TOKEN` 和工作流声明的 `packages: write` 权限。首次发布后，在 GitHub Packages 中将 `octopus-plus` 包的可见性设为 Public，才能免登录拉取；私有包需要先登录 `ghcr.io`。镜像标签包括 `latest`、版本号，以及对应的 `-alpine` 标签。

修复发布工作流后，可以在 Actions 中手动运行 `release`，选择包含修复的工作流分支，并在 `tag` 中填写已有版本标签（如 `v1.0.1`）。检查和构建仍固定到该标签对应的提交。若旧流程已创建只有更新说明、没有附件的 Release，会保留说明并补上传产物；已有附件的 Release 会拒绝覆盖。

自更新只接受同时满足以下条件的发布包：归档文件被列在 `checksums.sha256` 中，并且该清单被 Ed25519 签名覆盖。

首次生成签名 seed：

```sh
go run scripts/release_sign.go generate
```

命令会输出 `OCTOPUS_RELEASE_SIGNING_KEY` 和对应的公钥。只把 `OCTOPUS_RELEASE_SIGNING_KEY` 保存为 GitHub Actions secret，secret 名称必须保持一致。签名 seed 应保留离线备份；丢失或轮换该密钥后，必须先发布内置新公钥的受信任二进制，之后新密钥签名的更新包才能被安装。

本地或 CI 环境生成公钥：

```sh
OCTOPUS_RELEASE_SIGNING_KEY='<secret>' go run scripts/release_sign.go public-key
```

发布构建会派生公钥并嵌入每个平台的二进制，对 ZIP 归档生成 SHA-256 清单，同时上传 `checksums.sha256` 和 `checksums.sha256.sig`：

```sh
export OCTOPUS_RELEASE_SIGNING_KEY='<secret>'
./scripts/build.sh release
```

生产签名 key 不得打印、提交到仓库或写入构建日志。普通 `go build`、`./scripts/build.sh build ...` 和 `Build` 工作流默认不嵌入更新公钥，自更新接口会在下载归档前直接失败；`release` 构建必须配置签名 secret。

## 专题文档

| 文档 | 内容 |
| --- | --- |
| [API Key 限额](API_KEY_LIMITS.md) | RPM、费用预留、输出上限与接口限制 |
| [登录续期](AUTHENTICATION.md) | 管理员 JWT、自动续期与失效规则 |
| [签到能力、收益与通知](CHECKIN.md) | 签到配置、状态确认、Sub2API 会话、奖励、通知和 Turnstile |
| [中继路由](RELAY_ROUTING.md) | 路由策略、重试预算、故障范围、亲和与路由预览 |
| [请求记录](REQUEST_LOGGING.md) | 正文捕获、保留、搜索、时间线与诊断 |
| [诊断日志](LOGGING.md) | 服务日志事件、级别与脱敏 |
| [Polywire](polywire/README.md) | 独立协议转换模块与能力边界 |
| [Web 开发](web/README.md) | 开发联调、环境变量、静态导出与前端测试 |

## 致谢

- [looplj/axonhub](https://github.com/looplj/axonhub)
- [bestruirui/octopus](https://github.com/bestruirui/octopus)
