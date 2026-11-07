package model

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/clientip"
	"github.com/suprelory/octopus-plus/internal/notify"
)

type SettingKey string

const (
	SettingKeyProxyURL                         SettingKey = "proxy_url"
	SettingKeyTrustedProxies                   SettingKey = "trusted_proxies"            // 可信反向代理 IP/CIDR 列表（逗号或换行分隔）
	SettingKeyStatsSaveInterval                SettingKey = "stats_save_interval"        // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval          SettingKey = "model_info_update_interval" // 模型信息更新间隔(小时)
	SettingKeySyncLLMInterval                  SettingKey = "sync_llm_interval"          // LLM 同步间隔(小时)
	SettingKeySiteSyncInterval                 SettingKey = "site_sync_interval"         // 站点账号同步间隔(小时)
	SettingKeyRelayLogKeepPeriod               SettingKey = "relay_log_keep_period"      // 日志保存时间范围(天)
	SettingKeyRelayLogKeepEnabled              SettingKey = "relay_log_keep_enabled"     // 是否保留历史日志
	SettingKeyRelayLogContentEnabled           SettingKey = "relay_log_content_enabled"
	SettingKeyRelayLogContentKeepPeriod        SettingKey = "relay_log_content_keep_period"
	SettingKeyRelayLogContentMaxMB             SettingKey = "relay_log_content_max_mb"
	SettingKeyCORSAllowOrigins                 SettingKey = "cors_allow_origins"           // 跨域白名单(逗号分隔的完整 origin, 如 "https://example.com,https://example2.com"). 为空不允许跨域, "*"允许所有来源但不下发凭证
	SettingKeyCircuitBreakerThreshold          SettingKey = "circuit_breaker_threshold"    // 熔断触发阈值（连续失败次数）
	SettingKeyCircuitBreakerCooldown           SettingKey = "circuit_breaker_cooldown"     // 熔断基础冷却时间（秒）
	SettingKeyCircuitBreakerMaxCooldown        SettingKey = "circuit_breaker_max_cooldown" // 熔断最大冷却时间（秒），指数退避上限
	SettingKeyChannelAffinityEnabled           SettingKey = "channel_affinity_enabled"     // 是否优先复用同一 API Key/分组/模型上次成功的渠道
	SettingKeyChannelAffinityTTLSeconds        SettingKey = "channel_affinity_ttl_seconds" // 渠道亲和记录 TTL（秒）
	SettingKeyChannelAffinityMode              SettingKey = "channel_affinity_mode"
	SettingKeyChannelAffinitySource            SettingKey = "channel_affinity_source"
	SettingKeyChannelAffinityHeader            SettingKey = "channel_affinity_header"
	SettingKeyEmptyResponseDetectionEnabled    SettingKey = "empty_response_detection_enabled" // 是否全局启用空回检测
	SettingKeyRelayMaxChannelAttempts          SettingKey = "relay_max_channel_attempts"       // 单个 HTTP 请求或 WS response.create 最多尝试的不同渠道数
	SettingKeyRelayMaxTotalAttempts            SettingKey = "relay_max_total_attempts"         // 单个逻辑请求的上游发送总数，包含重连重发和 replay
	SettingKeyRelayFailoverTimeoutSeconds      SettingKey = "relay_failover_timeout_seconds"   // HTTP/WS 提交响应前的故障转移总预算（秒）
	SettingKeyRelayImagesMaxChannelAttempts    SettingKey = "relay_images_max_channel_attempts"
	SettingKeyRelayImagesMaxTotalAttempts      SettingKey = "relay_images_max_total_attempts"
	SettingKeyRelayImagesTimeoutSeconds        SettingKey = "relay_images_timeout_seconds"
	SettingKeyRelayCompactMaxChannelAttempts   SettingKey = "relay_compact_max_channel_attempts"
	SettingKeyRelayCompactMaxTotalAttempts     SettingKey = "relay_compact_max_total_attempts"
	SettingKeyRelayCompactTimeoutSeconds       SettingKey = "relay_compact_timeout_seconds"
	SettingKeyCapabilityDegradationPolicy      SettingKey = "capability_degradation_policy"        // 能力降级策略：allow/warn/strict
	SettingKeyResponsesWSEnabled               SettingKey = "responses_ws_enabled"                 // 是否启用 OpenAI Responses WS 上游能力（仅客户端 WS 入站）
	SettingKeyResponsesWSDefaultMode           SettingKey = "responses_ws_default_mode"            // OpenAI Responses WS 默认模式：off/transform/passthrough
	SettingKeySSEHeartbeatInterval             SettingKey = "sse_heartbeat_interval"               // SSE/WS 流式保活间隔（秒），0 表示禁用
	SettingKeySSEPreStreamHeartbeatDelay       SettingKey = "sse_pre_stream_heartbeat_delay"       // SSE 上游流建立前心跳首次延迟（秒），0 表示禁用
	SettingKeyNonStreamKeepAliveInterval       SettingKey = "nonstream_keepalive_interval"         // 非流式 JSON 保活间隔（秒），0 表示禁用
	SettingKeyProjectedChannelAutoGroupEnabled SettingKey = "projected_channel_auto_group_enabled" // 全局站点投影渠道自动分组模式（0关闭/1模糊/2精确/3正则）
	SettingKeyJWTSecret                        SettingKey = "jwt_secret"                           // JWT 签名密钥（自动生成）
	SettingKeyApiBaseUrl                       SettingKey = "api_base_url"                         // 对外服务基础地址，用于一键导出客户端配置，为空时不显示导出入口
	SettingKeyWebDAVURL                        SettingKey = "webdav_url"                           // WebDAV 服务器地址
	SettingKeyWebDAVUsername                   SettingKey = "webdav_username"                      // WebDAV 用户名
	SettingKeyWebDAVPassword                   SettingKey = "webdav_password"                      // WebDAV 密码
	SettingKeyWebDAVBackupPath                 SettingKey = "webdav_backup_path"                   // WebDAV 远程备份目录
	SettingKeyWebDAVBackupInterval             SettingKey = "webdav_backup_interval"               // WebDAV 自动备份间隔(小时)，0=禁用
	SettingKeyWebDAVRetentionCount             SettingKey = "webdav_retention_count"               // WebDAV 保留备份份数
	SettingKeyWebDAVIncludeStats               SettingKey = "webdav_include_stats"                 // WebDAV 备份是否包含统计数据
	SettingKeyNotificationChannels             SettingKey = "notification_channels"                // 统一通知渠道配置，JSON
	SettingKeyCheckinNotifyEnabled             SettingKey = "checkin_notify_enabled"               // 是否发送签到结果通知
	SettingKeyCheckinNotifySuccessEnabled      SettingKey = "checkin_notify_success_enabled"       // 是否包含成功和已签到结果
	SettingKeyCheckinNotifyManualEnabled       SettingKey = "checkin_notify_manual_enabled"        // 是否包含手动签到结果
	SettingKeyCheckinNotifyWebhookURL          SettingKey = "checkin_notify_webhook_url"           // 签到异常通知 Webhook
	SettingKeyCheckinNotifyCooldownSeconds     SettingKey = "checkin_notify_cooldown_seconds"      // 同一账号/原因通知冷却时间（秒）
	SettingKeyCheckinLowBalanceThreshold       SettingKey = "checkin_low_balance_threshold"        // 低余额通知阈值，0 表示关闭
	SettingKeyTurnstileSolverEnabled           SettingKey = "turnstile_solver_enabled"             // 签到 Turnstile 求解总开关
	SettingKeyTurnstileCloudflareSolverEnabled SettingKey = "turnstile_cloudflare_solver_enabled"  // 是否使用 Cloudflare Solver 平台
	SettingKeyTurnstileCloudflareSolverAPIKey  SettingKey = "turnstile_cloudflare_solver_api_key"  // Cloudflare Solver 令牌
	SettingKeyTurnstileSolverQEnabled          SettingKey = "turnstile_solverq_enabled"            // 是否使用 SolverQ 平台
	SettingKeyTurnstileSolverQAPIKey           SettingKey = "turnstile_solverq_api_key"            // SolverQ 令牌
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyTrustedProxies, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},       // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},          // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"}, // 默认24小时更新一次模型信息
		{Key: SettingKeySyncLLMInterval, Value: "24"},         // 默认24小时同步一次LLM
		{Key: SettingKeySiteSyncInterval, Value: "12"},        // 默认12小时同步一次站点账号信息
		{Key: SettingKeyRelayLogKeepPeriod, Value: "7"},       // 默认日志保存7天
		{Key: SettingKeyRelayLogKeepEnabled, Value: "true"},   // 默认保留历史日志
		{Key: SettingKeyRelayLogContentEnabled, Value: "true"},
		{Key: SettingKeyRelayLogContentKeepPeriod, Value: "7"},
		{Key: SettingKeyRelayLogContentMaxMB, Value: "4"},
		{Key: SettingKeyCircuitBreakerThreshold, Value: "5"},      // 默认连续失败5次触发熔断
		{Key: SettingKeyCircuitBreakerCooldown, Value: "60"},      // 默认基础冷却60秒
		{Key: SettingKeyCircuitBreakerMaxCooldown, Value: "600"},  // 默认最大冷却600秒（10分钟）
		{Key: SettingKeyChannelAffinityEnabled, Value: "true"},    // 默认启用同 API Key/分组/模型的成功渠道亲和
		{Key: SettingKeyChannelAffinityTTLSeconds, Value: "3600"}, // 默认保留 1 小时
		{Key: SettingKeyChannelAffinityMode, Value: "prefer"},
		{Key: SettingKeyChannelAffinitySource, Value: "auto"},
		{Key: SettingKeyChannelAffinityHeader, Value: "X-Session-Id"},
		{Key: SettingKeyEmptyResponseDetectionEnabled, Value: "true"}, // 默认启用空回检测
		{Key: SettingKeyRelayMaxChannelAttempts, Value: "4"},          // 默认最多尝试 4 个候选渠道
		{Key: SettingKeyRelayMaxTotalAttempts, Value: "12"},           // 默认最多发起 12 次上游转发尝试
		{Key: SettingKeyRelayFailoverTimeoutSeconds, Value: "300"},    // 默认故障转移预算 5 分钟
		{Key: SettingKeyRelayImagesMaxChannelAttempts, Value: "0"},
		{Key: SettingKeyRelayImagesMaxTotalAttempts, Value: "0"},
		{Key: SettingKeyRelayImagesTimeoutSeconds, Value: "0"},
		{Key: SettingKeyRelayCompactMaxChannelAttempts, Value: "0"},
		{Key: SettingKeyRelayCompactMaxTotalAttempts, Value: "0"},
		{Key: SettingKeyRelayCompactTimeoutSeconds, Value: "0"},
		{Key: SettingKeyCapabilityDegradationPolicy, Value: "warn"},   // 默认允许降级并写入诊断日志
		{Key: SettingKeyResponsesWSEnabled, Value: "false"},           // 默认关闭 OpenAI Responses WS 新路径
		{Key: SettingKeyResponsesWSDefaultMode, Value: "passthrough"}, // 启用后默认使用协议保真的 passthrough
		{Key: SettingKeySSEHeartbeatInterval, Value: "0"},             // 默认禁用 SSE 流式心跳
		{Key: SettingKeySSEPreStreamHeartbeatDelay, Value: "0"},       // 默认禁用 SSE 上游流建立前心跳
		{Key: SettingKeyNonStreamKeepAliveInterval, Value: "0"},       // 默认禁用非流式保活
		{Key: SettingKeyProjectedChannelAutoGroupEnabled, Value: "0"}, // 默认不强制站点投影渠道自动分组
		{Key: SettingKeyJWTSecret, Value: ""},                         // 为空时自动生成
		{Key: SettingKeyApiBaseUrl, Value: ""},                        // 默认为空，不显示客户端导出入口
		{Key: SettingKeyWebDAVURL, Value: ""},                         // 默认为空，未配置
		{Key: SettingKeyWebDAVUsername, Value: ""},                    // 默认为空
		{Key: SettingKeyWebDAVPassword, Value: ""},                    // 默认为空
		{Key: SettingKeyWebDAVBackupPath, Value: "/octopus-backups"},  // 默认远程目录
		{Key: SettingKeyWebDAVBackupInterval, Value: "0"},             // 默认禁用自动备份
		{Key: SettingKeyWebDAVRetentionCount, Value: "10"},            // 默认保留10份
		{Key: SettingKeyWebDAVIncludeStats, Value: "true"},            // 默认包含统计数据
		{Key: SettingKeyNotificationChannels, Value: ""},              // 未配置时兼容旧签到 Webhook
		{Key: SettingKeyCheckinNotifyEnabled, Value: "false"},         // 默认关闭签到结果通知
		{Key: SettingKeyCheckinNotifySuccessEnabled, Value: "false"},  // 默认仅通知异常
		{Key: SettingKeyCheckinNotifyManualEnabled, Value: "false"},   // 默认仅通知定时签到
		{Key: SettingKeyCheckinNotifyWebhookURL, Value: ""},           // 默认不发送通知
		{Key: SettingKeyCheckinNotifyCooldownSeconds, Value: "3600"},  // 默认同一账号/原因冷却1小时
		{Key: SettingKeyCheckinLowBalanceThreshold, Value: "0"},       // 默认关闭低余额通知
		{Key: SettingKeyTurnstileSolverEnabled, Value: "false"},       // 默认不求解签到 Turnstile
		{Key: SettingKeyTurnstileCloudflareSolverEnabled, Value: "false"},
		{Key: SettingKeyTurnstileCloudflareSolverAPIKey, Value: ""},
		{Key: SettingKeyTurnstileSolverQEnabled, Value: "false"},
		{Key: SettingKeyTurnstileSolverQAPIKey, Value: ""},
	}
}

func (s *Setting) Validate() error {
	switch s.Key {
	case SettingKeyRelayLogContentKeepPeriod:
		return validateIntRange(s.Value, 0, 3650)
	case SettingKeyRelayLogContentMaxMB:
		return validateIntRange(s.Value, 1, 64)
	case SettingKeyRelayLogContentEnabled:
		if s.Value != "true" && s.Value != "false" {
			return fmt.Errorf("setting value must be true or false")
		}
		return nil
	case SettingKeyChannelAffinityMode:
		if s.Value != "off" && s.Value != "prefer" && s.Value != "strict" {
			return fmt.Errorf("affinity mode must be off, prefer or strict")
		}
		return nil
	case SettingKeyChannelAffinitySource:
		switch s.Value {
		case "auto", "header", "session_id", "prompt_cache_key", "api_key":
			return nil
		}
		return fmt.Errorf("invalid affinity source")
	case SettingKeyChannelAffinityHeader:
		if len(s.Value) == 0 || len(s.Value) > 128 {
			return fmt.Errorf("invalid affinity header name")
		}
		for _, ch := range s.Value {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return fmt.Errorf("invalid affinity header name")
			}
		}
		return nil
	case SettingKeyModelInfoUpdateInterval, SettingKeySyncLLMInterval, SettingKeySiteSyncInterval, SettingKeyWebDAVBackupInterval:
		return validateIntRange(s.Value, 0, int(math.MaxInt64/int64(time.Hour)))
	case SettingKeyStatsSaveInterval:
		return validateIntRange(s.Value, 0, int(math.MaxInt64/int64(time.Minute)))
	case SettingKeyRelayLogKeepPeriod,
		SettingKeyCircuitBreakerThreshold, SettingKeyCircuitBreakerCooldown, SettingKeyCircuitBreakerMaxCooldown:
		_, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("setting value must be an integer")
		}
		return nil
	case SettingKeyWebDAVRetentionCount, SettingKeyChannelAffinityTTLSeconds:
		return validateIntMin(s.Value, 1)
	case SettingKeyCheckinNotifyCooldownSeconds:
		return validateIntRange(s.Value, 0, 7*24*60*60)
	case SettingKeyCheckinLowBalanceThreshold:
		value, err := strconv.ParseFloat(s.Value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("setting value must be a non-negative number")
		}
		return nil
	case SettingKeyRelayMaxChannelAttempts:
		return validateIntRange(s.Value, 1, 64)
	case SettingKeyRelayImagesMaxChannelAttempts, SettingKeyRelayCompactMaxChannelAttempts:
		return validateIntRange(s.Value, 0, 64)
	case SettingKeyRelayImagesMaxTotalAttempts, SettingKeyRelayCompactMaxTotalAttempts:
		return validateIntRange(s.Value, 0, 256)
	case SettingKeyRelayImagesTimeoutSeconds, SettingKeyRelayCompactTimeoutSeconds:
		return validateIntRange(s.Value, 0, 3600)
	case SettingKeyRelayMaxTotalAttempts:
		return validateIntRange(s.Value, 1, 256)
	case SettingKeyRelayFailoverTimeoutSeconds:
		return validateIntRange(s.Value, 1, 3600)
	case SettingKeySSEHeartbeatInterval, SettingKeySSEPreStreamHeartbeatDelay, SettingKeyNonStreamKeepAliveInterval:
		value, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("setting value must be an integer")
		}
		if value < 0 {
			return fmt.Errorf("setting value must be non-negative")
		}
		return nil
	case SettingKeyRelayLogKeepEnabled, SettingKeyResponsesWSEnabled, SettingKeyWebDAVIncludeStats, SettingKeyChannelAffinityEnabled, SettingKeyEmptyResponseDetectionEnabled, SettingKeyCheckinNotifyEnabled, SettingKeyCheckinNotifySuccessEnabled, SettingKeyCheckinNotifyManualEnabled,
		SettingKeyTurnstileSolverEnabled, SettingKeyTurnstileCloudflareSolverEnabled, SettingKeyTurnstileSolverQEnabled:
		if s.Value != "true" && s.Value != "false" {
			return fmt.Errorf("setting value must be true or false")
		}
		return nil
	case SettingKeyTurnstileCloudflareSolverAPIKey, SettingKeyTurnstileSolverQAPIKey:
		// Keys are sent in a request header or JSON body; reject anything that
		// cannot be a single token instead of failing on every check-in.
		s.Value = strings.TrimSpace(s.Value)
		if len(s.Value) > 512 || strings.ContainsFunc(s.Value, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return fmt.Errorf("solver API key must be a single token of at most 512 characters")
		}
		return nil
	case SettingKeyCapabilityDegradationPolicy:
		switch s.Value {
		case "allow", "warn", "strict":
			return nil
		default:
			return fmt.Errorf("setting value must be one of allow, warn, strict")
		}
	case SettingKeyProjectedChannelAutoGroupEnabled:
		if _, ok := ParseAutoGroupSettingValue(s.Value); !ok {
			return fmt.Errorf("setting value must be one of 0, 1, 2, 3")
		}
		return nil
	case SettingKeyResponsesWSDefaultMode:
		switch s.Value {
		case "off", "transform", "passthrough":
			return nil
		default:
			return fmt.Errorf("setting value must be one of off, transform, passthrough")
		}
	case SettingKeyProxyURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("proxy URL is invalid: %w", err)
		}
		validSchemes := map[string]bool{
			"http":   true,
			"https":  true,
			"socks5": true,
		}
		if !validSchemes[parsedURL.Scheme] {
			return fmt.Errorf("proxy URL scheme must be http, https, socks, or socks5")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("proxy URL must have a host")
		}
		return nil
	case SettingKeyTrustedProxies:
		_, normalized, err := clientip.ParseTrustedProxies(s.Value)
		if err != nil {
			return err
		}
		s.Value = normalized
		return nil
	case SettingKeyApiBaseUrl:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("api base URL is invalid: %w", err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("api base URL scheme must be http or https")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("api base URL must have a host")
		}
		return nil
	case SettingKeyWebDAVURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("WebDAV URL is invalid: %w", err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("WebDAV URL scheme must be http or https")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("WebDAV URL must have a host")
		}
		return nil
	case SettingKeyNotificationChannels:
		_, err := notify.ParseConfig(s.Value)
		return err
	case SettingKeyCheckinNotifyWebhookURL:
		s.Value = strings.TrimSpace(s.Value)
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil || parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.Fragment != "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return fmt.Errorf("check-in notification webhook URL must be an http or https URL")
		}
		return nil
	}

	return nil
}

// validateIntMin 校验 v 为整数且不小于 lo。
func validateIntMin(v string, lo int) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("setting value must be an integer")
	}
	if n < lo {
		return fmt.Errorf("setting value must be at least %d", lo)
	}
	return nil
}

// validateIntRange 校验 v 为整数且位于闭区间 [lo, hi]。
func validateIntRange(v string, lo, hi int) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("setting value must be an integer")
	}
	if n < lo || n > hi {
		return fmt.Errorf("setting value must be between %d and %d", lo, hi)
	}
	return nil
}
