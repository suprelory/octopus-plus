package op

import (
	"errors"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *dbImportState) importGroups() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	groupIDMap := s.groupIDs
	// 10. Groups (dedup by name)
	for i := range dump.Groups {
		g := dump.Groups[i]
		oldID := g.ID
		g.ID = 0
		if !g.Mode.Valid() {
			return fmt.Errorf("import groups: invalid group mode: %d", g.Mode)
		}
		g.Items = nil
		// Restore this only after preset IDs have been remapped. Older backups
		// may contain a marker without the corresponding preset rows.
		g.ActivePresetID = nil

		var existing model.Group
		if err := tx.Where("name = ?", g.Name).First(&existing).Error; err == nil {
			groupIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import groups: %w", err)
		}
		if err := tx.Omit("Items").Create(&g).Error; err != nil {
			return fmt.Errorf("import groups: %w", err)
		}
		groupIDMap[oldID] = g.ID
		s.newGroupIDs[g.ID] = true
		res.RowsAffected["groups"]++
	}
	return nil
}

func (s *dbImportState) importGroupItems() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	groupIDMap := s.groupIDs
	resolveImportedChannel := s.resolveChannel
	// 11. GroupItems (remap group_id+channel_id, dedup by uniqueIndex)
	for i := range dump.GroupItems {
		item := dump.GroupItems[i]
		resolved, err := resolveImportedChannel(item.ChannelID)
		if err != nil {
			return fmt.Errorf("import group_items: resolve channel: %w", err)
		}
		if !resolved.Supported {
			continue
		}
		item.ID = 0
		if newID, ok := groupIDMap[item.GroupID]; ok {
			item.GroupID = newID
		}
		item.ChannelID = resolved.ID
		var existing model.GroupItem
		if err := tx.Where("group_id = ? AND channel_id = ? AND model_name = ?", item.GroupID, item.ChannelID, item.ModelName).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import group_items: %w", err)
		}
		if err := tx.Create(&item).Error; err != nil {
			return fmt.Errorf("import group_items: %w", err)
		}
		res.RowsAffected["group_items"]++
	}
	return nil
}

func (s *dbImportState) importModelPrices() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	// 12. LLMInfos (upsert by name - unchanged)
	if n, err := createUpsertAll(tx, dump.LLMInfos, []clause.Column{{Name: "name"}}); err != nil {
		return fmt.Errorf("import llm_infos: %w", err)
	} else {
		res.RowsAffected["llm_infos"] = n
	}
	return nil
}

func (s *dbImportState) importAPIKeys() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	apiKeyIDMap := s.apiKeyIDs
	// 13. APIKeys (dedup by api_key field)
	for i := range dump.APIKeys {
		key := dump.APIKeys[i]
		oldID := key.ID
		key.ID = 0

		var existing model.APIKey
		if err := tx.Where("api_key = ?", key.APIKey).First(&existing).Error; err == nil {
			apiKeyIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import api_keys: %w", err)
		}
		if err := tx.Create(&key).Error; err != nil {
			return fmt.Errorf("import api_keys: %w", err)
		}
		apiKeyIDMap[oldID] = key.ID
		res.RowsAffected["api_keys"]++
	}
	return nil
}

func (s *dbImportState) importSettings() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	// 14. Settings (upsert by key - unchanged)
	if n, err := createUpsertSettings(tx, dump.Settings); err != nil {
		return fmt.Errorf("import settings: %w", err)
	} else {
		res.RowsAffected["settings"] = n
	}
	return nil
}
