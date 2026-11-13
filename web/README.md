# Octopus Web 管理面板

本目录是 [Octopus](../README.md) 的前端，使用 Next.js 16、React 19、TypeScript、Tailwind CSS 4、Radix UI、Zustand 和 TanStack React Query。界面包含统计首页、订阅站、独立签到、渠道、分组、模型、请求记录和设置，支持简体中文、繁体中文与英文。

生产环境使用 Next.js 静态导出：`web/out` 被复制或移动到根目录的 `static/out`，再嵌入 Go 二进制，由后端同时提供页面与 API。

## 开发联调

使用 Node.js 24 和 [package.json](package.json) 的 `packageManager` 指定的 pnpm 精确版本。Node.js 版本范围由同一文件的 `engines.node` 声明，构建、CI 和发布工作流均读取这两个字段。后端需要 Go 1.25+。以下开发、安装、测试和构建命令应在允许构建的环境执行，资源受限时可使用仓库的远程 CI。

在允许构建的环境中，可按版本声明安装 pnpm：

```sh
cd web
npm install --global "$(node -p 'require("./package.json").packageManager')"
```

先在项目根目录启动后端。全新检出没有 `static/out`，需要占位文件满足 `go:embed`：

```sh
mkdir -p static/out
touch static/out/.keep
OCTOPUS_ADMIN_PASSWORD='至少 12 个字符的强密码' go run . start
```

环境变量只在首次安装时创建管理员，默认用户名为 `admin`；已有数据库继续使用原账号。占位目录不包含管理页面，开发时通过下面的 Next.js 服务访问界面。

### 首次配置跨域

后端默认不允许跨域。若已有同源管理页面，在“设置 → 连接与任务 → 网络与服务 → CORS 跨域白名单”加入 `http://localhost:3000`。

全新安装使用占位目录时，可在另一个终端通过 API 配置。先用实际管理员账号登录，以下示例申请 30 分钟的管理 JWT：

```sh
curl --fail-with-body http://127.0.0.1:8080/api/v1/user/login \
  -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"<管理员密码>","expire":30}'
```

将响应中的 `data.token` 填入下一条命令的 `<管理 JWT>`：

```sh
curl --fail-with-body http://127.0.0.1:8080/api/v1/setting/set \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <管理 JWT>' \
  --data '{"key":"cors_allow_origins","value":"http://localhost:3000"}'
```

此操作替换完整白名单；已有其他来源时，在 `value` 中用逗号分隔并一并保留。设置保存到数据库并立即生效，不能用 `NEXT_PUBLIC_API_BASE_URL` 代替。若使用 `http://127.0.0.1:3000`、其他端口或远程浏览器访问前端，白名单须包含浏览器实际访问的完整来源。

### 启动前端

从项目根目录执行：

```sh
cd web
pnpm install --frozen-lockfile
NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm dev
```

访问 `http://localhost:3000`。API 地址不含 `/api/v1`，各请求会自行附加接口路径。该地址由浏览器访问；浏览器不在后端机器上时，应替换为它能访问的后端地址。

## 前端环境变量

这些 `NEXT_PUBLIC_` 变量在启动开发服务器或构建时读取，并写入前端资源；更改生产值需要重新构建前端。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `NEXT_PUBLIC_API_BASE_URL` | `.` | API 基础地址；生产默认同源，分离开发时指向 Go 后端 |
| `NEXT_PUBLIC_APP_VERSION` | `unknown` | 界面版本号；构建脚本与 Build 工作流会注入版本信息 |
| `NEXT_PUBLIC_GITHUB_REPO` | `https://github.com/suprelory/octopus-plus` | 界面使用的仓库链接 |

## 生产构建

在 `web` 目录中执行：

```sh
pnpm install --frozen-lockfile
unset NEXT_PUBLIC_API_BASE_URL
pnpm build
```

`pnpm build` 会清理旧的 `out` 并生成新的静态站点。`next.config.ts` 使用 `output: "export"`，生产资源前缀为 `./`；当前部署不使用 `pnpm start`（`next start`）。

将输出嵌入后端（接续上面的 `web` 工作目录）：

```sh
cd ..
rm -rf static/out
mv web/out static/out
mkdir -p build
go build -tags=jsoniter -o build/octopus .
```

需要自定义 API 地址时，用目标地址替代上述 `unset`，并在后端配置对应的 CORS 来源。也可在根目录使用 `./scripts/build.sh build linux x86_64`，自动完成前端构建、模型价格更新和后端构建；完整部署与签名发布见[主 README](../README.md)。

## 检查与测试

以下命令在 `web` 目录执行：

```sh
pnpm lint
pnpm test
pnpm exec tsc --noEmit
pnpm exec playwright install chromium
pnpm test:e2e
pnpm build
```

`pnpm test` 运行 `tests/*.test.mjs`；`pnpm test:e2e` 运行 Playwright 浏览器回归，会自动启动端口 `4173` 的开发服务器，使用模拟 API，无需 Go 后端或上游凭据。CI 使用预装浏览器的 Playwright 镜像。覆盖范围、失败截图和 trace 的查看方法见[测试说明](tests/README.md)。

## 目录结构

```text
src/app/                  Next.js 入口、布局与全局样式
src/components/modules/   首页、站点、签到、渠道、分组、模型、日志与设置
src/components/ui/        通用 UI 组件
src/api/                  API 客户端、端点与会话管理
src/route/                模块导航、懒加载与预加载
src/provider/             主题、语言与 Query Provider
src/stores/               Zustand 状态
public/locale/            简体中文、繁体中文与英文翻译
tests/                    辅助逻辑回归测试
tests/e2e/                Playwright 浏览器回归
```
