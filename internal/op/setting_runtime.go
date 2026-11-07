package op

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/suprelory/octopus-plus/internal/model"
)

var settingWriteLock sync.Mutex
var settingAppliersLock sync.RWMutex
var settingAppliers = make(map[model.SettingKey]func(string) error)

// RegisterSettingApplier connects a derived runtime object to its persisted
// setting without making op depend on the scheduler or HTTP middleware.
// Appliers must not write settings. The returned function restores the previous
// registration, which also allows isolated tests to clean up their dependency.
func RegisterSettingApplier(key model.SettingKey, apply func(string) error) func() {
	settingAppliersLock.Lock()
	previous, existed := settingAppliers[key]
	settingAppliers[key] = apply
	settingAppliersLock.Unlock()
	return func() {
		settingAppliersLock.Lock()
		defer settingAppliersLock.Unlock()
		if existed {
			settingAppliers[key] = previous
		} else {
			delete(settingAppliers, key)
		}
	}
}

func applySettingLocked(key model.SettingKey, value string) error {
	settingAppliersLock.RLock()
	apply := settingAppliers[key]
	settingAppliersLock.RUnlock()
	if apply != nil {
		if err := apply(value); err != nil {
			return fmt.Errorf("apply runtime setting %s: %w", key, err)
		}
	}
	return nil
}

// ApplyRuntimeSettings reapplies all derived state after either restore path.
// Continue after errors so a failed task setting cannot prevent trusted proxies
// from adopting the restored trust boundary.
func ApplyRuntimeSettings() error {
	settingWriteLock.Lock()
	defer settingWriteLock.Unlock()
	settingAppliersLock.RLock()
	keys := make([]model.SettingKey, 0, len(settingAppliers))
	for key := range settingAppliers {
		keys = append(keys, key)
	}
	settingAppliersLock.RUnlock()
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var failures []error
	for _, key := range keys {
		value, err := SettingGetString(key)
		if err != nil {
			failures = append(failures, fmt.Errorf("load runtime setting %s: %w", key, err))
		}
		failures = append(failures, applySettingLocked(key, value))
	}
	return errors.Join(failures...)
}
