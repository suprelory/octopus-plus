# 独立签到 API 与前端（P4.1 / P4.2）

开发分支为 `v2`，接口前缀为 `/api/v1/checkin`。P4.2 独立签到前端已按本契约实现；生产启动入口在 P5 完成迁移与备份后切换。

## 装配与鉴权

调用方先准备独立签到 schema，再构造一个长期复用的 `checkin.NewServices(store, notificationConfig)`，最后调用 `handlers.RegisterCheckinRoutes(engine, services)`。服务装配共享会话、执行器、余额、批次和通知实例；路由注册不迁移数据、不注册定时任务。`notificationConfig == nil` 表示关闭通知，生产配置需切换时显式注入 `checkin.LoadNotificationConfig`。

所有接口使用现有管理员 Bearer JWT 鉴权，转发 API Key 不可调用。签到业务只依赖注入的 `CheckinStore`；管理员认证和共享代理/系统设置仍属于应用基础设施。停止产生执行/重试请求后，可调用 `Notifications.Wait(ctx)` 等待当前异步通知任务结束，再关闭存储；直接调用 `RunDue` 的任务由调用方等待。

## 前端开发入口（P4.2）

独立页面位于 `web/src/components/modules/checkin/`，专用类型与 hooks 位于 `web/src/api/endpoints/checkin-types.ts` 和 `checkin.ts`。站点与账号、凭据、历史与收益、批次、通知分别提供管理入口。创建账号可选择兼容的已有凭据，或同时创建凭据；余额可关闭、使用主凭据或独立配置。自定义 HTTP、状态查询、奖励提取和本地规则预览在站点编辑器中配置。

生产切换前使用临时构建开关。在项目的 `web` 目录执行：

```powershell
$env:NEXT_PUBLIC_CHECKIN_V2 = '1'
pnpm dev
```

构建静态文件时同样先设置该环境变量，再运行 `pnpm build`；开关在构建时决定页面实现，部署后修改环境变量需要重新构建。不设置开关时沿用旧页面。API 位于不同地址时设置现有的 `NEXT_PUBLIC_API_BASE_URL`。

该开关只切换前端，不注册后端路由、不创建独立表、不迁移旧数据、不启动独立调度。联调后端必须按上文显式准备 schema 和注册独立服务；当前默认生产启动入口尚未完成 P5 切换。独立页面只请求 `/api/v1/checkin` 业务接口，查询失败会保留错误并提供刷新，不自动请求旧订阅 API。代理池和管理员登录继续复用公共基础设施。

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
pnpm test:e2e:checkin
```

两组浏览器测试分别启动默认页面和独立页面，顺序运行；它们与构建共享 `.next`，不要并行启动。独立浏览器用例使用 API mock 校验请求契约、凭据版本、显式确认/重试、长 ID 和布局；后端行为由 P4.1 HTTP 集成测试覆盖，生产迁移与装配联调仍在 P5 验收。CI 已加入两组浏览器测试以及默认/独立两种构建。

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
| 408 / 504 | `checkin.canceled` / `checkin.timeout` | 请求取消/超时，先读取执行状态 |
| 500 | `checkin.internal_error` | 安全通用错误，不返回原始 SQL、URL 或秘密 |

完整映射位于 `internal/server/handlers/checkin_errors.go`。已完成提交后的业务失败通常记录在执行结果内，不一定作为 HTTP 错误返回。

## 验证入口

`internal/server/handlers/checkin_api_test.go` 和 `checkin_async_api_test.go` 使用真实管理员 JWT 与独立 SQLite 数据库，验证业务库无需订阅/渠道表。上游及通知请求仅访问测试本机 HTTP 服务。覆盖 CRUD、秘密保护、版本/引用冲突、执行重放/确认、批次复用与并发、余额失败隔离、历史统计、长 ID 分页、通知显式重试和本地规则。

`internal/op/checkin_errors_test.go` 验证 SQLite 唯一约束及 MySQL/PostgreSQL 驱动错误分类；真实数据库约束用例也加入 `TestIndependentCheckinDatabases`，由既有专用数据库 CI 执行。本轮本地验收未启动 MySQL/PostgreSQL 实例。
