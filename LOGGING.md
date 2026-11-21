# 诊断日志

运行日志输出到服务控制台，Docker 部署可用 `docker compose logs -f octopus` 查看。默认 `log.level=info`、`log.format=console`；设置 `log.format=json` 可方便日志平台检索结构化字段。

`log.access.enabled` 控制普通 HTTP 访问日志，默认关闭。管理操作、认证拒绝、转发拒绝与后台任务异常仍按各自级别输出，并遵循 `log.level`。普通查询、列表轮询和分组预览不会额外产生成功操作日志；请求失败或超过慢请求阈值时仍可记录诊断。

| 事件 | 记录内容 | 级别 |
| --- | --- | --- |
| `admin.operation` | 管理接口的操作路径、资源 ID、变更字段名、业务结果、耗时；导入导出和恢复附带数量或选项 | 成功 Info，业务失败 Warn，HTTP 5xx Error |
| `auth.event` | 登录成功及登录、管理员认证、API Key 认证的拒绝原因、来源 IP、已识别的凭据 ID | 成功 Info，拒绝 Warn |
| `http.request` / `http.slow` | HTTP 错误码、安全处理后的底层原因或慢请求耗时 | HTTP 5xx Error，慢请求 Warn；普通访问按配置输出 |
| `relay.rejected` | 模型限制、格式错误、模型不存在或没有可用渠道等转发前拒绝 | Warn |
| `relay.upstream_error` | 本地请求 ID、渠道 ID、上游 HTTP 状态与固定错误分类 | Warn |
| `check-in batch … stopped` | 签到批次异常中断时的批次 ID 和错误 | Warn |
| `check-in notifications will resume on the next scan` | 通知扫描出错，等待下一次扫描恢复 | Warn |
| `webdav.retention.failed` / `webdav.retention.complete` | 备份目录读取失败；清理的删除、失败、剩余数量及耗时 | 失败 Warn，清理成功 Info |
| `webdav.backup.failed` | 定时 WebDAV 备份失败原因 | Warn |
| `transaction.panic` | 操作、资源或任务 ID、安全处理后的 panic 信息及堆栈 | Error |

同一动作、拒绝原因和来源 IP 的认证失败最多每分钟输出一次，后续记录的 `suppressed` 表示该时间窗省略的重复次数。跟踪的来源数量有上限，超限来源共用一个限流窗口。

管理日志记录变更字段名和显式摘要，不序列化请求对象。新增诊断会过滤凭据、认证头、Cookie、URL 路径及查询值。Debug 下 HTTP 日志仅记录查询参数名。

中继控制台诊断不输出上游错误正文、自由文本消息或供应商自定义错误码；HTTP 访问日志也不重复记录这些内容。转换、流式响应和 WebSocket 错误仅保留固定分类及 Go 错误类型。这与正文捕获开关独立：开启正文捕获后，完整交换仍由请求记录的访问权限、大小限制和保留期管理，详见 [Request recording](REQUEST_LOGGING.md)。

转发前拒绝也写入管理面板的转发日志，沿用转发日志保留设置。该记录不保存请求正文、不产生上游尝试，也不增加用量、费用或渠道失败统计。开启 HTTP 访问日志后，会同时保留独立的访问记录。

手工事务遇到 panic 会回滚、记录原始堆栈并向调用方返回失败。

扩展日志时，HTTP 错误使用 `resp.ErrorWithAppError` 或 `resp.InternalErrorWithLog`，由请求日志统一输出；返回 HTTP 200 但业务失败的管理操作使用 `middleware.AuditResult` 标明结果。具有凭据上下文的错误应先脱敏，再通过 `apperror.Error.WithLogMessage` 指定诊断边界，防止展开错误链时暴露原始原因中的凭据。
