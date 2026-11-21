package op

import (
	"testing"

	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSettingRefreshSeedsTrustedProxiesFromStartupConfigOnce(t *testing.T) {
	ctx := setupOpTestDB(t)
	previousConfig := conf.AppConfig
	conf.AppConfig.Server.TrustedProxies = []string{"172.24.0.1", "10.0.0.0/24"}
	settingCache.Clear()
	t.Cleanup(func() {
		conf.AppConfig = previousConfig
		settingCache.Clear()
	})

	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache: %v", err)
	}
	got, err := SettingGetString(model.SettingKeyTrustedProxies)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.0.0/24,172.24.0.1" {
		t.Fatalf("seeded trusted proxies = %q", got)
	}

	if err := SettingSetString(model.SettingKeyTrustedProxies, ""); err != nil {
		t.Fatalf("clear trusted proxies: %v", err)
	}
	settingCache.Clear()
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("refresh existing settings: %v", err)
	}
	got, err = SettingGetString(model.SettingKeyTrustedProxies)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("existing empty trusted proxies were reseeded: %q", got)
	}
}
