package op

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/cache"
)

var settingCache = cache.New[model.SettingKey, string](16)

func SettingList(ctx context.Context) ([]model.Setting, error) {
	settings := make([]model.Setting, 0, settingCache.Len())
	for key, value := range settingCache.GetAll() {
		settings = append(settings, model.Setting{
			Key:   key,
			Value: value,
		})
	}
	return settings, nil
}

func SettingGetString(key model.SettingKey) (string, error) {
	setting, ok := settingCache.Get(key)
	if !ok {
		return "", fmt.Errorf("setting not found")
	}
	return setting, nil
}

func SettingSetString(key model.SettingKey, value string) error {
	settingWriteLock.Lock()
	defer settingWriteLock.Unlock()
	valueCache, ok := settingCache.Get(key)
	if !ok {
		return fmt.Errorf("setting not found")
	}
	if valueCache == value {
		return applySettingLocked(key, value)
	}
	result := db.GetDB().Model(&model.Setting{Key: key}).Update("Value", value)
	if result.Error != nil {
		return fmt.Errorf("failed to set setting: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("failed to set setting, key not found")
	}
	settingCache.Set(key, value)
	return applySettingLocked(key, value)
}

func SettingGetInt(key model.SettingKey) (int, error) {
	setting, ok := settingCache.Get(key)
	if !ok {
		return 0, fmt.Errorf("setting not found")
	}
	return strconv.Atoi(setting)
}

func SettingGetBool(key model.SettingKey) (bool, error) {
	setting, ok := settingCache.Get(key)
	if !ok {
		for _, defaultSetting := range model.DefaultSettings() {
			if defaultSetting.Key == key {
				return strconv.ParseBool(defaultSetting.Value)
			}
		}
		return false, fmt.Errorf("setting not found")
	}
	return strconv.ParseBool(setting)
}

func SettingSetInt(key model.SettingKey, value int) error {
	return SettingSetString(key, strconv.Itoa(value))
}

func settingRefreshCache(ctx context.Context) error {
	settingWriteLock.Lock()
	defer settingWriteLock.Unlock()
	db := db.GetDB().WithContext(ctx)

	var settings []model.Setting
	if err := db.Find(&settings).Error; err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}

	existingKeys := make(map[model.SettingKey]bool)
	for _, setting := range settings {
		existingKeys[setting.Key] = true
	}

	defaultSettings := model.DefaultSettings()
	missingSettings := make([]model.Setting, 0, len(defaultSettings))

	for _, defaultSetting := range defaultSettings {
		if !existingKeys[defaultSetting.Key] {
			// Preserve deployments that already configured trusted proxies in
			// data/config.json. This seed runs only while the database key is
			// missing; once created, the runtime setting is authoritative and may
			// be cleared from the settings page.
			if defaultSetting.Key == model.SettingKeyTrustedProxies && len(conf.AppConfig.Server.TrustedProxies) > 0 {
				defaultSetting.Value = strings.Join(conf.AppConfig.Server.TrustedProxies, ",")
				if err := defaultSetting.Validate(); err != nil {
					return fmt.Errorf("invalid configured trusted proxies: %w", err)
				}
			}
			missingSettings = append(missingSettings, defaultSetting)
		}
	}

	if len(missingSettings) > 0 {
		if err := db.CreateInBatches(missingSettings, len(missingSettings)).Error; err != nil {
			return fmt.Errorf("failed to create missing settings: %w", err)
		}
		settings = append(settings, missingSettings...)
	}
	for _, setting := range settings {
		settingCache.Set(setting.Key, setting.Value)
	}
	return nil
}
