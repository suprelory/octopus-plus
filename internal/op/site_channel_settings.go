package op

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func UpdateSiteProjectedChannelSettings(siteID int, accountID int, req []model.SiteProjectedChannelSettingsUpdateRequest, ctx context.Context) error {
	if len(req) == 0 {
		return nil
	}
	channelIDs := make([]int, 0, len(req))
	seen := make(map[int]struct{}, len(req))
	for _, item := range req {
		if item.ChannelID <= 0 {
			return fmt.Errorf("channel id is required")
		}
		if _, ok := seen[item.ChannelID]; ok {
			return fmt.Errorf("duplicate projected channel: %d", item.ChannelID)
		}
		seen[item.ChannelID] = struct{}{}
		if !isValidAutoGroupType(item.AutoGroup) {
			return fmt.Errorf("invalid auto group type")
		}
		if err := validateParamOverride(item.ParamOverride); err != nil {
			return err
		}
		channelIDs = append(channelIDs, item.ChannelID)
	}

	var bindings []model.SiteChannelBinding
	if err := db.GetDB().WithContext(ctx).
		Where("site_id = ? AND site_account_id = ? AND channel_id IN ?", siteID, accountID, channelIDs).
		Find(&bindings).Error; err != nil {
		return err
	}
	if len(bindings) != len(channelIDs) {
		return fmt.Errorf("projected channel not found")
	}
	valid := make(map[int]struct{}, len(bindings))
	for _, binding := range bindings {
		channel, err := validateChannelReference(binding.ChannelID)
		if err != nil {
			return err
		}
		if !supportedChannelType(channel.Type) {
			return fmt.Errorf("unsupported channel type: %d", channel.Type)
		}
		valid[binding.ChannelID] = struct{}{}
	}

	for _, item := range req {
		if _, ok := valid[item.ChannelID]; !ok {
			return fmt.Errorf("projected channel not found")
		}
		paramOverride := strings.TrimSpace(item.ParamOverride)
		updates := map[string]any{
			"auto_group": item.AutoGroup,
		}
		if paramOverride == "" {
			updates["param_override"] = nil
		} else {
			updates["param_override"] = paramOverride
		}
		if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", item.ChannelID).Updates(updates).Error; err != nil {
			return err
		}
		if err := channelRefreshCacheByID(item.ChannelID, ctx); err != nil {
			return err
		}
		channel, err := ChannelGet(item.ChannelID, ctx)
		if err != nil {
			return err
		}
		if effective := EffectiveProjectedChannelAutoGroup(*channel); effective != model.AutoGroupTypeNone {
			ChannelAutoGroupWithMode(channel, effective, ctx)
		}
	}
	return nil
}

func siteChannelAccount(siteID int, accountID int, ctx context.Context) (*model.SiteAccount, error) {
	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return nil, err
	}
	for i := range site.Accounts {
		if site.Accounts[i].ID == accountID {
			return &site.Accounts[i], nil
		}
	}
	return nil, newSiteChannelAccountNotFoundError()
}

func isValidAutoGroupType(value model.AutoGroupType) bool {
	switch value {
	case model.AutoGroupTypeNone, model.AutoGroupTypeFuzzy, model.AutoGroupTypeExact, model.AutoGroupTypeRegex:
		return true
	default:
		return false
	}
}

func UpdateSiteGroupProjection(siteID int, accountID int, req *model.SiteGroupProjectionUpdateRequest, ctx context.Context) error {
	if req == nil {
		return fmt.Errorf("site group projection update request is nil")
	}
	if _, err := siteChannelAccount(siteID, accountID, ctx); err != nil {
		return err
	}
	groupKey := model.NormalizeSiteGroupKey(req.GroupKey)
	groupName := model.NormalizeSiteGroupName(groupKey, groupKey)
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.SiteUserGroup
		result := tx.Where("site_account_id = ? AND group_key = ?", accountID, groupKey).First(&existing)
		if result.Error == nil {
			return tx.Model(&model.SiteUserGroup{}).
				Where("id = ?", existing.ID).
				Update("projection_disabled", req.ProjectionDisabled).Error
		}
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if !req.ProjectionDisabled {
			return nil
		}
		row := model.SiteUserGroup{
			SiteAccountID:      accountID,
			GroupKey:           groupKey,
			Name:               groupName,
			ProjectionDisabled: req.ProjectionDisabled,
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "site_account_id"}, {Name: "group_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"projection_disabled"}),
		}).Create(&row).Error
	})
}
