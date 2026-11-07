package op

import (
	"errors"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func (s *dbImportState) importGroupPresets() error {
	presets := make(map[int]model.GroupPreset)
	for _, source := range s.dump.GroupPresets {
		groupID, ok := s.groupIDs[source.GroupID]
		if !ok {
			continue // Never attach an orphan to an unrelated destination group.
		}
		if !source.Mode.Valid() {
			return fmt.Errorf("import group_presets: invalid group mode: %d", source.Mode)
		}
		var existing model.GroupPreset
		if err := s.tx.Where("group_id = ? AND name = ?", groupID, source.Name).First(&existing).Error; err == nil {
			presets[source.ID] = existing
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import group_presets: %w", err)
		}
		preset := source
		preset.ID = 0
		preset.GroupID = groupID
		preset.Items = make([]model.GroupPresetItem, 0, len(source.Items))
		type itemKey struct {
			channelID int
			modelName string
		}
		seen := make(map[itemKey]bool)
		for _, item := range source.Items {
			if _, ok := s.channelIDs[item.ChannelID]; !ok {
				continue
			}
			channel, err := s.resolveChannel(item.ChannelID)
			if err != nil {
				return fmt.Errorf("import group_presets: resolve channel: %w", err)
			}
			if !channel.Supported {
				continue
			}
			item.ChannelID = channel.ID
			key := itemKey{item.ChannelID, item.ModelName}
			if !seen[key] {
				preset.Items = append(preset.Items, item)
				seen[key] = true
			}
		}
		if err := s.tx.Create(&preset).Error; err != nil {
			return fmt.Errorf("import group_presets: %w", err)
		}
		presets[source.ID] = preset
		s.result.RowsAffected["group_presets"]++
	}
	for _, group := range s.dump.Groups {
		groupID := s.groupIDs[group.ID]
		if !s.newGroupIDs[groupID] || group.ActivePresetID == nil {
			continue
		}
		preset, ok := presets[*group.ActivePresetID]
		if !ok || preset.GroupID != groupID {
			continue
		}
		if err := s.tx.Model(&model.Group{}).Where("id = ?", groupID).Update("active_preset_id", preset.ID).Error; err != nil {
			return fmt.Errorf("import group_presets: restore active preset: %w", err)
		}
	}
	return nil
}
