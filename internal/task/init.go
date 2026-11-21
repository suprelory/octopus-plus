package task

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const (
	TaskPriceUpdate          = "price_update"
	TaskStatsSave            = "stats_save"
	TaskRelayLogSave         = "relay_log_save"
	TaskSyncLLM              = "sync_llm"
	TaskCleanLLM             = "clean_llm"
	TaskBaseUrlDelay         = "base_url_delay"
	TaskSiteSync             = "site_sync"
	TaskSiteCheckin          = "site_checkin"
	TaskCheckinNotifications = "checkin_notifications"
	TaskWSAffinityCleanup    = "ws_affinity_cleanup"
	TaskWebDAVBackup         = "webdav_backup"
	SiteCheckinScanInterval  = 10 * time.Minute
)

func Init(services *checkin.Services) {
	tasksMu.Lock()
	runtimeContext, stopRuntime = context.WithCancel(context.Background())
	tasksMu.Unlock()
	unregisterSettings = initIntervalTasks()

	// 注册基础URL延迟任务
	Register(TaskBaseUrlDelay, 24*time.Hour, true, ChannelBaseUrlDelayTask)

	// 签到任务只负责高频扫描；每个账号的实际执行时间由 next_auto_checkin_at 控制。
	Register(TaskSiteCheckin, SiteCheckinScanInterval, true, func() {
		ctx, cancel := context.WithTimeout(taskContext(), time.Hour)
		defer cancel()
		if err := services.Jobs.RunDue(ctx); err != nil && ctx.Err() == nil {
			log.Warnf("check-in scan failed: %v", err)
		}
	})
	Register(TaskCheckinNotifications, time.Minute, true, services.Notifications.Wake)

	// 注册中继日志保存任务
	Register(TaskRelayLogSave, time.Hour, false, func() {
		if err := op.RelayLogSaveDBTask(taskContext()); err != nil {
			log.Warnf("relay log save db task failed: %v", err)
		}
	})

	Register(TaskWSAffinityCleanup, 10*time.Minute, false, func() {
		ctx, cancel := context.WithTimeout(taskContext(), 30*time.Second)
		defer cancel()
		deleted, err := op.WSResponseAffinityCleanup(ctx, time.Now())
		if err != nil {
			log.Warnf("ws response affinity cleanup failed: %v", err)
			return
		}
		if deleted > 0 {
			log.Debugf("ws response affinity cleanup removed %d expired rows", deleted)
		}
	})
}
