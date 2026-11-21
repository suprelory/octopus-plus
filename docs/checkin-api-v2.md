# 独立签到 API 与 v2 运行时

开发分支为 `v2`，接口前缀为 `/api/v1/checkin`。P5.4 已将生产启动、定时任务、备份及前端默认入口切换到独立运行时；旧数据库须先按 [v2 路线](../V2_ROADMAP.md#数据库升级) 完成离线迁移。

## 装配与鉴权

`start` 先通过数据库迁移门禁、准备普通渠道与独立签到 schema，再构造一个长期复用的 `checkin.NewServices(store, checkin.LoadNotificationConfig)`，交给 HTTP 服务与调度器。API、调度、批次、余额与通知共享同一会话和执行器。签到扫描每 10 分钟执行一次，通知每分钟唤醒一次，两者启动时也执行；账号实际执行时间仍由站点时区、窗口及持久化调度决定。

嵌入测试可以显式准备 schema、构造服务并调用 `handlers.RegisterCheckinRoutes(engine, services)`；`notificationConfig == nil` 表示关闭通知。构造服务和注册路由本身不迁移数据或启动任务。所有接口使用现有管理员 Bearer JWT 鉴权，转发 API Key 不可调用。签到业务只依赖注入的 `CheckinStore`；管理员认证和共享代理/系统设置仍属于应用基础设施。

退出时先停止调度与 HTTP 服务，再调用 `Services.Close(ctx)` 关闭工作准入、取消服务上下文并等待在途执行、后台批次、会话保存与通知结束，最后保存缓存并关闭数据库。调度器和服务各有 20 秒等待预算；超时进程以失败状态退出，不继续关闭仍可能被使用的存储。批次保留中断状态，已提交工作流仅恢复确认，未知通知投递不自动重发。`Notifications.Wait(ctx)` 只等待当前后台通知扫描，不能代替整个服务的关闭流程。

## 前端开发入口（P4.2 / P4.3）

独立页面位于 `web/src/components/modules/checkin/`，专用类型与 hooks 位于 `web/src/api/endpoints/checkin-types.ts` 和 `checkin.ts`。站点与账号、凭据、历史与收益、批次、通知分别提供管理入口。创建账号可选择兼容的已有凭据，或同时创建凭据；余额可关闭、使用主凭据或独立配置。自定义 HTTP、状态查询、奖励提取和本地规则预览在站点编辑器中配置。

默认启用 v2 界面。在项目的 `web` 目录执行：

```powershell
pnpm dev
```

构建静态文件运行 `pnpm build`。API 位于不同地址时设置现有的 `NEXT_PUBLIC_API_BASE_URL`。`NEXT_PUBLIC_V2_UI=0` 仅保留给旧界面兼容回归，不能恢复已经退出生产的旧后端路由；部署时应删除这一覆盖值。其他值或不设置均使用 v2。该变量在构建时生效，修改后需要重新构建；原 `NEXT_PUBLIC_CHECKIN_V2` 已不再生效，P6 将删除兼容分支。

后端使用正常的 `go run . start --config <开发配置>` 联调；全新数据库自动准备独立表，旧数据库不自动迁移。独立页面只请求 `/api/v1/checkin` 业务接口，查询失败会保留错误并提供刷新，不自动请求旧订阅 API。代理池和管理员登录继续复用公共基础设施。

全局 v2 界面具有以下行为：

- 导航移除订阅站，渠道页移除投影页签；浏览器保存的旧订阅导航恢复到主页，旧渠道页签恢复到普通渠道。
- 渠道统一展示，分组成员按渠道/协议标识，自动分组按协议组织且只提交渠道设置。日志统一筛选渠道，保留请求内容、错误和重试诊断，移除订阅模型禁用操作；不请求 `/api/v1/site`、`/api/v1/site-channel` 或日志的 `site-action-targets`。
- 未迁移的托管渠道显示迁移提示，仍禁止编辑、删除与启停；存在此类渠道时自动分组禁止保存，防止丢失原投影全局覆盖值。转换后仍使用原渠道与 Key ID，不通过界面改写托管状态。
- 代理池识别普通渠道及 `checkin_site`、`checkin_account`、`checkin_credential`、`checkin_balance`，跳转到对应编辑器；余额引用聚焦余额配置，归档站点可正确定位，失效引用提示刷新。生产引用计数与删除保护只查询普通渠道和独立签到数据。
- 设置中的订阅同步、旧全量签到和旧调度配置退出 v2 界面。普通渠道模型同步和价格更新继续使用原有功能；签到按站点配置时区和执行窗口，全量签到经确认后调用独立批次接口并轮询进度。

生产路由不再注册旧订阅、投影渠道及旧签到接口，也不注册订阅同步任务。渠道、分组、代理、转发和统计可在没有订阅业务表的数据库上运行。旧源码、测试兼容初始化入口和临时前端分支留待 P6 删除。

本地备份接口及定时 WebDAV 默认使用 version 2。version 1 JSON/ZIP 文件通过共用转换器导入，不能以 `version=1` 参数重新启用旧导入服务；恢复后继续使用独立任务与通知服务。

页面保留以下接口约束：

- 凭据编辑只读取摘要；留空表示保留，清空秘密或替换认证 Header 必须明确选择。保存使用编辑器打开时的版本；冲突保留草稿并提示重新打开。切换凭据类型后不提交已隐藏的秘密字段。
- 待确认任务使用确认入口；写操作不自动重试。批次明确区分全部站点和非空所选站点，创建后轮询进度。日志、批次、投递 ID 和游标始终作为字符串处理。
- 历史筛选明确统计时区，收益与余额区分有效零和未知值，上游今日入账与本地签到奖励分别展示。未知通知投递需确认可能重复送达后才能请求重试。
- 中英文及繁体界面提供稳定错误码翻译；桌面和移动端均提供表单、分页及错误恢复入口。

前端验证命令（在 `web` 目录执行）：

```text
pnpm test
pnpm exec tsc --noEmit
pnpm lint
pnpm test:e2e
pnpm test:e2e:v2
```

`test:e2e` 显式启用旧界面兼容回归，`test:e2e:v2` 验证默认 v2 界面，包含独立签到、全局工作区及 ZIP 备份交互。两组与构建共享 `.next`，需顺序运行。浏览器用例使用 API mock 校验请求契约、凭据版本、显式确认/重试、长 ID、旧导航恢复、渠道保护、代理引用跳转和布局。生产后端另由下述集成测试覆盖。CI 同时运行两组浏览器测试及默认 v2 / 显式旧界面构建。

## 通用约定

- 成功统一 HTTP 200：`{"code":200,"message":"success","data":...}`；失败返回相同信封中的 `error_code` 和安全文案。
- 管理资源 ID（站点、账号、凭据、代理）为正整数。日志、批次、通知投递、待确认任务中的长 ID 和分页游标使用十进制字符串，前端不得转换为 JavaScript `number`。
- 非空请求体必须使用 `Content-Type: application/json`，顶层必须为对象。普通接口上限 1 MiB，本地规则测试上限 2 MiB，嵌套深度上限 64。
- 拒绝未知配置字段、重复字段、尾随 JSON、标量 `null` 和数组中的 `null` 配置项。规则样例和 `checked_value` / `unchecked_value` 保留任意 JSON 值及大小写语义，但同名重复键仍无效。
- PATCH 省略字段表示保留；`false`、`0`、空字符串按显式值处理，并继续进行业务校验。指针/数组字段允许 `null`。`balance_config` 整体替换，`null` 表示关闭余额查询。
- DELETE、归档/恢复、执行、确认、余额查询、通知重试接受空请求体或 `{}`，拒绝其他字段。所有接口拒绝未知、重复或空值查询参数。
- 路径 ID 和游标只接受正十进制数字，拒绝负数、符号和溢出；分页 `limit` 默认 50，范围 1–100。

## 管理接口

| 方法与路径 | 请求/查询 | `data` |
| --- | --- | --- |
| GET `/sites` | `include_archived=true\|false`，默认 false | 站点数组，包含计算后的 `capability` |
| POST `/sites` | `CheckinSitePatch` 的可写配置字段 | 新建站点 |
| GET `/sites/:id` | 无 | 站点 |
| PATCH `/sites/:id` | 站点字段补丁 | 更新后站点 |
| DELETE `/sites/:id` | 空或 `{}` | `{id}` |
| POST `/sites/:id/archive`、`/restore` | 空或 `{}` | 更新后站点 |
| GET `/accounts` | 可选 `site_id` | 账号数组 |
| POST `/accounts` | `site_id`、账号配置、`credential_id` 或 `credential` | 新建账号 |
| GET `/accounts/:id` | 无 | 账号 |
| PATCH `/accounts/:id` | `CheckinAccountPatch`，不接受 `site_id` | 更新后账号 |
| DELETE `/accounts/:id` | 空或 `{}` | `{id}` |
| GET `/credentials`、`/credentials/:id` | 无 | 凭据摘要数组/单个摘要 |
| POST `/credentials` | `CheckinCredentialInput` | 新建凭据摘要 |
| PATCH `/credentials/:id` | `expected_version` 和凭据补丁 | 更新后凭据摘要 |
| DELETE `/credentials/:id` | 空或 `{}` | `{id}` |

可写字段定义在 `internal/model/checkin_requests.go` 和 `checkin_credential_dto.go`。站点/账号创建默认 `enabled=true`，账号默认 `auto_checkin=false`。站点验证补全默认 HTTP 方法、时区、签到时间窗等配置。创建账号时，已有 `credential_id` 与内嵌 `credential` 二选一；内嵌凭据与账号在同一事务中创建，账号失败不会留下凭据。

凭据的密码、Token、API Key、Cookie、会话 Cookie 和认证 Header 内容只作为写入参数。创建、编辑、列表和详情均返回 `CheckinCredentialSummary`，通过 `has_*`、`configured`、`session_state` 和引用数量显示配置状态。编辑必须提交上次读取的 `credential_version` 作为 `expected_version`，版本冲突返回 409；省略秘密字段保留现值，不支持通过空占位符获取秘密。不能写入刷新租约、身份修订、运行锁或工作流状态。

账号 `checkin_pending` 仅公开日志/批次 ID、来源、日期、开始/截止时间、是否提交/结束、状态和原因，隐藏内部指纹与提交前证据。有未完成提交的账号禁止删除；站点有账号时禁止删除；凭据的主认证或余额引用未解除时禁止删除。归档保留配置和历史，恢复保留原 `enabled` 设置。

创建自定义 HTTP 签到配置示例（依次使用创建站点返回的 ID）：

```json
{
  "name": "Example check-in",
  "platform": "api",
  "base_url": "https://checkin.example",
  "checkin_http_enabled": true,
  "checkin_http_path": "/checkin"
}
```

```json
{
  "site_id": 1,
  "name": "Main account",
  "auth_mode": "cookie",
  "auto_checkin": false,
  "credential": {
    "name": "Check-in session",
    "type": "cookie",
    "platform": "api",
    "auth_base_url": "https://checkin.example",
    "cookie": "session=replace-with-your-session"
  }
}
```

## 执行与查询

| 方法与路径 | 请求/查询 | 行为 |
| --- | --- | --- |
| POST `/accounts/:id/execute` | 空或 `{}` | 手动执行；来源由服务端固定为 `manual` |
| POST `/accounts/:id/confirm` | 空或 `{}` | 只确认已有提交，不再次发送签到 |
| POST `/accounts/:id/balance` | 空或 `{}` | 独立刷新余额和可用的上游今日入账 |
| POST `/batches` | `{"scope":"all"}` 或 `{"scope":"selected","site_ids":[1,2]}` | 创建后台批次或返回同范围活动批次 |
| GET `/batches/:id` | 无 | 批次状态、计数及当前进度 |
| GET `/batches` | `limit`、`before_id` | 批次游标分页 |
| GET `/logs` | 下列筛选及 `limit` | 日志游标分页 |
| GET `/stats` | 下列筛选及 `timezone` | 收益/次数聚合，含按站点结果 |
| GET `/capabilities` | 无 | 各平台默认能力 |

执行/确认返回 `CheckinResult`：`status`、`reason`、`message`、可选 `reward`、字符串 `log_id`、`site_id`、`account_id`、`date`、`submitted`、`replayed`。HTTP 200 表示服务返回了业务结果，签到是否成功看 `status`；已提交但尚未确认的结果可为 `pending`。同日已完成操作返回原结果并标记 `replayed=true`；确认始终复用原日志。前端不能依据 HTTP 失败自行假定上游从未收到提交。

余额响应为 `{account_id,balance,balance_used,today_income}`。后两个字段本次未知时返回 `null`，有效零为 `0`；`today_income` 是上游今日入账，不是本地签到奖励。余额查询失败不会撤销已完成签到，也不会重发签到。

批次只选取开启自动签到且符合条件的账号。`selected` 必须为非空有效站点范围，原始 ID 数组最多 1000 项；排序和去重后比较范围。相同范围活动批次复用，不同范围返回 `checkin.batch_active`（409）。后台批次独立于发起 HTTP 请求的生命周期；前端通过 GET 轮询。查询会恢复过期批次为中断状态。

日志和统计筛选为 `site_id`、`account_id`、`status`、`source`、`batch_id`、`before_id`、`from`、`until`。`status` 支持 `success/failed/skipped/pending`；`source` 支持 `manual/scheduled/batch` 及旧历史 `import/auto`。时间采用 RFC3339（含时区，可有小数秒），按完成时间应用 `[from, until)`。统计 `timezone` 使用 IANA 时区，省略时沿用服务器本地时区，响应包含实际时区；建议前端显式传入。

分页按 ID 降序返回 `{"items":[],"next_before_id":"..."}`，仅确有后续记录时返回游标，下一页使用 `before_id`。管理列表为空时直接返回 `[]`。

## 本地规则测试

`POST /reward/test` 接受 `{code,response,status_response?}`，返回 `{reward,found}`，其中奖励 `"0"` 也视为找到。`POST /status/test` 接受 `{config,http_status,response,date}`，返回 `{state}`，状态为 `checked_in`、`not_checked_in` 或 `unknown`；日期格式 `YYYY-MM-DD`。

两个接口只计算用户提供的样例，不访问站点、不加载凭据、不登录或提交签到。运行共享的受限规则引擎；错误返回稳定代码和通用说明，不回显脚本异常中的秘密。

## 通知投递

`GET /notifications?limit=50&before_id=...` 返回投递记录分页。每项包含字符串 `id`、`completion_id`、目标指纹 `destination`、事件、阈值、`state`、尝试次数及开始/结束时间（Unix 毫秒）。不返回 Webhook 地址、SMTP 密码或冷却键。

`POST /notifications/:id/retry` 只允许状态为 `uncertain` 且原发送预算已结束的记录，成功返回 `{"id":"...","state":"queued"}` 并唤醒通知服务。该响应表示已排队，投递结果由列表查询获取；后续仍按当前渠道开关和冷却策略执行。已送达、尚在发送、过早重试及重复重试均返回 409。结果未知的消息可能已经送达，显式重试可能造成重复通知，前端应在用户执行前说明。

## 错误契约

客户端按 `error_code` 处理并翻译，避免依赖英文 `message`。

| HTTP | 代表性错误码 | 处理 |
| --- | --- | --- |
| 400 | `common.invalid_json`、`common.invalid_param`、`checkin.invalid_configuration`、`checkin.invalid_batch_selection` | 修正请求/配置 |
| 400 | `checkin.invalid_reward_rule`、`checkin.invalid_status_rule` | 检查样例和规则 |
| 401 | 现有 `auth.*` | 恢复管理员登录 |
| 404 | `checkin.not_found` | 资源或引用不存在，刷新配置 |
| 409 | `checkin.name_conflict`、`checkin.credential_conflict` | 名称重复或编辑版本过期 |
| 409 | `checkin.credential_in_use`、`checkin.site_in_use`、`checkin.workflow_pending` | 解除引用或完成待确认任务 |
| 409 | `checkin.already_running`、`checkin.batch_active`、`checkin.workflow_conflict` | 读取当前执行/批次状态 |
| 409 | `checkin.not_submitted`、`checkin.configuration_changed`、`checkin.workflow_expired` | 没有可确认任务，或原任务失效 |
| 409 | `checkin.unavailable`、`checkin.site_archived`、`checkin.proxy_disabled`、`checkin.adapter_unavailable` | 检查启用状态及能力 |
| 409 | `checkin.balance_disabled`、`checkin.balance_changed` | 检查余额配置或重新读取 |
| 409 | `checkin.verification_required`、`checkin.session_expired`、`checkin.session_uncertain` | 站点验证或修复凭据 |
| 409 | `checkin.notification_state` | 投递不符合重试条件，刷新状态 |
| 413 / 415 | `checkin.request_too_large` / `checkin.content_type` | 调整请求体大小/媒体类型 |
| 502 | `checkin.balance_unavailable`、`checkin.request_failed` | 上游响应不可用 |
| 503 | `checkin.session_backoff`、`checkin.session_unavailable`、`checkin.session_save_failed` | 稍后重新读取会话状态 |
| 503 | `checkin.unavailable` | 服务正在退出；恢复后先查询状态 |
| 408 / 504 | `checkin.canceled` / `checkin.timeout` | 请求取消/超时，先读取执行状态 |
| 500 | `checkin.internal_error` | 安全通用错误，不返回原始 SQL、URL 或秘密 |

完整映射位于 `internal/server/handlers/checkin_errors.go`。已完成提交后的业务失败通常记录在执行结果内，不一定作为 HTTP 错误返回。

## 验证入口

`internal/server/handlers/checkin_api_test.go` 和 `checkin_async_api_test.go` 使用真实管理员 JWT 与独立 SQLite 数据库，验证业务库无需订阅/渠道表。上游及通知请求仅访问测试本机 HTTP 服务。覆盖 CRUD、秘密保护、版本/引用冲突、执行重放/确认、批次复用与并发、余额失败隔离、历史统计、长 ID 分页、通知显式重试和本地规则。

`internal/server/runtime_test.go` 通过生产路由、真实 JWT 和本机上游验证普通渠道转发、签到、代理保护、默认 ZIP 备份/恢复、重启后不重复提交及旧路由退出。`internal/task/runtime_test.go` 验证独立扫描、通知和默认 WebDAV 备份，`internal/checkin/lifecycle_test.go` 验证退出时等待 Token 保存并保留已提交状态。

`internal/migration/v2/runtime_test.go` 在 SQLite、MySQL、PostgreSQL 上验证未迁移/未完成迁移拒绝启动、并发首次启动、迁移切换及删除旧表后重启；P5.4 已在三种数据库实际执行。`internal/op/checkin_errors_test.go` 和 `TestIndependentCheckinDatabases` 覆盖错误分类及真实约束/并发行为。外部数据库测试需专用 DSN，并只创建/删除随机名称的测试数据库，命令与验证结果见路线文件。
