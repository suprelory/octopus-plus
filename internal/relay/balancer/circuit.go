package balancer

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

// CircuitState 熔断器状态
type CircuitState int

type FailureKind int

const (
	StateClosed   CircuitState = iota // 正常通行
	StateOpen                         // 熔断中，拒绝所有请求
	StateHalfOpen                     // 半开，仅允许单个试探请求
)

const (
	// FailureIgnored does not affect breaker state. Request validation and
	// client cancellation use this kind because retrying another credential
	// cannot repair the caller's request.
	FailureIgnored FailureKind = iota
	FailureTransient
	FailureRateLimit
	FailureQuota
	FailureModelUnsupported
	FailureAuthentication
	FailurePermission
)

const (
	// Permanent credential/model faults should not be retried on the normal
	// one-minute transient cadence. They remain model/key scoped and can still
	// be cleared by a successful probe or an explicit channel reset.
	modelUnsupportedCooldown = 12 * time.Hour
	authenticationCooldown   = 30 * time.Minute
)

// circuitEntry 单个熔断器条目
type circuitEntry struct {
	State               CircuitState
	ConsecutiveFailures int64
	LastFailureTime     time.Time
	// RetryAt is the absolute time at which a tripped entry may be probed.
	// Keeping the deadline avoids losing provider Retry-After precision when a
	// request waits in a retry queue.
	RetryAt       time.Time
	TripCount     int // 累计熔断触发次数（用于指数退避）
	HalfOpenSince time.Time
	mu            sync.Mutex
}

// 全局熔断器存储
var globalBreaker sync.Map // key: string -> value: *circuitEntry

// circuitKey 生成熔断器键：channelID:channelKeyID:modelName
func circuitKey(channelID, keyID int, modelName string) string {
	return fmt.Sprintf("%d:%d:%s", channelID, keyID, modelName)
}

func resetCircuitBreakerByChannel(channelID int) {
	prefix := fmt.Sprintf("%d:", channelID)
	globalBreaker.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			globalBreaker.Delete(k)
		}
		return true
	})
}

// getOrCreateEntry 获取或创建熔断器条目
func getOrCreateEntry(key string) *circuitEntry {
	if v, ok := globalBreaker.Load(key); ok {
		return v.(*circuitEntry)
	}
	entry := &circuitEntry{State: StateClosed}
	actual, _ := globalBreaker.LoadOrStore(key, entry)
	return actual.(*circuitEntry)
}

// getThreshold 获取熔断阈值配置
func getThreshold() int64 {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerThreshold)
	if err != nil || v <= 0 {
		return 5
	}
	return int64(v)
}

// GetCooldown 获取当前冷却时间（带指数退避）
func GetCooldown(tripCount int) time.Duration {
	base, err := op.SettingGetInt(model.SettingKeyCircuitBreakerCooldown)
	if err != nil || base <= 0 {
		base = 60
	}
	maxCooldown, err := op.SettingGetInt(model.SettingKeyCircuitBreakerMaxCooldown)
	if err != nil || maxCooldown <= 0 {
		maxCooldown = 600
	}

	// 指数退避：baseCooldown * 2^(tripCount-1)
	cooldown := base
	if tripCount > 1 {
		shift := tripCount - 1
		if shift > 20 { // 防止溢出
			shift = 20
		}
		cooldown = base << shift
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return time.Duration(cooldown) * time.Second
}

// CanAttempt checks availability without claiming a half-open probe. Selection
// must still call IsTripped immediately before using the key.
func CanAttempt(channelID, keyID int, modelName string) bool {
	for _, key := range scopedCircuitKeys(channelID, keyID, modelName) {
		if !canAttemptCircuit(key) {
			return false
		}
	}
	return true
}

func canAttemptCircuit(key string) bool {
	v, ok := globalBreaker.Load(key)
	if !ok {
		return true
	}
	entry := v.(*circuitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.State == StateClosed {
		return true
	}
	if entry.State != StateOpen {
		return false
	}
	retryAt := entry.RetryAt
	if retryAt.IsZero() {
		retryAt = entry.LastFailureTime.Add(GetCooldown(entry.TripCount))
	}
	return !time.Now().Before(retryAt)
}

// IsTripped 检查通道是否处于熔断状态
// 返回 tripped=true 表示该通道应被跳过，remaining 为剩余冷却时间
func IsTripped(channelID, keyID int, modelName string) (tripped bool, remaining time.Duration) {
	// Check every scope before claiming a half-open probe.
	keys := scopedCircuitKeys(channelID, keyID, modelName)
	for _, key := range keys {
		if !canAttemptCircuit(key) {
			return isCircuitTripped(key)
		}
	}
	for _, key := range keys {
		if tripped, remaining := isCircuitTripped(key); tripped {
			return true, remaining
		}
	}
	return false, 0
}

func scopedCircuitKeys(channelID, keyID int, modelName string) []string {
	keys := []string{circuitKey(channelID, 0, ""), circuitKey(channelID, 0, modelName), circuitKey(channelID, keyID, ""), circuitKey(channelID, keyID, modelName)}
	result := make([]string, 0, len(keys))
	seen := make(map[string]bool)
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result
}

func isCircuitTripped(key string) (tripped bool, remaining time.Duration) {
	v, ok := globalBreaker.Load(key)
	if !ok {
		return false, 0 // 无记录，视为 Closed
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	switch entry.State {
	case StateClosed:
		return false, 0

	case StateOpen:
		now := time.Now()
		retryAt := entry.RetryAt
		if retryAt.IsZero() {
			retryAt = entry.LastFailureTime.Add(GetCooldown(entry.TripCount))
		}
		if !now.Before(retryAt) {
			entry.State = StateHalfOpen
			entry.HalfOpenSince = now
			entry.RetryAt = time.Time{}
			log.Infof("circuit breaker [%s] Open -> HalfOpen (retry_at=%v elapsed)", key, retryAt)
			return false, 0
		}
		// 仍在冷却中
		return true, retryAt.Sub(now)

	case StateHalfOpen:
		cooldown := GetCooldown(entry.TripCount)
		if entry.HalfOpenSince.IsZero() {
			entry.HalfOpenSince = time.Now()
		}
		if time.Since(entry.HalfOpenSince) >= cooldown {
			entry.State = StateOpen
			entry.LastFailureTime = time.Now()
			entry.HalfOpenSince = time.Time{}
			log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe timed out, cooldown=%v)", key, cooldown)
			return true, cooldown
		}
		// 已有试探请求在进行中，拒绝其他请求
		return true, 0

	default:
		return false, 0
	}
}

// RecordSuccess 记录成功，重置熔断器状态
func RecordSuccess(channelID, keyID int, modelName string) {
	for _, key := range scopedCircuitKeys(channelID, keyID, modelName) {
		recordCircuitSuccess(key)
	}
}

func recordCircuitSuccess(key string) {
	v, ok := globalBreaker.Load(key)
	if !ok {
		return
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.State == StateHalfOpen {
		log.Infof("circuit breaker [%s] HalfOpen -> Closed (probe succeeded)", key)
	}

	// 重置全部状态
	entry.State = StateClosed
	entry.ConsecutiveFailures = 0
	entry.TripCount = 0
	entry.RetryAt = time.Time{}
	entry.HalfOpenSince = time.Time{}
}

// RetryAt returns the currently stored absolute breaker deadline. It is useful
// for diagnostics and for callers that need to forward an exact cooldown.
func RetryAt(channelID, keyID int, modelName string) (time.Time, bool) {
	var deadline time.Time
	for _, key := range scopedCircuitKeys(channelID, keyID, modelName) {
		if at, ok := circuitRetryAt(key); ok && at.After(deadline) {
			deadline = at
		}
	}
	return deadline, !deadline.IsZero()
}

func circuitRetryAt(key string) (time.Time, bool) {
	v, ok := globalBreaker.Load(key)
	if !ok {
		return time.Time{}, false
	}
	entry := v.(*circuitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.State != StateOpen || entry.RetryAt.IsZero() {
		return time.Time{}, false
	}
	return entry.RetryAt, true
}

// RecordFailure records a failure without an externally supplied deadline.
// New relay code should prefer RecordFailureAt when Retry-After was present.
func RecordFailure(channelID, keyID int, modelName string, kind FailureKind) {
	RecordFailureAt(channelID, keyID, modelName, kind, time.Time{})
}

func RecordScopedFailureAt(channelID, keyID int, modelName string, kind FailureKind, retryAt time.Time, scope string) {
	switch scope {
	case "key":
		modelName = ""
	case "model":
		keyID = 0
	case "channel":
		keyID, modelName = 0, ""
	}
	RecordFailureAt(channelID, keyID, modelName, kind, retryAt)
}

// RecordFailureAt records a failure and, for rate/quota/provider errors, the
// exact absolute deadline supplied by the upstream. Request and cancellation
// failures are ignored before an entry is allocated.
func RecordFailureAt(channelID, keyID int, modelName string, kind FailureKind, retryAt time.Time) {
	if kind == FailureIgnored {
		return
	}
	key := circuitKey(channelID, keyID, modelName)
	entry := getOrCreateEntry(key)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := time.Now()
	entry.LastFailureTime = now
	entry.HalfOpenSince = time.Time{}

	switch entry.State {
	case StateClosed:
		entry.RetryAt = time.Time{}
		if immediateFailureKind(kind) {
			entry.State = StateOpen
			entry.ConsecutiveFailures = 0
			if entry.TripCount <= 0 {
				entry.TripCount = 1
			}
			entry.RetryAt = retryDeadline(now, retryAt, entry.TripCount, kind)
			log.Warnf("circuit breaker [%s] Closed -> Open (%s, retry_at=%v)", key, failureKindName(kind), entry.RetryAt)
			return
		}
		entry.ConsecutiveFailures++
		threshold := getThreshold()
		if entry.ConsecutiveFailures >= threshold {
			entry.State = StateOpen
			entry.TripCount++
			entry.RetryAt = retryDeadline(now, retryAt, entry.TripCount, kind)
			log.Warnf("circuit breaker [%s] Closed -> Open (failures=%d >= threshold=%d, tripCount=%d, cooldown=%v)",
				key, entry.ConsecutiveFailures, threshold, entry.TripCount, GetCooldown(entry.TripCount))
		}

	case StateHalfOpen:
		entry.RetryAt = time.Time{}
		if immediateFailureKind(kind) {
			entry.State = StateOpen
			if entry.TripCount <= 0 {
				entry.TripCount = 1
			}
			entry.RetryAt = retryDeadline(now, retryAt, entry.TripCount, kind)
			log.Warnf("circuit breaker [%s] HalfOpen -> Open (%s, tripCount=%d, retry_at=%v)",
				key, failureKindName(kind), entry.TripCount, entry.RetryAt)
			return
		}
		// 试探失败，重新进入 Open 状态，TripCount 递增（冷却时间翻倍）
		entry.State = StateOpen
		entry.TripCount++
		entry.ConsecutiveFailures = 0 // 重新开始计数
		entry.RetryAt = retryDeadline(now, retryAt, entry.TripCount, kind)
		log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe failed, tripCount=%d, cooldown=%v)",
			key, entry.TripCount, GetCooldown(entry.TripCount))

	case StateOpen:
		// 理论上不应该在 Open 状态下接收到失败记录（请求应被拒绝），
		// 但为安全起见只延长 deadline，不能把已有精确 Retry-After 清掉。
		if retryAt.After(entry.RetryAt) {
			entry.RetryAt = retryAt
		}
	}
}

func immediateFailureKind(kind FailureKind) bool {
	switch kind {
	case FailureRateLimit, FailureQuota, FailureModelUnsupported, FailureAuthentication, FailurePermission:
		return true
	default:
		return false
	}
}

func retryDeadline(now, requested time.Time, tripCount int, kind FailureKind) time.Time {
	if !requested.IsZero() && requested.After(now) {
		return requested
	}
	switch kind {
	case FailureModelUnsupported:
		return now.Add(modelUnsupportedCooldown)
	case FailureAuthentication, FailurePermission:
		return now.Add(authenticationCooldown)
	}
	return now.Add(GetCooldown(tripCount))
}

func failureKindName(kind FailureKind) string {
	switch kind {
	case FailureTransient:
		return "transient"
	case FailureRateLimit:
		return "rate_limit"
	case FailureQuota:
		return "quota"
	case FailureModelUnsupported:
		return "model_unsupported"
	case FailureAuthentication:
		return "authentication"
	case FailurePermission:
		return "permission"
	default:
		return "ignored"
	}
}
