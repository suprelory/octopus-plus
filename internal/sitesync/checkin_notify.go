package sitesync

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

type checkinNotificationConfig struct {
	enabled        bool
	successEnabled bool
	manualEnabled  bool
	channels       notify.Config
	cooldown       time.Duration
	threshold      float64
}

// Payloads contain only the persisted outcome and display names. Credentials,
// site URLs, configured HTTP headers and raw upstream responses are excluded.
type checkinNotification struct {
	Event        string                    `json:"event"`
	Level        string                    `json:"level"`
	Title        string                    `json:"title"`
	Source       SiteBatchTrigger          `json:"source"`
	LogID        int64                     `json:"log_id,string"`
	SiteID       int                       `json:"site_id"`
	AccountID    int                       `json:"account_id"`
	SiteName     string                    `json:"site_name"`
	AccountName  string                    `json:"account_name"`
	Status       model.SiteExecutionStatus `json:"status"`
	Reason       string                    `json:"reason"`
	Message      string                    `json:"message"`
	Reward       string                    `json:"reward,omitempty"`
	FailureCount int                       `json:"failure_count"`
	Balance      *float64                  `json:"balance,omitempty"`
	Threshold    *float64                  `json:"threshold,omitempty"`
	OccurredAt   time.Time                 `json:"occurred_at"`
}

type checkinNotificationKey struct {
	destination [32]byte
	accountID   int
	event       string
	reason      string
}

type checkinNotificationReservation struct {
	inFlight bool
	until    time.Time
}

type checkinNotifier struct {
	config  func() checkinNotificationConfig
	client  *http.Client
	mu      sync.Mutex
	recent  map[checkinNotificationKey]checkinNotificationReservation
	slots   chan struct{}
	workers chan struct{}
	wg      sync.WaitGroup
}

var (
	checkinNotificationLocation = time.FixedZone("UTC+8", 8*60*60)
	checkinNotifications        = newCheckinNotifier(loadCheckinNotificationConfig)
)

func newCheckinNotifier(config func() checkinNotificationConfig) *checkinNotifier {
	return &checkinNotifier{
		config: config,
		client: notify.NewHTTPClient(),
		recent: make(map[checkinNotificationKey]checkinNotificationReservation),
		slots:  make(chan struct{}, 128), workers: make(chan struct{}, 4),
	}
}

func loadCheckinNotificationConfig() checkinNotificationConfig {
	enabled, _ := op.SettingGetBool(model.SettingKeyCheckinNotifyEnabled)
	successEnabled, _ := op.SettingGetBool(model.SettingKeyCheckinNotifySuccessEnabled)
	manualEnabled, _ := op.SettingGetBool(model.SettingKeyCheckinNotifyManualEnabled)
	rawChannels, _ := op.SettingGetString(model.SettingKeyNotificationChannels)
	webhook, _ := op.SettingGetString(model.SettingKeyCheckinNotifyWebhookURL)
	channels, err := notify.ResolveConfig(rawChannels, webhook)
	if err != nil {
		enabled = false
	}
	seconds, err := op.SettingGetInt(model.SettingKeyCheckinNotifyCooldownSeconds)
	if err != nil || seconds < 0 || seconds > 7*24*60*60 {
		seconds = 3600
	}
	raw, _ := op.SettingGetString(model.SettingKeyCheckinLowBalanceThreshold)
	threshold, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 {
		threshold = 0
	}
	return checkinNotificationConfig{
		enabled: enabled, successEnabled: successEnabled, manualEnabled: manualEnabled,
		channels: channels, cooldown: time.Duration(seconds) * time.Second, threshold: threshold,
	}
}

// Called only after an execution has committed. Delivery is bounded and
// asynchronous, so notification latency or failure never changes its outcome.
func (n *checkinNotifier) notify(site *model.Site, account *model.SiteAccount, result *model.SiteCheckinResult, balance siteBalanceFetchResult, trigger SiteBatchTrigger) {
	config := n.config()
	if !config.enabled || (trigger != SiteBatchTriggerScheduled && !config.manualEnabled) || site == nil || account == nil || result == nil || result.LogID == 0 {
		return
	}
	event := checkinNotification{
		LogID: result.LogID, SiteID: site.ID, AccountID: account.ID,
		SiteName: site.Name, AccountName: account.Name, Status: result.Status,
		Reason: result.Reason, Message: result.Message, Reward: result.Reward, Source: trigger,
		FailureCount: account.CheckinFailureCount,
		OccurredAt:   time.Now().UTC(),
	}
	if result.Status == model.SiteExecutionStatusFailed {
		event.Event, event.Level, event.Title = "site_checkin_failed", "error", "签到失败"
	} else if result.Status == model.SiteExecutionStatusSuccess && balance.ok && config.threshold > 0 && balance.balance < config.threshold {
		event.Event, event.Reason = "site_checkin_low_balance", "low_balance"
		event.Level, event.Title = "warning", "签到后余额不足"
		event.Balance, event.Threshold = &balance.balance, &config.threshold
	} else if result.Status == model.SiteExecutionStatusSuccess && config.successEnabled {
		event.Event, event.Level, event.Title = "site_checkin_success", "info", "签到成功"
		if result.Reason == model.SiteCheckinReasonAlreadyCheckedIn {
			event.Title = "今日已签到"
		}
		if balance.ok {
			event.Balance = &balance.balance
		}
	} else {
		return
	}
	n.enqueue(config, event)
}

func (n *checkinNotifier) enqueue(config checkinNotificationConfig, event checkinNotification) bool {
	queued := false
	for _, target := range config.channels.Targets() {
		if n.enqueueTarget(target, config.cooldown, event) {
			queued = true
		}
	}
	return queued
}

func (n *checkinNotifier) enqueueTarget(target notify.Target, cooldown time.Duration, event checkinNotification) bool {
	key := checkinNotificationKey{target.Fingerprint(), event.AccountID, event.Event, event.Reason}
	now := time.Now()
	n.mu.Lock()
	for key, item := range n.recent {
		if !item.inFlight && !now.Before(item.until) {
			delete(n.recent, key)
		}
	}
	if _, exists := n.recent[key]; exists {
		n.mu.Unlock()
		return false
	}
	// Bound both outstanding work and cooldown state on large installations.
	if len(n.recent) >= 10000 {
		n.mu.Unlock()
		log.Warnf("checkin notification capacity reached for account %d", event.AccountID)
		return false
	}
	select {
	case n.slots <- struct{}{}:
	default:
		n.mu.Unlock()
		log.Warnf("checkin notification queue full for account %d", event.AccountID)
		return false
	}
	n.recent[key] = checkinNotificationReservation{inFlight: true}
	n.mu.Unlock()
	n.wg.Add(1)
	go func() {
		// Waiters own the entire worker lifetime, including safe.Run's exit log.
		defer n.wg.Done()
		safe.Run("site-checkin-notification", func() {
			delivered := false
			defer func() {
				n.mu.Lock()
				if delivered && cooldown > 0 {
					n.recent[key] = checkinNotificationReservation{until: time.Now().Add(cooldown)}
				} else {
					delete(n.recent, key)
				}
				n.mu.Unlock()
				<-n.slots
			}()
			n.workers <- struct{}{}
			defer func() { <-n.workers }()
			started := time.Now()
			if err := n.deliver(target, event); err != nil {
				// Deliver returns sanitized errors; keep a second redaction boundary
				// for alternate HTTP transports.
				log.Warnw("checkin.notification.failed", "account_id", event.AccountID,
					"event", event.Event, "channel", string(target.Kind), "error", log.SafeError(err),
					"duration_ms", time.Since(started).Milliseconds())
				return
			}
			delivered = true
		})
	}()
	return true
}

func (n *checkinNotifier) deliver(target notify.Target, event checkinNotification) error {
	event.OccurredAt = event.OccurredAt.In(checkinNotificationLocation)
	source := "定时签到"
	if event.Source == SiteBatchTriggerManual {
		source = "手动签到"
	}
	lines := []string{
		fmt.Sprintf("站点：%s\n账号：%s", event.SiteName, event.AccountName),
		"触发：" + source,
		"结果：" + event.Title,
		"详情：" + event.Message,
	}
	if event.Reward != "" {
		lines = append(lines, "签到奖励："+event.Reward)
	}
	if event.Balance != nil {
		lines = append(lines, fmt.Sprintf("余额：%.4f USD", *event.Balance))
	}
	if event.Threshold != nil {
		lines = append(lines, fmt.Sprintf("低余额阈值：%.4f USD", *event.Threshold))
	}
	if event.FailureCount > 0 {
		lines = append(lines, fmt.Sprintf("连续失败：%d 次", event.FailureCount))
	}
	lines = append(lines, "时间："+event.OccurredAt.Format(time.RFC3339))
	variables := map[string]string{
		"event": event.Title, "site": event.SiteName, "account": event.AccountName,
		"source": source, "detail": event.Message, "reward": event.Reward,
		"failure_count": strconv.Itoa(event.FailureCount),
	}
	if event.Balance != nil {
		variables["balance"] = fmt.Sprintf("%.4f", *event.Balance)
	}
	if event.Threshold != nil {
		variables["threshold"] = fmt.Sprintf("%.4f", *event.Threshold)
	}
	return notify.Deliver(context.Background(), n.client, target, notify.Message{
		Level: event.Level, Title: event.Title, Text: strings.Join(lines, "\n"),
		Timestamp: event.OccurredAt, Payload: event,
		Variables: variables,
	})
}
