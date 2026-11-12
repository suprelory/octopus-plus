# 站点签到能力、收益与多渠道通知

## 独立签到页面

导航栏的“签到”页面集中管理签到站点、账号、全量与批量签到、记录和收益。“站点”页面管理订阅站的账号同步与托管渠道。

1. 在“签到”页面新增签到站点，填写实际签到服务的地址和平台；地址可以与订阅站不同。
2. 使用平台内置签到时，关联订阅站，再在签到账号中选择该站的订阅账号。地址为空或未修改时，选择关联站点会填入其平台和地址；执行前会校验一致性，并实时读取订阅账号的 Access Token、用户 ID 或登录凭据。刷新得到的 token 写回订阅账号，签到记录和时间窗口仍属于签到账号。
3. 使用外部签到站时，启用自定义 HTTP 签到，填写实际签到地址、方法和站内路径，并在签到账号中填写该签到站的完整 Cookie。需要用余额差计算奖励时，先关联订阅站，再在签到账号的“余额账号（可选）”中选择实际接收奖励的订阅账号，沿用其余额查询能力。按需开启自动签到和随机延迟。
4. 保存后展开账号，点击“签到并验证”或“立即签到”；也可以通过“手动签到 URL”打开浏览器中的签到页面。

签到账号不执行分组、Key 和模型同步，也不生成托管渠道。签到时间窗口沿用签到站设置。平台签到和 Cookie 签到使用签到站及签到账号的代理；复用 Sub2API 订阅令牌时，签到和状态查询统一使用订阅账号的代理及订阅站 User-Agent。关联余额查询使用订阅站及订阅账号自己的地址、平台、代理和凭据。删除关联订阅站或账号后，平台签到和复用订阅令牌的 HTTP 签到会提示重新选择账号，不再使用旧凭据；外部 Cookie 签到可继续独立执行。需要暂停签到时，在签到页面停用对应站点或账号。

升级时，已有签到配置会迁入独立签到站，保留账号凭据、开关、时区、执行时间和历史记录，同时关闭原订阅账号的签到，避免重复执行。迁移保持原自动请求地址，并保留原手动签到链接；需要改为外站签到时，在新签到站中修改地址及账号凭据。恢复旧备份和导入 ALL-API-Hub / metapi 的签到配置也会进行分离，重复导入不会覆盖独立账号已修改的设置。

已有平台签到账号在保留迁移来源且地址、平台一致时，会自动绑定原订阅账号。原本独立且未关联订阅站的平台签到账号仍兼容原凭据。自定义 HTTP 签到会迁移旧 Cookie 请求头或 Cookie 格式的 access token；只有 Bearer token 的旧配置需要补填签到站 Cookie，或在 Sub2API 签到站中显式开启下述订阅令牌选项。显式修改过的 Cookie 或账号绑定不会被重复迁移覆盖，备份恢复会重映射账号绑定。

账号接口支持 `credential_type: "linked_account"` 和 `linked_account_id`，或 `credential_type: "cookie"` 和 `cookie`。Cookie 账号也可保存可选的 `linked_account_id`，该账号必须属于签到站关联的订阅站，传 `null` 可取消余额账号关联。Cookie 必须为不超过 16 KiB 的单行值，自动放入 `Cookie` 请求头并覆盖旧的共享 Cookie Header。自定义 HTTP 的请求体及专属 Header 支持 `{{cookie}}`、`{{username}}`；旧平台凭据占位符不再注入 token、密码、API Key 或用户 ID。Cookie 和关联账号凭据均从签到日志及错误详情中脱敏。

自动生成的签到站沿用订阅站名称，不添加“· 签到”。两类站点可同名，同类站点仍要求名称唯一；签到站内遇到重名时追加数字序号。升级会修正仍使用旧自动名称的关联签到站，保留自定义名称。

接口沿用 `/api/v1/site`：创建签到站时指定 `kind: "checkin"`，可选的 `linked_site_id` 指向订阅站，传 `null` 可取消关联；省略 `kind` 时创建订阅站。站点类型创建后保持不变。列表通过 `kind` 区分两类记录，单账号、批量及定时签到仅执行签到站账号。

“全量签到”通过 `POST /api/v1/site/checkin-all` 创建后台任务；勾选站点后的“批量签到”通过 `POST /api/v1/site/batch` 提交 `{"action":"checkin","ids":[1,2]}`，返回同样的任务对象，并在 `site_ids` 中保留所选范围。两者均只执行启用站点中已启用且开启自动签到、站点签到能力可用的账号，手动触发时不等待账号的下次执行时间。任务进度和日志沿用签到批次查询接口。同一范围的重复提交复用正在执行的任务；存在其他范围的活动任务时返回 409，需等待其完成。无效、已归档或订阅站的 ID 会被拒绝，不会扩大为全量任务。

所有执行入口都会在读取最新账号状态后检查签到站点及账号是否停用、站点是否归档；不满足条件时记录跳过，不请求上游，也不改写上次实际执行结果或增加失败次数。

## Sub2API 复用订阅账号 Access Token

Sub2API 签到站开启“自定义 HTTP 签到”并关联 Sub2API 订阅站后，可开启“使用订阅账号 Access Token”。该选项默认关闭，升级也不会自动开启。开启后，在签到账号中选择关联订阅站的 Access Token 账号，无需填写 Cookie；API Key 账号不能用于此模式。

签到和状态查询实时读取订阅账号的令牌，自动发送 `Authorization: Bearer ...`，覆盖手写的 Authorization Header，并移除 Cookie Header。刷新使用订阅站地址，签到仍使用配置的签到地址；刷新、签到和状态查询统一使用订阅账号的代理及订阅站的 User-Agent。签到站的业务 Header 可单独配置，但不会覆盖此模式下的身份与 User-Agent。

订阅同步、创建 Key、余额及签到查询共用 Sub2API 会话管理。自动续期需要有效的 Refresh Token；过期时间可选，JWT 的 `exp` 仅用于辅助安排提前刷新。已知距离过期不足 5 分钟时提前刷新；使用登录 Access Token 的查询收到可恢复的 401 时，读取最新凭据，必要时刷新并重查一次。403、API Key 失效、账号停用、会话撤销及 IP/UA 绑定不匹配不会触发循环刷新。429、服务或网络临时错误保留凭据并短暂退避，只有已确认尚未过期的旧 Access Token 可以继续使用。

同一订阅账号的并发请求共享刷新结果，数据库租约协调多个进程的令牌轮转。Access Token、Refresh Token、有效期和 `credential_version` 一起保存，同步快照不能覆盖它们。请求取消后，已经启动的刷新仍会在独立的有限超时内完成保存；数据库暂时不可用时，当前进程保留已收到的新令牌并只重试保存。刷新令牌一旦被上游消费，若响应丢失或进程在保存前退出，客户端无法凭旧令牌恢复，需重新获取登录凭据。请使用独立登录会话，避免浏览器和本项目争用同一个 Refresh Token。

签到前查询的鉴权恢复不会导致提前提交。提交标记写入后，即使签到接口返回 401，也只刷新凭据并复查状态，不清除提交标记；永久鉴权错误会显示具体原因。没有状态接口时，签到请求（包括配置为 GET 的签到）也不会自动重发；如凭据已恢复，会提示确认站点状态后再操作。

Sub2API 订阅账号更新凭据、凭据类型或账号代理时，API 请求必须携带列表返回的 `credential_version`。缺少版本或版本过旧返回 `409 / site.account.credential_conflict`。只改账号名称、自动同步等非凭据信息时，省略凭据字段即可，不需要版本。编辑界面只提交实际修改的凭据字段，并保留打开表单时的版本；冲突后保留草稿并提示重新加载。升级把旧账号版本初始化为 0，不修改现有令牌，刷新租约不会进入备份。

站点 API 对应 `checkin_use_subscription_token: true`，需要同时提供 `platform: "sub2api"`、`kind: "checkin"`、`checkin_http_enabled: true` 和 `linked_site_id`。签到账号使用 `credential_type: "linked_account"` 与 `linked_account_id`，不复制订阅凭据。关闭选项后恢复 Cookie 模式，需要在签到账号中填写 Cookie。开关、账号绑定和待确认任务均随备份保存。

## 收益与余额

“签到 → 签到收益”展示今日、近 7 天、近 30 天和累计奖励，并支持按站点、账号筛选及站点明细。收益范围由该面板的筛选器决定，保留已删除站点和账号的历史日志。日期按浏览器时区计算，近 7 / 30 天包含今天。

只汇总成功签到日志中的有效、非负数字奖励；重复签到不会重复累计。缺失奖励和无效数字分别计数。默认提取的上游奖励保留原单位，不进行汇率或额度换算；自定义提取代码应返回 USD 金额，余额差补记也使用账户余额的 USD 单位，均保留最多 6 位小数。不同来源或站点的总和不一定代表同一种货币。账户的 `today_income` 仍来自上游余额或收入记录，与签到奖励统计分别展示。

平台签到在取得执行凭据后、发出签到请求前，会尝试读取一次最新余额（最多等待 10 秒，不读取收入日志、不覆盖缓存余额）。如果本次签到成功且未返回 `data.reward`，会在签到后余额刷新成功时，用“签到后余额 − 签到前余额”补记本次奖励，并用于签到记录、收益统计和通知。已有奖励（包括 0）优先保留；“今日已签到”、任一次余额读取失败或差额为负时不补记，差额为零时记录 0。余额差只反映两次查询之间的净变化，同期消费或充值可能影响金额。

平台签到成功或上游返回“今日已签到”后，会尝试刷新账户余额、累计用量和今日收入；绑定订阅账号时，前后余额均读取该订阅账号，更新订阅账号余额，并在签到账号展示该余额。刷新复用现有的平台接口、凭据及签到代理设置，最多等待 10 秒。接口失败、缺失字段或无效数字会保留对应旧值；真实的零余额会正常保存。刷新发生在签到日志提交后，仅可补齐缺失奖励，不改变签到状态；刷新或补记失败均不会使已成功的签到变为失败。

自定义 HTTP 签到选择“余额账号”后也遵循上述余额差规则，查询实际关联订阅站的接口。Cookie 模式下，订阅站登录得到的新 token 只保存到订阅账号，余额账号失效、登录失败或余额查询失败均不影响外站签到。Sub2API 订阅令牌模式使用所选订阅账号进行签到和余额查询，余额查询失败仍不改变签到结果。未选择余额账号时跳过前后余额查询，仍可直接提取响应奖励。余额查询复用 NewAPI、AnyRouter、OneAPI、OneHub、DoneHub 和 Sub2API 的现有实现，无需配置新的余额请求。

## 自定义 HTTP 奖励提取代码

在自定义 HTTP 签到配置中，可选填“奖励提取代码（JavaScript）”，保存为站点的 `checkin_reward_extractor`。填写函数体，通过 `response` 读取签到返回的 JSON，返回非负、有限的数字或数字字符串；返回 `null` / `undefined` 表示未提取到奖励。

优先级为：**自定义代码结果 → 默认 `data.reward` → 关联订阅账号签到前后的余额差**。有效的 0 也会优先保留。代码报错、超时或返回无效值时继续尝试后续规则，签到结果保持原有判定。失败或重复签到不执行提取代码、不记录奖励；非 JSON 响应跳过代码提取。

通用模板：

```javascript
return response.data?.reward ?? response.reward ?? null;
```

New API 模板（`quota_awarded` 按 500000 quota / USD 换算）：

```javascript
const quota = response.data?.quota_awarded;
return quota == null ? null : Number(quota) / 500000;
```

其他格式可自行修改字段路径，例如 `return response.data?.amount ?? null;`。模板的换算比例需与实际站点单位一致。

“测试提取”仅运行填写的代码和 JSON 响应样例，不发送签到或余额请求，也不保存配置。对应接口为管理员认证的 `POST /api/v1/site/checkin-reward/test`：请求 `{"code":"return response.data.reward;","response":{"data":{"reward":0.1}}}`，返回 `{"reward":"0.1","found":true}`；无结果时 `found` 为 `false`。

提取器只支持同步 JavaScript，不提供网络、文件、账号凭据或模块导入能力。每次使用独立的 WASM 运行环境，限制 16 KiB 代码、1 MiB JSON 输入、16 MiB JavaScript 内存和 250 毫秒执行时间；排队上限为 5 秒，首次 WASM 初始化上限为 30 秒，均受调用方总超时限制。配置随站点备份保存，恢复时也会重映射余额账号关联。

## 使用独立接口确认签到结果

当 `/api/checkin` 只提交操作、需要 `/api/checkin/status` 查询结果时，在自定义 HTTP 配置中开启“通过状态接口确认签到”。原有签到请求配置继续用于提交；状态查询单独配置 GET / POST、站内路径、JSON 请求体和 Header。状态查询沿用签到选择的 Cookie 或订阅账号 Access Token 及签到站公共 Header，不继承签到专属 Header。Cookie 模式使用签到账号代理；订阅令牌模式使用订阅账号代理及订阅站 User-Agent。请求体与专属 Header 仍支持 `{{cookie}}`、`{{username}}`；状态路径和 JSON 请求体还支持 `{{checkin_date}}`，其值按站点时区在本次执行开始时固定。

站点接口新增可选 `checkin_status` 对象，省略时沿用已有值，更新为 `null` 时关闭状态查询。创建未提供此字段的站点继续按签到响应判定。示例：

```json
{
  "checkin_http_enabled": true,
  "checkin_http_method": "POST",
  "checkin_http_path": "/api/checkin",
  "checkin_status": {
    "method": "GET",
    "path": "/api/checkin/status",
    "body": "",
    "headers": [],
    "skip_precheck": false,
    "checked_path": "data.checked_in",
    "checked_value": true,
    "unchecked_value": false,
    "date_path": "",
    "code": "",
    "initial_delay_ms": 0,
    "poll_interval_ms": 2000,
    "max_attempts": 3,
    "timeout_seconds": 30,
    "confirm_timeout_minutes": 60,
    "reward_source": "status"
  }
}
```

字段匹配要求状态接口返回 2xx JSON，并严格匹配字段值及类型。`true` 与字符串 `"true"` 不等价；字段缺失、类型不符、`success: false`、非 JSON、HTTP 错误均为“无法确认”。点分路径支持数组下标，如 `data.records.0.checked_in`。可选 `date_path` 必须匹配本次 `YYYY-MM-DD` 日期；没有日期字段或 JavaScript 规则时，跨日后的“今日状态”不能确认前一天的签到。

复杂格式可填写 `code`（函数体），优先于字段匹配；输入 `response` 为状态响应 JSON，`context.httpStatus` 为 HTTP 状态，`context.checkinDate` 为本次目标签到日期。只接受 `checked_in`、`not_checked_in`、`unknown` 三个字符串，异常、无效返回值和超时均不提交新签到。非 2xx 或非 JSON 响应不会运行代码。示例：

```javascript
if (response.code !== 0 || response.data?.date !== context.checkinDate) return "unknown";
if (response.data.checked_in === true) return "checked_in";
if (response.data.checked_in === false) return "not_checked_in";
return "unknown";
```

默认执行流程：

1. 查询签到前状态。已签到则直接记录成功但不计奖励；无法确认则保存为 `pending / pending_confirmation`，暂不提交。
2. 确认未签到后，先把本次执行和提交标记写入数据库，再发送一次签到请求。该标记写入后，即使进程退出或请求超时，恢复时也只查询状态。
3. 按配置等待并查询状态，以状态接口的业务结果为最终依据。签到接口的 2xx、“已受理”或错误状态均不能单独确定本次结果。
4. 暂未确认时保留待确认状态，不增加失败次数、不计收益、不发送结果通知。开启自动签到的账号随后台扫描继续查询；也可手动点击继续确认。关闭自动签到的账号需要手动继续。
5. 确认成功后更新原有那一条日志、刷新余额并发送一次结果通知，当日再次点击复用成功结果。达到确认期限后记为 `failed / unconfirmed_checkin`，自动任务停止确认；待确认期间配置或凭据变化也会停止自动确认。

失败后可再次手动点击签到。尚未提交的流程会重新开始前置检查，使用当前 Cookie 和配置，并保留旧失败日志；修正配置后无需等待次日。已经提交的流程只使用当前配置复查状态，即使确认期限已到也不会重复提交；复查仍遵守单轮超时和查询次数限制。复查成功会把原失败日志更新为成功，奖励只计一次；仍无法确认时显示原始失败原因和本次查询结果，不增加失败次数或重复发送失败通知。状态查询已关闭时，需要重新启用才能复查已提交的流程。跨日复查仍需要能够验证目标签到日期的规则。

默认每轮最多查询 3 次、间隔 2 秒、整轮最多 30 秒，确认期限 60 分钟。查询次数最多 5 次，首次等待和间隔最多 10 秒，整轮最多 120 秒，确认期限最多 24 小时。恢复查询允许在签到时间窗口之外执行，新的签到仍遵循时间窗口。批次展示的“本轮待确认”是该批次结束时的计数，最新结果以账号状态和签到记录为准。

`skip_precheck: true` 可跳过前置检查，但无法证明本次新增签到，因此后续只记“今日已签到”，不计奖励。默认前置检查确认未签到且提交后确认已签到时，才提取本次奖励；同一次已提交签到的后续确认复用同一条日志，重复点击与恢复查询不会重复累计。未提交的失败在手动重试时保留旧日志，并创建新的检查记录。

`reward_source` 可选 `submit`（默认，签到响应的 `data.reward`）或 `status`（状态响应的 `data.reward`）。奖励代码中 `response` 继续代表签到响应，另可通过 `responses.submit`、`responses.status` 读取两个响应，例如 `return responses.status?.data?.amount ?? null;`。未保存原始响应正文，恢复查询时 `responses.submit` 为 `null`，需要奖励的站点应优先使用状态响应。恢复确认不再通过旧余额快照计算奖励；当轮立即确认仍可沿用余额差补记。

“用响应样例测试判定”只运行 JSON 样例，不请求上游、不提交签到、不保存配置。管理员接口为 `POST /api/v1/site/checkin-status/test`，请求包含 `config`、`http_status`、`date`、`response`，返回 `{"state":"checked_in"}` 等状态。奖励预览接口可额外提供 `status_response` 以测试两个响应的提取代码。状态代码沿用奖励代码的 WASM 隔离及资源限制。

配置、待确认阶段和提交标记随备份保存。恢复会重映射配置引用；不包含历史日志的备份也能重建待确认记录。配置已变化的执行不会套用新规则重发签到。数据库升级自动添加配置、进度和批次待确认计数字段。

## 通知设置

在“设置 → 连接与任务 → 统一通知渠道”选择并配置通知渠道，点击“保存通知渠道”。多个渠道可以同时接收同一条结果；切换渠道不会丢失当前填写的内容。每个渠道的“测试此渠道”使用当前填写的配置，只发送一条测试通知，不保存配置，也不占用签到通知的冷却时间。清空渠道后保存即可停用。

支持与 meta-gateway 相同的五种渠道：

| 渠道 | 配置 | 发送方式 |
| --- | --- | --- |
| Webhook | HTTP / HTTPS 地址 | POST JSON，保留原有签到事件字段；Markdown 正文原样传递，并附 `format: "markdown"` |
| Bark | 含设备密钥的完整推送 URL，支持自建服务 | POST 标题、正文及 Octopus 分组；Markdown 转为纯文本 |
| Server酱 | SendKey | Server酱 Turbo 接口，直接传递 Markdown 正文 |
| Telegram | Bot Token、Chat ID | `sendMessage`，支持纯文本及由 Markdown 转换的 HTML，支持用户、群组和频道 |
| SMTP 邮件 | 服务器、端口、发件人、收件人，可选用户名和密码 | 支持 STARTTLS、隐式 TLS；Markdown 生成 HTML 和纯文本双版本；无需认证的邮件中继可显式选择不加密 |

SMTP 未填写端口时默认使用 587，显式选择 TLS 时默认使用 465；自动安全模式在 465 端口使用 TLS，其他端口要求 STARTTLS。多个收件人使用逗号分隔，最多 20 个。所有网络请求使用服务端网络连接。

每个渠道可以单独设置标题、正文模板及“纯文本 / Markdown”正文格式；旧配置未指定格式时沿用纯文本发送逻辑。标题始终为纯文本，模板留空或替换后全空时沿用事件原内容。支持 `{{site}}`、`{{account}}`、`{{message}}`、`{{detail}}`、`{{reward}}`、`{{balance}}` 等编辑器列出的变量，变量只替换一次，Markdown 模式会转义变量中的格式字符。编辑器使用示例数据预览，恢复默认会清除该渠道的模板和格式。

Markdown 支持标题、强调、链接、代码、列表、表格等常见语法，邮件与 Telegram 转换时禁用原始 HTML。Telegram 仅输出其支持的 HTML 标签，列表、表格和图片替代文字转为文字，并按显示文字的 UTF-16 长度截断长消息，保留完整标签。Bark 转为纯文本并保留链接地址；Webhook 的 Markdown 渲染由接收端决定。修改模板和格式不会重置通知冷却。

然后在“签到结果通知”开启通知，并选择需要的结果。此处开关、冷却时间和阈值自动保存，默认仍只通知定时签到异常。

| 设置 | 默认值 | 行为 |
| --- | --- | --- |
| `notification_channels` | 空 | 统一渠道 JSON 配置；未配置时沿用旧签到 Webhook，显式保存 `{}` 表示全部停用 |
| `checkin_notify_enabled` | `false` | 开启签到结果通知 |
| `checkin_notify_success_enabled` | `false` | 包含成功及“今日已签到”的结果 |
| `checkin_notify_manual_enabled` | `false` | 包含单账号、批量和全量手动签到 |
| `checkin_notify_webhook_url` | 空 | 兼容旧版本的 Webhook；保存统一渠道配置后由新配置决定发送目标 |
| `checkin_notify_cooldown_seconds` | `3600` | 每个渠道分别按账号、事件和原因冷却，范围 0–604800 秒；0 不冷却 |
| `checkin_low_balance_threshold` | `0` | 签到成功后，刷新所得余额低于该 USD 阈值时提醒；0 关闭低余额提醒 |

通知只在签到结果写入日志后发送，跳过的任务不通知。成功结果可包含奖励和本次刷新所得的余额。低余额提醒只使用本次成功刷新并保存的余额，不使用过期余额；触发低余额提醒时只发送这一条提醒，不再重复发送成功通知。自定义 HTTP 签到选择余额账号且本次刷新成功时，同样可包含余额并触发低余额提醒。

签到结果通知的默认正文时间、模板变量 `{{time}}` 和 Webhook 的 `occurred_at` 均使用 UTC+8，采用带 `+08:00` 偏移的 RFC3339 格式，不受服务器时区影响。

Webhook 接收端支持 `site_checkin_failed`、`site_checkin_low_balance` 和 `site_checkin_success` 三个事件。原有字段保持兼容，并新增标题、等级、触发来源和可选奖励。例如：

```json
{
  "event": "site_checkin_failed",
  "level": "error",
  "title": "签到失败",
  "source": "scheduled",
  "log_id": "1790507233018",
  "site_id": 1,
  "account_id": 11,
  "site_name": "Example site",
  "account_name": "Primary account",
  "status": "failed",
  "reason": "upstream_http_error",
  "message": "HTTP 401 Unauthorized",
  "failure_count": 3,
  "occurred_at": "2026-09-27T19:00:00+08:00"
}
```

低余额事件额外包含 `balance` 和 `threshold`，`reason` 为 `low_balance`、`level` 为 `warning`。成功事件的 `level` 为 `info`，`reason` 为 `checked_in` 或 `already_checked_in`，有奖励时包含 `reward`。所有渠道复用执行日志中的脱敏文本，不发送签到凭据、站点 URL、自定义请求头或上游原始响应。

Webhook 接收端应返回 2xx；Bark、Server酱和 Telegram 还会检查业务响应是否成功。所有 HTTP 渠道均不跟随重定向，发送失败日志和测试接口不会返回通知渠道的凭据或原始响应。

统一渠道配置示例（按需保留字段，空字段不启用对应渠道）：

```json
{
  "webhook_url": "https://example.com/notify",
  "bark_url": "https://api.day.app/device-key",
  "serverchan_key": "SCT...",
  "telegram_bot_token": "123456:bot-token",
  "telegram_chat_id": "-1001234567890",
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "smtp_tls": "starttls",
  "smtp_user": "sender@example.com",
  "smtp_password": "app-password",
  "smtp_from": "Octopus <sender@example.com>",
  "smtp_to": "admin@example.com",
  "templates": {
    "telegram": { "format": "markdown", "body": "**{{site}}**\n账号：{{account}}\n{{message}}" },
    "smtp": { "format": "markdown", "title": "{{emoji}} {{title}}", "body": "## {{event}}\n\n{{message}}" }
  }
}
```

通知按渠道异步发送，每次最多等待 10 秒（包括 SMTP 会话），最多同时发送 4 条，总待发送数量上限为 128。队列已满时丢弃新通知并记录服务日志。发送失败不会改变签到结果，也不会占用该渠道的冷却期；后续符合通知条件的签到可以再次发送，已成功的其他渠道仍保持冷却。更换渠道地址或凭据只重置该渠道的冷却。冷却状态保存在当前进程内，重启会清空，多个服务实例独立计时，最多保留 10000 个未过期的通知键。

渠道配置和通知开关沿用现有设置接口与备份，无需新增数据库表。`POST /api/v1/setting/notification/test` 需要管理员认证，请求为 `{"channel":"webhook","config":{"webhook_url":"https://example.com/notify"}}`；每次只测试指定渠道。

## 统计接口

`GET /api/v1/site/checkin-stats` 需要管理员登录认证，支持 `site_id`、`account_id`、`timezone`（IANA 时区）、`from` 和 `until`（RFC3339 时间）。`from` 为包含边界，`until` 为排除边界；未指定时区时使用服务所在时区。

返回 `today_reward`、`recent_7_days_reward`、`recent_30_days_reward`、`total_reward`，执行状态计数、`invalid_reward_count`、`unknown_reward_count`、实际使用的 `timezone` 和 `by_site` 明细。统计按日志 ID 分页扫描，不会一次加载所有日志。站点收益面板每分钟更新，单次签到或批量签到完成后也会刷新。

统计使用现有签到日志，余额保存到现有账户记录，通知设置纳入现有设置备份；无需新增数据库表。

## 平台默认值、站点配置与接口验证

“签到 → 编辑签到站点 → 站点签到能力”支持三个模式：

- **平台默认（auto）**：优先采用本站当前请求配置的接口验证结果；尚未验证时，自定义 HTTP 签到或平台默认值决定是否启用。
- **启用（enabled）**：显式启用本站的内置或自定义接口，不受平台默认值及历史验证结果影响。
- **禁用（disabled）**：停止本站的手动、批量和定时签到，也覆盖自定义 HTTP 配置。

| 平台 | 初始默认值 | 内置请求 |
| --- | --- | --- |
| NewAPI、OneAPI、OneHub | 启用 | POST /api/user/checkin |
| AnyRouter | 启用 | 内置 Bearer / Cookie 签到及 sign_in 回退 |
| DoneHub | 关闭 | 可手动验证或显式启用 POST /api/user/checkin |
| Sub2API、API | 关闭 | 需配置自定义 HTTP 接口 |

默认关闭不代表该平台的所有站点都不支持签到。前端通过 `GET /api/v1/site/checkin-capabilities` 读取默认值，通过站点响应中的 `checkin_capability` 读取本站实际策略，不再维护平台黑名单。ALL-API-Hub 和 metapi 导入保留显式的账号自动签到偏好；未提供该偏好时使用平台默认值。账号偏好和站点能力共同决定是否进入批量、定时任务。

保存配置后，展开账号并点击“签到并验证”。它调用现有的 `POST /api/v1/site/account/checkin/:id`，**会实际执行签到**，并沿用签到账号凭据、请求头和代理设置；即使账号关闭自动签到或平台默认关闭，也可手动验证。明确禁用本站时不可执行。已验证支持的账号显示“立即签到”，再次执行仍会更新验证结果。

成功签到或明确的“今日已签到”响应确认支持。明确的路由不存在或“签到功能未启用”等站点级响应确认当前接口不可用；AnyRouter 会考虑回退接口后再判断。401 / 403、429、服务错误、网络失败、账号不存在或账号被禁用均不代表站点不支持。自定义 HTTP 需要 JSON 中的明确 `success` 成功标记或可识别的“已签到”提示；普通 2xx、登录 HTML、空响应及无成功标记的 JSON 均记录为 `failed / unconfirmed_checkin`，提示检查签到接口和 Cookie，并进入正常失败退避。无法确认的响应不运行奖励提取、不补记奖励、不更新上次成功时间，也不覆盖已有的明确签到能力证据。

自定义 HTTP 签到的响应提示包含“今日已签到”时，优先记录为 `success / already_checked_in`，不受 HTTP 状态码或 `success: false` 影响，也不累计返回的奖励。支持 JSON 的 `message`、`msg`、`error.message` 提示及非 JSON 响应中的文本；非 JSON 响应仅记录“今日已签到”，不保存原始响应正文。

验证只影响当前站点。在默认模式下，确认支持会开启该站点签到，确认不可用会停止自动任务并清空待执行时间；仍可手动重试验证。无法判断的响应不会覆盖以前的明确证据。站点响应及单账号签到结果包含是否启用、是否可验证、策略来源、支持状态和最后验证时间。

证据绑定平台、站点 URL、自定义签到方法 / 路径 / 请求体 / 请求头、站点请求头和代理选择。修改这些配置后旧证据失效，修改前已发出的请求也不能为新配置写入验证结果。模式或时间窗口调整不改变证据。配置和证据随站点备份保存；新建和更新接口不接受客户端伪造的验证结果。

## Turnstile 验证求解

站点开启 Cloudflare Turnstile 后，签到接口会拒绝没有验证 token 的请求，例如返回 `Turnstile token is empty`（较早的 new-api 版本返回 `Turnstile token 为空`）。在“设置 → 连接与任务 → 签到 Turnstile 求解”开启总开关，再启用 Cloudflare Solver 或 SolverQ 并填写对应平台的 API 令牌，签到遇到这类提示时会自动求解并重试一次。

签到响应不成功且提示包含 “Turnstile”（不区分大小写）时，Octopus 从站点公开的 `/api/status` 读取 `turnstile_site_key`，以站点根地址作为页面地址提交求解，再把 token 作为 `turnstile` 查询参数重新提交签到。两个平台都启用时先使用 Cloudflare Solver，失败后使用 SolverQ；未填写令牌的平台不参与。token 只能使用一次，每次签到单独求解。重试成功后，奖励、余额刷新和通知沿用正常的签到流程。

求解只用于 NewAPI、OneAPI、OneHub、DoneHub 的内置签到，包括使用关联订阅账号的签到；不用于用户名密码账号登录时的 Turnstile 校验、自定义 HTTP 签到和 AnyRouter。Cloudflare 5 秒盾质询仍记录为 `cloudflare_protection` 并按原规则退避，不调用求解平台。

总开关关闭时不请求 `/api/status` 和求解平台，Turnstile 提示与以前一样记录为失败。开启后，没有可用平台、`/api/status` 返回错误状态码或非 JSON 内容、站点未提供 site key、求解失败或站点拒绝求解所得的 token 时，签到记录为 `failed / upstream_business_error`，并在原提示后附加原因，例如 `Turnstile token is empty (Turnstile solver: solving failed: Cloudflare Solver: ERROR_ZERO_BALANCE)`。附加的原因由固定说明、平台名称和错误码或 HTTP 状态码组成，不包含令牌、token、平台的错误描述或原始响应。读取 `/api/status` 或重试签到时出现的网络错误、超时和 Cloudflare 质询仍按原有原因记录；重试签到出错时，错误信息里的 token 替换为 `[redacted]`。

每个平台单次求解最多等待 90 秒。求解请求使用服务端网络连接，不经过签到代理，也不跟随重定向。批量签到时每个账号单独求解，耗时和平台额度消耗随账号数量增加。

| 设置 | 默认值 | 行为 |
| --- | --- | --- |
| `turnstile_solver_enabled` | `false` | 签到 Turnstile 求解总开关 |
| `turnstile_cloudflare_solver_enabled` | `false` | 使用 Cloudflare Solver |
| `turnstile_cloudflare_solver_api_key` | 空 | Cloudflare Solver 令牌 |
| `turnstile_solverq_enabled` | `false` | 使用 SolverQ |
| `turnstile_solverq_api_key` | 空 | SolverQ 令牌 |

令牌保存时去除首尾空白，不能包含空白或控制字符，最长 512 个字符。开关和令牌沿用现有设置接口与备份，无需新增数据库表；令牌与 WebDAV 密码一样以明文保存，设置列表和备份中都包含它。
