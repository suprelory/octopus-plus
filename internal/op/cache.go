package op

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/suprelory/octopus-plus/internal/conf"
)

func InitCache() error {
	return refreshCache(true)
}

// DBImportIncremental publishes restored statistics atomically with its commit.
// This follow-up only refreshes configuration: reloading stats here could erase
// requests that settled after the import returned. includeStats is retained for
// compatibility with callers of the former two-step restore API.
func RefreshCacheAfterImport(includeStats bool) error {
	refreshErr := refreshCache(false)
	return errors.Join(refreshErr, ApplyRuntimeSettings())
}

func refreshCache(includeStats bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), conf.CacheInitTimeout())
	defer cancel()
	if err := settingRefreshCache(ctx); err != nil {
		return fmt.Errorf("setting refresh cache error: %v", err)
	}
	if err := refreshChannels(ctx, !includeStats); err != nil {
		return fmt.Errorf("channel refresh cache error: %v", err)
	}
	if err := proxyConfigurationRefreshCache(ctx); err != nil {
		return fmt.Errorf("proxy configuration refresh cache error: %v", err)
	}
	if err := groupRefreshCache(ctx); err != nil {
		return fmt.Errorf("group refresh cache error: %v", err)
	}
	if err := apiKeyRefreshCache(ctx); err != nil {
		return fmt.Errorf("api key refresh cache error: %v", err)
	}
	if err := llmRefreshCache(ctx); err != nil {
		return fmt.Errorf("llm refresh cache error: %v", err)
	}
	if includeStats {
		if err := statsRefreshCache(ctx); err != nil {
			return fmt.Errorf("stats refresh cache error: %v", err)
		}
	}
	return nil
}

func SaveCache() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StatsSaveDB(ctx); err != nil {
		return err
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		return err
	}
	if err := RelayLogSaveDBTask(ctx); err != nil {
		return err
	}
	return nil
}
