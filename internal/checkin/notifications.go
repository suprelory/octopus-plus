package checkin

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type NotificationConfig struct {
	Enabled, SuccessEnabled, ManualEnabled bool
	Channels                               notify.Config
	Cooldown                               time.Duration
	Threshold                              float64
}

// Explicitly selected by runtime wiring at cutover. Construction and tests do
// not access global settings or send to configured production destinations.
func LoadNotificationConfig() NotificationConfig {
	enabled, _ := op.SettingGetBool(model.SettingKeyCheckinNotifyEnabled)
	success, _ := op.SettingGetBool(model.SettingKeyCheckinNotifySuccessEnabled)
	manual, _ := op.SettingGetBool(model.SettingKeyCheckinNotifyManualEnabled)
	raw, _ := op.SettingGetString(model.SettingKeyNotificationChannels)
	legacy, _ := op.SettingGetString(model.SettingKeyCheckinNotifyWebhookURL)
	channels, err := notify.ResolveConfig(raw, legacy)
	if err != nil {
		enabled = false
	}
	seconds, err := op.SettingGetInt(model.SettingKeyCheckinNotifyCooldownSeconds)
	if err != nil || seconds < 0 || seconds > 7*24*60*60 {
		seconds = 3600
	}
	rawThreshold, _ := op.SettingGetString(model.SettingKeyCheckinLowBalanceThreshold)
	threshold, valid := finance.Number(rawThreshold)
	if !valid || threshold < 0 {
		threshold = 0
	}
	return NotificationConfig{Enabled: enabled, SuccessEnabled: success, ManualEnabled: manual, Channels: channels, Cooldown: time.Duration(seconds) * time.Second, Threshold: threshold}
}

type Notifications struct {
	store   *op.CheckinStore
	config  func() NotificationConfig
	client  *http.Client
	deliver func(context.Context, *http.Client, notify.Target, notify.Message) error
	mu      sync.Mutex
	running bool
	wake    bool
	done    chan struct{}
}

func NewNotifications(store *op.CheckinStore, config func() NotificationConfig) *Notifications {
	return &Notifications{store: store, config: config, client: notify.NewHTTPClient(), deliver: notify.Deliver}
}

// Coalesce completion bursts into one worker. A periodic RunDue also recovers
// completions and queued deliveries after a process restart.
func (n *Notifications) Wake() {
	n.mu.Lock()
	n.wake = true
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.done = make(chan struct{})
	n.mu.Unlock()
	go func() {
		for {
			n.mu.Lock()
			n.wake = false
			n.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			err := n.RunDue(ctx)
			cancel()
			if err != nil {
				log.Warnf("check-in notifications will resume on the next scan")
			}
			n.mu.Lock()
			if !n.wake {
				n.running = false
				close(n.done)
				n.mu.Unlock()
				return
			}
			n.mu.Unlock()
		}
	}()
}

// Wait waits for the current Wake worker and its coalesced scans. Callers must
// stop producing new work first; direct RunDue calls have their own lifecycle.
func (n *Notifications) Wait(ctx context.Context) error {
	n.mu.Lock()
	done, running := n.done, n.running
	n.mu.Unlock()
	if !running {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func notificationEvent(c model.CheckinCompletion, config NotificationConfig) (string, string) {
	entry := c.Entry
	if !config.Enabled || entry.Source != string(model.CheckinTriggerScheduled) && !config.ManualEnabled {
		return "", ""
	}
	if entry.Status == model.SiteExecutionStatusFailed {
		return "site_checkin_failed", entry.Reason
	}
	if entry.Status != model.SiteExecutionStatusSuccess {
		return "", ""
	}
	if c.Balance != nil && config.Threshold > 0 && *c.Balance < config.Threshold {
		return "site_checkin_low_balance", "low_balance"
	}
	if config.SuccessEnabled {
		return "site_checkin_success", entry.Reason
	}
	return "", ""
}

func notificationTargets(config NotificationConfig) map[string]notify.Target {
	targets := make(map[string]notify.Target)
	for _, target := range config.Channels.Targets() {
		digest := target.Fingerprint()
		targets[hex.EncodeToString(digest[:])] = target
	}
	return targets
}

func (n *Notifications) RunDue(ctx context.Context) error {
	completions, err := n.store.DueCompletions(ctx)
	if err != nil {
		return err
	}
	config := n.config()
	if config.Cooldown < 0 || config.Cooldown > 7*24*time.Hour {
		config.Cooldown = time.Hour
	}
	if !finance.Finite(config.Threshold) || config.Threshold < 0 {
		config.Threshold = 0
	}
	if err := config.Channels.Validate(); err != nil {
		return err
	}
	for _, completion := range completions {
		event, reason := notificationEvent(completion, config)
		var destinations []string
		if event != "" {
			for destination := range notificationTargets(config) {
				destinations = append(destinations, destination)
			}
		}
		if err := n.store.ExpandCompletion(ctx, completion, event, reason, destinations, config.Cooldown, config.Threshold); err != nil && !errors.Is(err, op.ErrCheckinNotificationState) {
			return err
		}
	}
	deliveries, err := n.store.PendingNotifications(ctx)
	if err != nil {
		return err
	}
	// Four bounded workers; pending records beyond this page remain in storage.
	var wg sync.WaitGroup
	queue := make(chan model.CheckinNotificationDelivery)
	failures := make(chan error, len(deliveries))
	for range 4 {
		wg.Go(func() {
			for delivery := range queue {
				if err := n.send(ctx, delivery); err != nil {
					failures <- err
				}
			}
		})
	}
	for _, delivery := range deliveries {
		queue <- delivery
	}
	close(queue)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (n *Notifications) send(ctx context.Context, pending model.CheckinNotificationDelivery) error {
	config := n.config()
	if err := config.Channels.Validate(); err != nil {
		return err
	}
	target, exists := notificationTargets(config)[pending.Destination]
	allowed := config.Enabled && exists && (pending.Event != "site_checkin_success" || config.SuccessEnabled)
	if pending.Event == "site_checkin_low_balance" && (!finance.Finite(config.Threshold) || config.Threshold <= 0) {
		allowed = false
	}
	delivery, completion, err := n.store.ClaimNotification(ctx, pending.ID, allowed, config.ManualEnabled)
	if err != nil || delivery == nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = n.deliver(sendCtx, n.client, target, notificationMessage(*completion, *delivery))
	cancel()
	persistCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	// Transport errors, including timeouts, cannot prove that no delivery occurred.
	return n.store.FinishNotification(persistCtx, *delivery, err == nil)
}

type notificationPayload struct {
	Event        string                    `json:"event"`
	Level        string                    `json:"level"`
	Title        string                    `json:"title"`
	Source       string                    `json:"source"`
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

func notificationMessage(completion model.CheckinCompletion, delivery model.CheckinNotificationDelivery) notify.Message {
	entry := completion.Entry
	event := notificationPayload{Event: delivery.Event, Source: entry.Source, LogID: entry.ID, SiteID: entry.SiteID, AccountID: entry.AccountID,
		SiteName: entry.SiteName, AccountName: entry.AccountName, Status: entry.Status, Reason: entry.Reason, Message: entry.Message, Reward: entry.Reward,
		FailureCount: completion.FailureCount, Balance: completion.Balance, OccurredAt: entry.FinishedAt.In(time.FixedZone("UTC+8", 8*3600))}
	switch delivery.Event {
	case "site_checkin_failed":
		event.Level, event.Title = "error", "签到失败"
	case "site_checkin_low_balance":
		event.Level, event.Title, event.Reason, event.Threshold = "warning", "签到后余额不足", "low_balance", &delivery.Threshold
	default:
		event.Level, event.Title = "info", "签到成功"
		if entry.Reason == model.SiteCheckinReasonAlreadyCheckedIn {
			event.Title = "今日已签到"
		}
	}
	source := "定时签到"
	if event.Source != string(model.CheckinTriggerScheduled) {
		source = "手动签到"
	}
	lines := []string{fmt.Sprintf("站点：%s\n账号：%s", event.SiteName, event.AccountName), "触发：" + source, "结果：" + event.Title, "详情：" + event.Message}
	variables := map[string]string{"event": event.Title, "site": event.SiteName, "account": event.AccountName, "source": source, "detail": event.Message, "reward": event.Reward, "failure_count": strconv.Itoa(event.FailureCount)}
	if event.Reward != "" {
		lines = append(lines, "签到奖励："+event.Reward)
	}
	if event.Balance != nil {
		variables["balance"] = fmt.Sprintf("%.4f", *event.Balance)
		lines = append(lines, "余额："+variables["balance"]+" USD")
	}
	if event.Threshold != nil {
		variables["threshold"] = fmt.Sprintf("%.4f", *event.Threshold)
		lines = append(lines, "低余额阈值："+variables["threshold"]+" USD")
	}
	if event.FailureCount > 0 {
		lines = append(lines, fmt.Sprintf("连续失败：%d 次", event.FailureCount))
	}
	lines = append(lines, "时间："+event.OccurredAt.Format(time.RFC3339))
	return notify.Message{Level: event.Level, Title: event.Title, Text: strings.Join(lines, "\n"), Timestamp: event.OccurredAt, Payload: event, Variables: variables}
}
