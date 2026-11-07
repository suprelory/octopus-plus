package task

import (
	"context"
	"strconv"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/price"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type intervalTask struct {
	key        model.SettingKey
	name       string
	unit       time.Duration
	runOnStart bool
	fn         func()
}

func intervalTasks() []intervalTask {
	return []intervalTask{
		{model.SettingKeyModelInfoUpdateInterval, string(model.SettingKeyModelInfoUpdateInterval), time.Hour, true, updateModelPrices},
		{model.SettingKeySyncLLMInterval, string(model.SettingKeySyncLLMInterval), time.Hour, true, SyncModelsTask},
		{model.SettingKeySiteSyncInterval, string(model.SettingKeySiteSyncInterval), time.Hour, true, SiteSyncTask},
		{model.SettingKeyStatsSaveInterval, TaskStatsSave, time.Minute, false, op.StatsSaveDBTask},
		{model.SettingKeyWebDAVBackupInterval, string(model.SettingKeyWebDAVBackupInterval), time.Hour, false, WebDAVBackupTask},
	}
}

func initIntervalTasks() func() {
	var unregister []func()
	for _, spec := range intervalTasks() {
		unregister = append(unregister, op.RegisterSettingApplier(spec.key, func(value string) error {
			setting := model.Setting{Key: spec.key, Value: value}
			if err := setting.Validate(); err != nil {
				// Old/manual backups may contain invalid or overflowing intervals.
				// Disable the schedule and report the failed application.
				_ = Configure(spec.name, 0, spec.runOnStart, spec.fn)
				return err
			}
			count, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			return Configure(spec.name, time.Duration(count)*spec.unit, spec.runOnStart, spec.fn)
		}))
	}
	if err := op.ApplyRuntimeSettings(); err != nil {
		log.Warnf("failed to apply runtime settings during task initialization: %v", err)
	}
	return func() {
		for i := len(unregister) - 1; i >= 0; i-- {
			unregister[i]()
		}
	}
}

func updateModelPrices() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := price.UpdateLLMPrice(ctx); err != nil {
		log.Warnf("failed to update price info: %v", err)
	}
}
