package op

import (
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
)

func (s *dbImportState) importProxies() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	proxyConfigIDMap := s.proxyIDs
	// 1. ProxyConfigurations (dedup by url; disambiguate name conflicts)
	for i := range dump.ProxyConfigurations {
		proxyConfig := dump.ProxyConfigurations[i]
		oldID := proxyConfig.ID
		proxyConfig.ID = 0
		proxyConfig.ReferenceCount = 0
		if err := proxyConfig.Validate(); err != nil {
			return fmt.Errorf("import proxy_configurations: %w", err)
		}

		var existing model.ProxyConfiguration
		if err := tx.Where("url = ?", proxyConfig.URL).First(&existing).Error; err == nil {
			if proxyConfig.Enabled && !existing.Enabled {
				if err := tx.Model(&existing).Update("enabled", true).Error; err != nil {
					return fmt.Errorf("import proxy_configurations: %w", err)
				}
			}
			proxyConfigIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import proxy_configurations: %w", err)
		}
		if err := tx.Where("name = ?", proxyConfig.Name).First(&existing).Error; err == nil {
			oldName := proxyConfig.Name
			proxyConfig.Name = uniqueProxyConfigName(proxyConfig.Name, tx)
			log.Warnw("proxy configuration name conflict during import",
				"old_id", oldID,
				"existing_id", existing.ID,
				"existing_url", existing.URL,
				"import_url", proxyConfig.URL,
				"old_name", oldName,
				"new_name", proxyConfig.Name,
			)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import proxy_configurations: %w", err)
		}
		if err := tx.Create(&proxyConfig).Error; err != nil {
			return fmt.Errorf("import proxy_configurations: %w", err)
		}
		proxyConfigIDMap[oldID] = proxyConfig.ID
		res.RowsAffected["proxy_configurations"]++
	}
	return nil
}

func (s *dbImportState) importChannels() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	channelIDMap := s.channelIDs
	proxyConfigIDMap := s.proxyIDs
	// 2. Channels (dedup by name)
	for i := range dump.Channels {
		ch := dump.Channels[i]
		oldID := ch.ID
		if !supportedChannelType(ch.Type) {
			return fmt.Errorf("import channels: unsupported channel type: %d", ch.Type)
		}
		ch.ID = 0
		ch.Keys = nil
		ch.Stats = nil
		remapProxyConfigID(&ch.ProxyMode, &ch.ProxyConfigID, proxyConfigIDMap)

		var existing model.Channel
		if err := tx.Where("name = ?", ch.Name).First(&existing).Error; err == nil {
			// Reuse a name only when its protocol matches the imported channel.
			if existing.Type == ch.Type {
				channelIDMap[oldID] = existing.ID
				continue
			}
			oldName := ch.Name
			ch.Name = uniqueChannelName(ch.Name, tx)
			log.Warnw("channel name conflict with different protocol during import",
				"old_id", oldID,
				"existing_id", existing.ID,
				"old_name", oldName,
				"new_name", ch.Name,
				"existing_type", existing.Type,
				"import_type", ch.Type,
			)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import channels: %w", err)
		}
		if err := tx.Omit("Keys", "Stats").Create(&ch).Error; err != nil {
			return fmt.Errorf("import channels: %w", err)
		}
		channelIDMap[oldID] = ch.ID
		res.RowsAffected["channels"]++
	}
	return nil
}

func (s *dbImportState) importChannelKeys() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	resolveImportedChannel := s.resolveChannel
	// 3. ChannelKeys (remap channel_id, dedup by channel_id+channel_key)
	for i := range dump.ChannelKeys {
		key := dump.ChannelKeys[i]
		resolved, err := resolveImportedChannel(key.ChannelID)
		if err != nil {
			return fmt.Errorf("import channel_keys: resolve channel: %w", err)
		}
		if !resolved.Supported {
			log.Warnw("skip missing or unsupported channel key during import", "channel_id", key.ChannelID)
			continue
		}
		key.ID = 0
		key.ChannelID = resolved.ID
		var existing model.ChannelKey
		if err := tx.Where("channel_id = ? AND channel_key = ?", key.ChannelID, key.ChannelKey).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import channel_keys: %w", err)
		}
		if err := tx.Create(&key).Error; err != nil {
			return fmt.Errorf("import channel_keys: %w", err)
		}
		res.RowsAffected["channel_keys"]++
	}
	return nil
}

type resolvedImportChannel struct {
	ID        int
	Exists    bool
	Supported bool
}

func (s *dbImportState) resolveChannel(sourceID int) (resolvedImportChannel, error) {
	tx := s.tx
	channelIDMap := s.channelIDs
	resolvedChannels := s.resolvedChannels
	if sourceID <= 0 {
		return resolvedImportChannel{}, nil
	}
	if cached, ok := resolvedChannels[sourceID]; ok {
		return cached, nil
	}

	targetID := sourceID
	if remappedID, ok := channelIDMap[sourceID]; ok {
		targetID = remappedID
	}
	var channel model.Channel
	if err := tx.Select("id", "type").Where("id = ?", targetID).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			resolvedChannels[sourceID] = resolvedImportChannel{}
			return resolvedImportChannel{}, nil
		}
		return resolvedImportChannel{}, err
	}
	resolved := resolvedImportChannel{
		ID:        channel.ID,
		Exists:    true,
		Supported: supportedChannelType(channel.Type),
	}
	resolvedChannels[sourceID] = resolved
	return resolved, nil
}

func uniqueProxyConfigName(baseName string, tx *gorm.DB) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "imported-proxy"
	}
	candidate := baseName
	index := 2
	for {
		var count int64
		if err := tx.Model(&model.ProxyConfiguration{}).Where("name = ?", candidate).Count(&count).Error; err != nil {
			return candidate
		}
		if count == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)", baseName, index)
		index++
	}
}

func uniqueChannelName(baseName string, tx *gorm.DB) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "imported-channel"
	}
	candidate := baseName
	for index := 2; ; index++ {
		var count int64
		if err := tx.Model(&model.Channel{}).Where("name = ?", candidate).Count(&count).Error; err != nil {
			return candidate
		}
		if count == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)", baseName, index)
	}
}

func remapProxyConfigID(mode *model.ProxyUsageMode, id **int, idMap map[int]int) {
	if mode == nil || id == nil || *mode != model.ProxyUsageModePool {
		if id != nil {
			*id = nil
		}
		return
	}
	if *id == nil {
		log.Warnw("remapProxyConfigID downgraded proxy mode",
			"original_mode", *mode,
			"proxy_config_id", nil,
			"reason", "nil",
		)
		*mode = model.ProxyUsageModeDirect
		*id = nil
		return
	}
	if newID, ok := idMap[**id]; ok {
		*id = &newID
		return
	}
	if **id <= 0 {
		log.Warnw("remapProxyConfigID downgraded proxy mode",
			"original_mode", *mode,
			"proxy_config_id", **id,
			"reason", "invalid",
		)
		*mode = model.ProxyUsageModeDirect
		*id = nil
		return
	}
	log.Warnw("remapProxyConfigID downgraded proxy mode",
		"original_mode", *mode,
		"proxy_config_id", **id,
		"reason", "not found in idMap",
	)
	*mode = model.ProxyUsageModeDirect
	*id = nil
}
