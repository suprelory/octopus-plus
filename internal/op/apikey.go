package op

import (
	"context"
	"fmt"
	"sync"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/cache"
	"gorm.io/gorm"
)

var apiKeyCache = cache.New[int, model.APIKey](16)
var apiKeyIDMap = cache.New[string, int](16)

// Serialize DB commits and cache publication with key deletion and settlement.
// When combined, acquire apiKeyWriteLock before statsPersistenceLock.
var apiKeyWriteLock sync.RWMutex

func APIKeyCreate(key *model.APIKey, ctx context.Context) error {
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	created := *key
	if err := db.GetDB().WithContext(ctx).Create(&created).Error; err != nil {
		return fmt.Errorf("failed to create API key: %w", err)
	}
	*key = created
	apiKeyCache.Set(key.ID, *key)
	apiKeyIDMap.Set(key.APIKey, key.ID)
	return nil
}

func APIKeyUpdate(key *model.APIKey, ctx context.Context) error {
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	existing, ok := apiKeyCache.Get(key.ID)
	if !ok {
		return fmt.Errorf("API key not found")
	}
	if err := db.GetDB().WithContext(ctx).Omit("api_key").Save(key).Error; err != nil {
		return fmt.Errorf("failed to update API key: %w", err)
	}
	key.APIKey = existing.APIKey
	apiKeyCache.Set(key.ID, *key)
	return nil
}

func APIKeyList(ctx context.Context) ([]model.APIKey, error) {
	apiKeyWriteLock.RLock()
	defer apiKeyWriteLock.RUnlock()
	keys := make([]model.APIKey, 0, apiKeyCache.Len())
	for _, apiKey := range apiKeyCache.GetAll() {
		keys = append(keys, apiKey)
	}
	return keys, nil
}

func APIKeyGet(id int, ctx context.Context) (model.APIKey, error) {
	apiKeyWriteLock.RLock()
	defer apiKeyWriteLock.RUnlock()
	return apiKeyGet(id)
}

func apiKeyGet(id int) (model.APIKey, error) {
	apiKey, ok := apiKeyCache.Get(id)
	if !ok {
		return model.APIKey{}, fmt.Errorf("API key not found")
	}
	return apiKey, nil
}

func APIKeyGetByAPIKey(apiKey string, ctx context.Context) (model.APIKey, error) {
	apiKeyWriteLock.RLock()
	defer apiKeyWriteLock.RUnlock()
	id, ok := apiKeyIDMap.Get(apiKey)
	if !ok {
		return model.APIKey{}, fmt.Errorf("API key not found")
	}
	return apiKeyGet(id)
}

func APIKeyDelete(id int, ctx context.Context) error {
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	// A flush must not persist a pre-delete snapshot after the transaction.
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	var k model.APIKey
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&k, id).Error; err != nil {
			return fmt.Errorf("load API key: %w", err)
		}
		if err := tx.Where("api_key_id = ?", id).Delete(&model.StatsAPIKey{}).Error; err != nil {
			return fmt.Errorf("failed to delete stats API key: %w", err)
		}
		if err := tx.Delete(&k).Error; err != nil {
			return fmt.Errorf("failed to delete API key: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	clearAPIKeyStatsCache(id)
	RateLimitDel(id)
	apiKeyCache.Del(k.ID)
	apiKeyIDMap.Del(k.APIKey)
	return nil
}

func apiKeyRefreshCache(ctx context.Context) error {
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	apiKeys := []model.APIKey{}
	if err := db.GetDB().WithContext(ctx).Find(&apiKeys).Error; err != nil {
		return err
	}
	publishAPIKeysLocked(apiKeys)
	return nil
}

func publishAPIKeysLocked(apiKeys []model.APIKey) {
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	for _, apiKey := range apiKeys {
		apiKeyCache.Set(apiKey.ID, apiKey)
		apiKeyIDMap.Set(apiKey.APIKey, apiKey.ID)
	}
}
