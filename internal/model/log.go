package model

import (
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"time"
)

type StreamDiagnostics = transformerModel.StreamDiagnostics
type RequestConversion = transformerModel.RequestConversion

// AttemptStatus 尝试状态
type AttemptStatus string

const (
	AttemptSuccess      AttemptStatus = "success"       // 转发成功
	AttemptFailed       AttemptStatus = "failed"        // 转发失败
	AttemptCircuitBreak AttemptStatus = "circuit_break" // 熔断跳过
	AttemptSkipped      AttemptStatus = "skipped"       // 其他原因跳过（禁用、无Key、类型不兼容等）
)

type CapabilityLoss struct {
	Field       string `json:"field"`
	TargetField string `json:"target_field,omitempty"`
	Action      string `json:"action"`
	Condition   string `json:"condition,omitempty"`
	Reason      string `json:"reason"`
}

// ChannelAttempt 记录单次渠道尝试的决策和结果
type ChannelAttempt struct {
	AttemptID         string                   `json:"attempt_id,omitempty"`
	HTTPStatus        *int                     `json:"http_status,omitempty"`
	UpstreamRequestID string                   `json:"upstream_request_id,omitempty"`
	ChannelID         int                      `json:"channel_id"`
	ChannelKeyID      int                      `json:"channel_key_id,omitempty"`
	ChannelName       string                   `json:"channel_name"`
	ModelName         string                   `json:"model_name"`
	AdapterType       string                   `json:"adapter_type,omitempty"` // response, chat, anthropic, gemini, embedding, etc.
	Transport         string                   `json:"transport,omitempty"`
	WSMode            RelayLogWSMode           `json:"ws_mode,omitempty"`
	Recovery          RelayLogWSRecovery       `json:"recovery,omitempty"`
	AttemptNum        int                      `json:"attempt_num"`
	Status            AttemptStatus            `json:"status"`
	Duration          int                      `json:"duration"`
	Sticky            bool                     `json:"sticky,omitempty"`
	SelectionReason   string                   `json:"selection_reason,omitempty"`
	SelectionStrategy string                   `json:"selection_strategy,omitempty"`
	QualityRank       int                      `json:"quality_rank"`
	CandidateCount    int                      `json:"candidate_count"`
	SelectionMetrics  *ChannelSelectionMetrics `json:"selection_metrics,omitempty"`
	Msg               string                   `json:"msg,omitempty"`
	CapabilityStatus  string                   `json:"capability_status,omitempty"`
	CapabilityPolicy  string                   `json:"capability_policy,omitempty"`
	ConversionPath    []string                 `json:"conversion_path,omitempty"`
	Conversion        *RequestConversion       `json:"conversion,omitempty"`
	RequiredFeatures  []string                 `json:"required_features,omitempty"`
	DegradedFields    []string                 `json:"degraded_fields,omitempty"`
	CapabilityLosses  []CapabilityLoss         `json:"capability_losses,omitempty"`
	Lossiness         string                   `json:"lossiness,omitempty"`
	CapabilityReasons []string                 `json:"capability_reasons,omitempty"`
	FallbackReason    string                   `json:"fallback_reason,omitempty"`
	FailureClass      string                   `json:"failure_class,omitempty"`
	FailureScope      string                   `json:"failure_scope,omitempty"`
	Retryable         bool                     `json:"retryable,omitempty"`
	RetryAt           *time.Time               `json:"retry_at,omitempty"`
	Stream            *StreamDiagnostics       `json:"stream,omitempty"`
	Routing           *RoutingSummary          `json:"routing,omitempty"`
}

// RoutingSummary is stored with the final attempt, inside the existing JSON log.
type RoutingSummary struct {
	StopReason      string `json:"stop_reason,omitempty"`
	AttemptsUsed    int    `json:"attempts_used"`
	AttemptLimit    int    `json:"attempt_limit"`
	ChannelsUsed    int    `json:"channels_used"`
	ChannelLimit    int    `json:"channel_limit"`
	RemainingMillis int64  `json:"remaining_millis"`
	Committed       bool   `json:"committed"`
	AffinityMode    string `json:"affinity_mode,omitempty"`
	AffinitySource  string `json:"affinity_source,omitempty"`
}

// ChannelSelectionMetrics captures the local health signals used when the
// candidate was selected. It is serialized inside the relay attempt payload
// and does not require a schema migration.
type ChannelSelectionMetrics struct {
	BaseRank            *int       `json:"base_rank,omitempty"`
	Priority            int        `json:"priority"`
	DynamicScore        float64    `json:"dynamic_score"`
	CompositeScore      float64    `json:"composite_score"`
	InFlight            int        `json:"in_flight"`
	RecentLoad          float64    `json:"recent_load"`
	FailureRate         float64    `json:"failure_rate"`
	LatencyMillis       float64    `json:"latency_millis"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
}

// RelayLogWSMode 表示本次上游 WebSocket 的会话/恢复模式。
type RelayLogWSMode string

const (
	RelayLogWSModeFresh        RelayLogWSMode = "fresh"        // 新建 WS 会话
	RelayLogWSModeContinuation RelayLogWSMode = "continuation" // 直接续传上游会话
	RelayLogWSModeReplay       RelayLogWSMode = "replay"       // 续传失败后回放上下文
)

// RelayLogWSExecMode 表示本次上游 WebSocket 的事件处理方式。
type RelayLogWSExecMode string

const (
	RelayLogWSExecModePassthrough RelayLogWSExecMode = "passthrough" // 原生 WS 事件直通
	RelayLogWSExecModeTransform   RelayLogWSExecMode = "transform"   // 经内部 transformer 管线转换
)

// RelayLogWSRecovery 表示本次会话在执行过程中触发的恢复动作。
type RelayLogWSRecovery string

const (
	RelayLogWSRecoveryReconnect RelayLogWSRecovery = "reconnect" // 续传链路失效后，原链路强制重连成功
	RelayLogWSRecoveryReplay    RelayLogWSRecovery = "replay"    // 续传失败后回放上下文成功
	RelayLogWSRecoveryDowngrade RelayLogWSRecovery = "downgrade" // WebSocket 不可用后降级到 HTTP
)

type RelayLog struct {
	Trace                *RelayTrace         `json:"trace,omitempty" gorm:"-"`
	ID                   int64               `json:"id" gorm:"primaryKey;autoIncrement:false"` // Snowflake ID
	Time                 int64               `json:"time"`                                     // 时间戳（秒）
	RequestModelName     string              `json:"request_model_name"`                       // 请求模型名称
	RequestAPIKeyID      int                 `json:"request_api_key_id" gorm:"index"`          // 请求使用的 API Key ID
	RequestAPIKeyName    string              `json:"request_api_key_name"`                     // 请求使用的 API Key 名称
	ClientIP             string              `json:"client_ip"`                                // 客户端 IP
	EndpointType         string              `json:"endpoint_type"`                            // 入站端点分类
	ChannelId            int                 `json:"channel" gorm:"index"`                     // 实际使用的渠道ID
	ChannelName          string              `json:"channel_name"`                             // 渠道名称
	ActualModelName      string              `json:"actual_model_name"`                        // 实际使用模型名称
	InputTokens          int                 `json:"input_tokens"`                             // 输入Token
	TransportInputTokens *int                `json:"transport_input_tokens,omitempty"`         // 实际发送到上游请求体的 Token 估算
	BillInputTokens      *int                `json:"bill_input_tokens,omitempty"`              // 按常规输入价格计费的 Token
	CacheReadTokens      *int                `json:"cache_read_tokens,omitempty"`              // 从缓存读取的 Token
	CacheWriteTokens     *int                `json:"cache_write_tokens,omitempty"`             // 写入缓存的 Token
	OutputTokens         int                 `json:"output_tokens"`                            // 输出 Token
	ReasoningEffort      string              `json:"reasoning_effort"`                         // 最终出站思考强度
	ReasoningTokens      int                 `json:"reasoning_tokens"`                         // 上游返回的思考 Token
	ReasoningChars       int                 `json:"reasoning_chars"`                          // 无官方 Token 时的思考字符数
	Ftut                 int                 `json:"ftut"`                                     // 首字时间(毫秒)
	UseTime              int                 `json:"use_time"`                                 // 总用时(毫秒)
	Cost                 float64             `json:"cost"`                                     // 消耗费用
	RequestContent       string              `json:"request_content"`                          // 请求内容
	ResponseContent      string              `json:"response_content"`                         // 响应内容
	Error                string              `json:"error"`                                    // 错误信息
	Success              bool                `json:"success" gorm:"not null;default:false"`    // 是否成功，便于状态筛选索引
	Attempts             []ChannelAttempt    `json:"attempts" gorm:"serializer:json"`          // 所有尝试记录
	TotalAttempts        int                 `json:"total_attempts"`                           // 总尝试次数
	UsedWS               bool                `json:"used_ws" gorm:"default:false"`             // 是否使用了上游WebSocket
	WSMode               *RelayLogWSMode     `json:"ws_mode,omitempty"`                        // 上游 WebSocket 会话模式
	WSExecMode           *RelayLogWSExecMode `json:"ws_exec_mode,omitempty"`                   // 上游 WebSocket 事件处理方式
	WSRecovery           *RelayLogWSRecovery `json:"ws_recovery,omitempty"`                    // 本次请求触发的恢复动作
}
