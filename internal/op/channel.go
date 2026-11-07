package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/cache"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/xstrings"
	model2 "github.com/suprelory/octopus-plus/polywire/outbound"
	"gorm.io/gorm"
)

var channelCache = cache.New[int, model.Channel](16)
var channelKeyCache = cache.New[int, model.ChannelKey](16)
var channelKeyCacheNeedUpdate = make(map[int]struct{})
var channelKeyCacheNeedUpdateLock sync.Mutex

// Serializes channel/key DB commits, cache publication and runtime settlement.
// Acquire before apiKeyWriteLock and the statistics locks when importing data.
var channelWriteLock sync.Mutex

func setChannelCache(id int, channel model.Channel) {
	channelCache.Set(id, channel)
	invalidateGroupResolutionCache()
}

func setChannelRuntimeCache(id int, channel model.Channel) {
	channelCache.Set(id, channel)
}

func ChannelList(ctx context.Context) ([]model.Channel, error) {
	channels := make([]model.Channel, 0, channelCache.Len())
	for _, channel := range channelCache.GetAll() {
		normalizeChannelProxyFields(&channel)
		channels = append(channels, channel)
	}
	return channels, nil
}

func normalizeChannelProxyFields(channel *model.Channel) {
	if channel == nil {
		return
	}
	if channel.ProxyMode == "" {
		channel.ProxyMode = model.ProxyUsageModeDirect
	}
	if channel.ProxyMode != model.ProxyUsageModePool {
		channel.ProxyConfigID = nil
	}
}

func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	if channel == nil {
		return fmt.Errorf("channel is nil")
	}
	if _, ok := model2.Descriptor(channel.Type); !ok {
		return fmt.Errorf("unsupported channel type: %d", channel.Type)
	}
	if channel.ProxyMode == "" {
		channel.ProxyMode = model.ProxyUsageModeDirect
	}
	channel.PassthroughMode = channel.PassthroughMode.Normalize()
	if err := channel.ProxyMode.Validate(false); err != nil {
		return err
	}
	if channel.ProxyMode == model.ProxyUsageModePool {
		if channel.ProxyConfigID == nil || *channel.ProxyConfigID <= 0 {
			return fmt.Errorf("proxy config id is required when proxy mode is pool")
		}
	} else {
		channel.ProxyConfigID = nil
	}
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if channel.ProxyMode == model.ProxyUsageModePool {
			if _, err := proxyURLForConfigTx(tx, *channel.ProxyConfigID); err != nil {
				return err
			}
		}
		return tx.Create(channel).Error
	}); err != nil {
		return err
	}
	normalizeChannelProxyFields(channel)
	setChannelCache(channel.ID, *channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}

// ChannelKeyUpdate replaces runtime fields of an existing key; configuration
// changes belong to ChannelUpdate. Relay settlement must use the delta variant.
func ChannelKeyUpdate(key model.ChannelKey) error {
	return channelKeyUpdate(key, 0, false)
}

// ChannelKeyUpdateWithDelta 原子合并 relay 运行时状态和成本增量。
func ChannelKeyUpdateWithDelta(key model.ChannelKey, costDelta float64) error {
	return channelKeyUpdate(key, costDelta, true)
}

func channelKeyUpdate(key model.ChannelKey, costDelta float64, mergeCost bool) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	if key.ID == 0 || key.ChannelID == 0 {
		return fmt.Errorf("invalid channel key")
	}
	if _, ok := channelCache.Get(key.ChannelID); !ok {
		return fmt.Errorf("channel not found")
	}
	currentKey, exists := channelKeyCache.Get(key.ID)
	if !exists || currentKey.ChannelID != key.ChannelID {
		return fmt.Errorf("channel key not found")
	}
	updatedKey := channelKeyCache.Update(key.ID, func(current model.ChannelKey, exists bool) model.ChannelKey {
		if !mergeCost {
			current.TotalCost = key.TotalCost
			current.StatusCode = key.StatusCode
			current.LastUseTimeStamp = key.LastUseTimeStamp
			return current
		}

		current.TotalCost += costDelta
		if key.LastUseTimeStamp >= current.LastUseTimeStamp {
			current.LastUseTimeStamp = key.LastUseTimeStamp
			current.StatusCode = key.StatusCode
		}
		return current
	})

	channelCache.Update(key.ChannelID, func(current model.Channel, exists bool) model.Channel {
		if !exists {
			return current
		}
		latestKey, ok := channelKeyCache.Get(key.ID)
		if !ok {
			latestKey = updatedKey
		}
		keys := make([]model.ChannelKey, len(current.Keys))
		copy(keys, current.Keys)
		for i := range keys {
			if keys[i].ID == key.ID {
				keys[i] = latestKey
				break
			}
		}
		current.Keys = keys
		return current
	})
	channelKeyCacheNeedUpdateLock.Lock()
	channelKeyCacheNeedUpdate[key.ID] = struct{}{}
	channelKeyCacheNeedUpdateLock.Unlock()
	return nil
}
func ChannelBaseUrlUpdate(channelID int, baseUrl []model.BaseUrl) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	ch, ok := channelCache.Get(channelID)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	// Copy to decouple callers from internal cache storage.
	if baseUrl == nil {
		ch.BaseUrls = nil
	} else {
		cp := make([]model.BaseUrl, len(baseUrl))
		copy(cp, baseUrl)
		ch.BaseUrls = cp
	}
	setChannelRuntimeCache(channelID, ch)
	return nil
}

// ChannelKeySaveDB 将运行时更新过的 ChannelKey 缓存写入数据库。
func ChannelKeySaveDB(ctx context.Context) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	channelKeyCacheNeedUpdateLock.Lock()
	keyIDs := make([]int, 0, len(channelKeyCacheNeedUpdate))
	for id := range channelKeyCacheNeedUpdate {
		keyIDs = append(keyIDs, id)
	}
	channelKeyCacheNeedUpdate = make(map[int]struct{})
	channelKeyCacheNeedUpdateLock.Unlock()

	if len(keyIDs) == 0 {
		return nil
	}

	rows := make([]model.ChannelKey, 0, len(keyIDs))
	for _, id := range keyIDs {
		k, ok := channelKeyCache.Get(id)
		if ok {
			rows = append(rows, k)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	// Runtime snapshots must never insert credentials or overwrite configuration.
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range rows {
			if err := tx.Model(&model.ChannelKey{}).
				Where("id = ? AND channel_id = ?", key.ID, key.ChannelID).
				Updates(map[string]any{
					"total_cost":          key.TotalCost,
					"status_code":         key.StatusCode,
					"last_use_time_stamp": key.LastUseTimeStamp,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		channelKeyCacheNeedUpdateLock.Lock()
		for _, id := range keyIDs {
			channelKeyCacheNeedUpdate[id] = struct{}{}
		}
		channelKeyCacheNeedUpdateLock.Unlock()
		return err
	}
	return nil
}

func ChannelUpdate(req *model.ChannelUpdateRequest, ctx context.Context) (updatedChannel *model.Channel, operationErr error) {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	if req == nil {
		return nil, fmt.Errorf("channel update request is nil")
	}
	existingChannel, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("channel not found")
	}
	normalizeChannelProxyFields(&existingChannel)
	if !req.BypassManagedCheck {
		if _, managed, err := ChannelManagedBinding(req.ID, ctx); err != nil {
			return nil, err
		} else if managed {
			return nil, fmt.Errorf("managed site channel is read-only; please edit it from the site account")
		}
	}

	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin channel update: %w", tx.Error)
	}
	defer rollbackOnPanic(tx, "channel.update", req.ID, &operationErr)

	var selectFields []string
	updates := model.Channel{ID: req.ID}

	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Type != nil {
		if _, ok := model2.Descriptor(*req.Type); !ok {
			tx.Rollback()
			return nil, fmt.Errorf("unsupported channel type: %d", *req.Type)
		}
		selectFields = append(selectFields, "type")
		updates.Type = *req.Type
	}
	if req.Enabled != nil {
		if *req.Enabled {
			candidateType := existingChannel.Type
			if req.Type != nil {
				candidateType = *req.Type
			}
			if _, ok := model2.Descriptor(candidateType); !ok {
				tx.Rollback()
				return nil, fmt.Errorf("unsupported channel type: %d cannot be enabled", candidateType)
			}
		}
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.BaseUrls != nil {
		selectFields = append(selectFields, "base_urls")
		updates.BaseUrls = *req.BaseUrls
	}
	if req.Model != nil {
		selectFields = append(selectFields, "model")
		updates.Model = *req.Model
	}
	if req.CustomModel != nil {
		selectFields = append(selectFields, "custom_model")
		updates.CustomModel = *req.CustomModel
	}
	effectiveProxyMode := existingChannel.ProxyMode
	effectiveProxyConfigID := existingChannel.ProxyConfigID
	proxyTouched := false
	if req.ProxyMode != nil {
		proxyTouched = true
		effectiveProxyMode = *req.ProxyMode
		selectFields = append(selectFields, "proxy_mode")
		updates.ProxyMode = *req.ProxyMode
	}
	if req.ProxyConfigID != nil || req.ProxyMode != nil {
		proxyTouched = true
		if effectiveProxyMode == model.ProxyUsageModePool {
			if req.ProxyConfigID != nil {
				selectFields = append(selectFields, "proxy_config_id")
				effectiveProxyConfigID = req.ProxyConfigID
				updates.ProxyConfigID = req.ProxyConfigID
			}
		} else {
			selectFields = append(selectFields, "proxy_config_id")
			effectiveProxyConfigID = nil
			updates.ProxyConfigID = nil
		}
	}
	if proxyTouched {
		if effectiveProxyMode == "" {
			effectiveProxyMode = model.ProxyUsageModeDirect
		}
		if err := effectiveProxyMode.Validate(false); err != nil {
			tx.Rollback()
			return nil, err
		}
		if effectiveProxyMode == model.ProxyUsageModePool {
			if effectiveProxyConfigID == nil || *effectiveProxyConfigID <= 0 {
				tx.Rollback()
				return nil, fmt.Errorf("proxy config id is required when proxy mode is pool")
			}
			if _, err := proxyURLForConfigTx(tx, *effectiveProxyConfigID); err != nil {
				tx.Rollback()
				return nil, err
			}
		}
	}
	if req.AutoSync != nil {
		selectFields = append(selectFields, "auto_sync")
		updates.AutoSync = *req.AutoSync
	}
	if req.AutoGroup != nil {
		selectFields = append(selectFields, "auto_group")
		updates.AutoGroup = *req.AutoGroup
	}
	if req.CustomHeader != nil {
		selectFields = append(selectFields, "custom_header")
		updates.CustomHeader = *req.CustomHeader
	}
	if req.WSMode != nil {
		selectFields = append(selectFields, "ws_mode")
		updates.WSMode = req.WSMode.Normalize()
	}
	if req.PassthroughMode != nil {
		selectFields = append(selectFields, "passthrough_mode")
		updates.PassthroughMode = req.PassthroughMode.Normalize()
	}
	if req.ParamOverride != nil {
		selectFields = append(selectFields, "param_override")
		updates.ParamOverride = req.ParamOverride
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = req.MatchRegex
	}

	// 只有当有字段需要更新时才执行 UPDATE
	if len(selectFields) > 0 {
		if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update channel: %w", err)
		}
	}

	// 删除 keys
	if len(req.KeysToDelete) > 0 {
		if err := tx.Where("id IN ? AND channel_id = ?", req.KeysToDelete, req.ID).Delete(&model.ChannelKey{}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to delete channel keys: %w", err)
		}
	}

	// 更新 keys（逐条，只更新提供的字段）
	if len(req.KeysToUpdate) > 0 {
		for _, ku := range req.KeysToUpdate {
			updates := map[string]interface{}{}
			if ku.Enabled != nil {
				updates["enabled"] = *ku.Enabled
			}
			if ku.ChannelKey != nil {
				updates["channel_key"] = *ku.ChannelKey
			}
			if ku.Remark != nil {
				updates["remark"] = *ku.Remark
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&model.ChannelKey{}).
				Where("id = ? AND channel_id = ?", ku.ID, req.ID).
				Updates(updates).Error; err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("failed to update channel key %d: %w", ku.ID, err)
			}
		}
	}

	// 新增 keys
	if len(req.KeysToAdd) > 0 {
		newKeys := make([]model.ChannelKey, 0, len(req.KeysToAdd))
		for _, ka := range req.KeysToAdd {
			newKeys = append(newKeys, model.ChannelKey{
				ChannelID:  req.ID,
				Enabled:    ka.Enabled,
				ChannelKey: ka.ChannelKey,
				Remark:     ka.Remark,
			})
		}
		if err := tx.Create(&newKeys).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create channel keys: %w", err)
		}
	}

	// Load the complete replacement before committing. A failed/cancelled read
	// must roll back rather than leave deleted keys available in the cache.
	var channel model.Channel
	if err := tx.Preload("Keys").First(&channel, req.ID).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("load updated channel: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	channel = publishChannelLocked(channel)
	resetBalancerStateForChannel(req.ID)
	return &channel, nil
}

func ChannelEnabled(id int, enabled bool, ctx context.Context) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	oldChannel, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	if _, managed, err := ChannelManagedBinding(id, ctx); err != nil {
		return err
	} else if managed {
		return fmt.Errorf("managed site channel is read-only; please enable or disable it from the site account")
	}
	if enabled {
		if _, supported := model2.Descriptor(oldChannel.Type); !supported {
			return fmt.Errorf("unsupported channel type: %d cannot be enabled", oldChannel.Type)
		}
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	oldChannel.Enabled = enabled
	normalizeChannelProxyFields(&oldChannel)
	setChannelCache(id, oldChannel)
	resetBalancerStateForChannel(id)
	return nil
}

func ChannelEnabledManaged(id int, enabled bool, ctx context.Context) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	oldChannel, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	if enabled {
		if _, supported := model2.Descriptor(oldChannel.Type); !supported {
			return fmt.Errorf("unsupported channel type: %d cannot be enabled", oldChannel.Type)
		}
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	oldChannel.Enabled = enabled
	normalizeChannelProxyFields(&oldChannel)
	setChannelCache(id, oldChannel)
	resetBalancerStateForChannel(id)
	return nil
}

func ChannelDel(id int, ctx context.Context) error {
	return channelDel(id, ctx, false)
}

func ChannelDelManaged(id int, ctx context.Context) error {
	if _, managed, err := ChannelManagedBinding(id, ctx); err != nil {
		return err
	} else if !managed {
		return fmt.Errorf("channel is not a managed site channel")
	}
	return channelDel(id, ctx, true)
}

func channelDel(id int, ctx context.Context, bypassManagedCheck bool) (operationErr error) {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	ch, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	if !bypassManagedCheck {
		if _, managed, err := ChannelManagedBinding(id, ctx); err != nil {
			return err
		} else if managed {
			return fmt.Errorf("managed site channel cannot be deleted directly; delete the site account or site binding instead")
		}
	}

	// 开启事务
	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("begin channel delete: %w", tx.Error)
	}
	defer rollbackOnPanic(tx, "channel.delete", id, &operationErr)

	// 获取所有受影响的 GroupID，用于刷新缓存
	var affectedGroupIDs []int
	if err := tx.Model(&model.GroupItem{}).
		Where("channel_id = ?", id).
		Pluck("group_id", &affectedGroupIDs).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to get affected groups: %w", err)
	}

	// 删除所有引用该渠道的 GroupItem
	if err := tx.Where("channel_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	// 删除渠道 keys
	if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelKey{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel keys: %w", err)
	}

	// 删除统计数据
	if err := tx.Where("channel_id = ?", id).Delete(&model.StatsChannel{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel stats: %w", err)
	}

	// 删除渠道
	if err := tx.Delete(&model.Channel{}, id).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 删除缓存
	channelCache.Del(id)
	for _, k := range ch.Keys {
		if k.ID != 0 {
			channelKeyCache.Del(k.ID)
			channelKeyCacheNeedUpdateLock.Lock()
			delete(channelKeyCacheNeedUpdate, k.ID)
			channelKeyCacheNeedUpdateLock.Unlock()
		}
	}
	StatsChannelDel(id)
	resetBalancerStateForChannel(id)

	// 刷新受影响的分组缓存
	for _, groupID := range affectedGroupIDs {
		if err := groupRefreshCacheByID(groupID, ctx); err != nil {
			log.Warnf("failed to refresh group cache for group %d: %v", groupID, err)
		}
	}

	return nil
}

func ChannelLLMList(ctx context.Context) ([]model.LLMChannel, error) {
	channelsByID := channelCache.GetAll()
	channelIDs := make([]int, 0, len(channelsByID))
	for channelID := range channelsByID {
		channelIDs = append(channelIDs, channelID)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(channelIDs, ctx)
	if err != nil {
		return nil, err
	}
	siteCache := make(map[int]*model.Site)
	accountCache := make(map[int]*model.SiteAccount)

	models := []model.LLMChannel{}
	for _, cachedChannel := range channelsByID {
		channel := cachedChannel
		normalizeChannelProxyFields(&channel)
		if !supportedChannelType(channel.Type) {
			// The model picker is a routing input, so retired/unknown protocols
			// must not be offered even when their historical channel row remains.
			continue
		}
		var binding *model.SiteChannelBinding
		if item, ok := bindingMap[channel.ID]; ok {
			copy := item
			binding = &copy
		}
		siteName := ""
		siteAccountName := ""
		siteGroupKey := ""
		siteGroupName := ""
		endpointType := "openai"
		var siteID *int
		var siteAccountID *int
		if binding != nil {
			siteID = &binding.SiteID
			siteAccountID = &binding.SiteAccountID
			siteGroupKey = model.NormalizeSiteGroupKey(binding.GroupKey)
			if site, ok := siteCache[binding.SiteID]; ok {
				siteName = site.Name
			} else if site, getErr := SiteGet(binding.SiteID, ctx); getErr == nil {
				siteCache[binding.SiteID] = site
				siteName = site.Name
			}
			if account, ok := accountCache[binding.SiteAccountID]; ok {
				siteAccountName = account.Name
			} else if account, getErr := SiteAccountGet(binding.SiteAccountID, ctx); getErr == nil {
				accountCache[binding.SiteAccountID] = account
				siteAccountName = account.Name
			}
			siteGroupName = siteGroupKey
			if binding.SiteUserGroupID != nil && *binding.SiteUserGroupID > 0 {
				if account := accountCache[binding.SiteAccountID]; account != nil {
					for _, group := range account.UserGroups {
						if group.ID == *binding.SiteUserGroupID {
							siteGroupName = model.NormalizeSiteGroupName(group.GroupKey, group.Name)
							siteGroupKey = model.NormalizeSiteGroupKey(group.GroupKey)
							break
						}
					}
				}
			}
			if siteGroupName == "" {
				siteGroupName = model.NormalizeSiteGroupName(siteGroupKey, "")
			}
			switch channel.Type {
			case model2.OutboundTypeAnthropic:
				endpointType = "anthropic"
			case model2.OutboundTypeGemini:
				endpointType = "gemini"
			default:
				endpointType = "openai"
			}
		}
		modelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
		for _, modelName := range modelNames {
			if modelName == "" {
				continue
			}
			models = append(models, model.LLMChannel{
				Name:            modelName,
				Enabled:         channel.Enabled,
				ChannelID:       channel.ID,
				ChannelName:     channel.Name,
				SiteID:          siteID,
				SiteAccountID:   siteAccountID,
				SiteGroupKey:    siteGroupKey,
				SiteGroupName:   siteGroupName,
				SiteName:        siteName,
				SiteAccountName: siteAccountName,
				EndpointType:    endpointType,
			})
		}
	}
	return models, nil
}

func ChannelGet(id int, ctx context.Context) (*model.Channel, error) {
	channel, ok := channelCache.Get(id)
	if !ok {
		return nil, fmt.Errorf("channel not found")
	}
	normalizeChannelProxyFields(&channel)
	return &channel, nil
}

func ChannelGetByName(name string, ctx context.Context) (*model.Channel, error) {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, fmt.Errorf("channel name is empty")
	}

	var channel model.Channel
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Where("name = ?", trimmed).
		First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			for id, cached := range channelCache.GetAll() {
				if cached.Name != trimmed {
					continue
				}
				channelCache.Del(id)
				for _, key := range cached.Keys {
					if key.ID != 0 {
						channelKeyCache.Del(key.ID)
					}
				}
			}
		}
		return nil, err
	}

	channel = publishChannelLocked(channel)

	return &channel, nil
}

func channelRefreshCache(ctx context.Context) error {
	return refreshChannels(ctx, true)
}

func refreshChannels(ctx context.Context, preserveRuntime bool) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	channels := []model.Channel{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Find(&channels).Error; err != nil {
		log.Warnf("failed to get channels: %v", err)
		return err
	}
	if !preserveRuntime {
		channelKeyCache.Clear()
		channelKeyCacheNeedUpdateLock.Lock()
		channelKeyCacheNeedUpdate = make(map[int]struct{})
		channelKeyCacheNeedUpdateLock.Unlock()
	}
	publishChannelsLocked(channels)
	return nil
}

func channelRefreshCacheByID(id int, ctx context.Context) error {
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	var channel model.Channel
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		First(&channel, id).Error; err != nil {
		return err
	}
	publishChannelLocked(channel)
	return nil
}

// The caller holds channelWriteLock. DB configuration wins, while unflushed
// runtime state belongs to the surviving key's cache entry.
func publishChannelLocked(channel model.Channel) model.Channel {
	normalizeChannelProxyFields(&channel)
	channel.Stats = nil
	live := make(map[int]struct{}, len(channel.Keys))
	for i := range channel.Keys {
		key := &channel.Keys[i]
		live[key.ID] = struct{}{}
		if current, ok := channelKeyCache.Get(key.ID); ok && current.ChannelID == channel.ID {
			key.TotalCost = current.TotalCost
			key.StatusCode = current.StatusCode
			key.LastUseTimeStamp = current.LastUseTimeStamp
		}
		channelKeyCache.Set(key.ID, *key)
	}
	if old, ok := channelCache.Get(channel.ID); ok {
		for _, key := range old.Keys {
			if _, exists := live[key.ID]; !exists {
				channelKeyCache.Del(key.ID)
				channelKeyCacheNeedUpdateLock.Lock()
				delete(channelKeyCacheNeedUpdate, key.ID)
				channelKeyCacheNeedUpdateLock.Unlock()
			}
		}
	}
	setChannelCache(channel.ID, channel)
	return channel
}

func publishChannelsLocked(channels []model.Channel) {
	liveKeys := make(map[int]struct{})
	liveChannels := make(map[int]struct{}, len(channels))
	for _, channel := range channels {
		publishChannelLocked(channel)
		liveChannels[channel.ID] = struct{}{}
		for _, key := range channel.Keys {
			liveKeys[key.ID] = struct{}{}
		}
	}
	for id := range channelCache.GetAll() {
		if _, exists := liveChannels[id]; !exists {
			channelCache.Del(id)
		}
	}
	for id := range channelKeyCache.GetAll() {
		if _, exists := liveKeys[id]; !exists {
			channelKeyCache.Del(id)
		}
	}
	channelKeyCacheNeedUpdateLock.Lock()
	for id := range channelKeyCacheNeedUpdate {
		if _, exists := liveKeys[id]; !exists {
			delete(channelKeyCacheNeedUpdate, id)
		}
	}
	channelKeyCacheNeedUpdateLock.Unlock()
	invalidateGroupResolutionCache()
}
