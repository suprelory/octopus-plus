package sitesync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	"gorm.io/gorm"
)

func buildManagedChannelName(siteRecord *model.Site, account *model.SiteAccount, group model.SiteUserGroup, obType outbound.OutboundType) string {
	groupName := model.NormalizeSiteGroupName(group.GroupKey, group.Name)
	formatName := model.CompactSiteModelRouteTypeName(model.SiteModelRouteTypeFromOutboundType(obType))
	return fmt.Sprintf("%s/%s/%s-%s", siteRecord.Name, account.Name, groupName, formatName)
}

func reuseManagedChannelByName(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, group model.SiteUserGroup, bindingKey string, channelPayload model.Channel) (*model.SiteChannelBinding, bool, error) {
	existingChannel, err := op.ChannelGetByName(channelPayload.Name, ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to lookup managed channel by name: %w", err)
	}

	binding, managed, err := op.ChannelManagedBinding(existingChannel.ID, ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to inspect existing managed channel binding: %w", err)
	}
	if managed {
		if binding.SiteID != siteRecord.ID || binding.SiteAccountID != account.ID {
			return nil, false, fmt.Errorf("managed channel name %q is already bound to another site account", channelPayload.Name)
		}
		return binding, true, nil
	}

	reusedBinding := model.SiteChannelBinding{
		SiteID:        siteRecord.ID,
		SiteAccountID: account.ID,
		GroupKey:      bindingKey,
		ChannelID:     existingChannel.ID,
	}
	if group.ID != 0 {
		reusedBinding.SiteUserGroupID = &group.ID
	}
	if err := db.GetDB().WithContext(ctx).Create(&reusedBinding).Error; err != nil {
		return nil, false, fmt.Errorf("failed to bind existing channel %q as managed channel: %w", channelPayload.Name, err)
	}
	return &reusedBinding, true, nil
}

func buildProjectedChannelBaseURL(siteRecord *model.Site) string {
	if siteRecord == nil {
		return ""
	}

	baseURL := strings.TrimRight(strings.TrimSpace(siteRecord.BaseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// resolveProjectedChannelBaseURL returns the base URL for a projected channel
// of the given route type. A per-route override on the site (RouteBaseURLs)
// wins and is used verbatim; otherwise the default site base URL handling
// (with the "/v1" convention) applies. This lets one upstream expose different
// protocols under different path prefixes.
func resolveProjectedChannelBaseURL(siteRecord *model.Site, routeType model.SiteModelRouteType) string {
	if override, ok := siteRecord.ResolveRouteBaseURL(routeType); ok {
		return override
	}
	return buildProjectedChannelBaseURL(siteRecord)
}

// isUsableSiteToken reports whether a token can produce a projected channel
// key: it must be ready, unmasked, and carry a non-empty normalized value.
func isUsableSiteToken(token model.SiteToken) bool {
	if !model.IsReadySiteToken(token) || model.IsMaskedSiteTokenValue(token.Token) {
		return false
	}
	return strings.TrimSpace(token.Token) != ""
}

// hasUsableToken reports whether at least one token would yield a channel key,
// keeping projection activation aligned with buildChannelKeys.
func hasUsableToken(tokens []model.SiteToken) bool {
	for _, token := range tokens {
		if isUsableSiteToken(token) {
			return true
		}
	}
	return false
}

func buildChannelKeys(tokens []model.SiteToken, platform model.SitePlatform) []model.ChannelKey {
	keys := make([]model.ChannelKey, 0, len(tokens))
	for _, token := range tokens {
		if !isUsableSiteToken(token) {
			continue
		}
		normalized := model.NormalizeSiteSyncTokenValueForPlatform(platform, token.Token)
		keys = append(keys, model.ChannelKey{Enabled: token.Enabled, ChannelKey: normalized, Remark: model.NormalizeSiteGroupName(token.GroupKey, token.GroupName)})
	}
	return keys
}

func diffManagedChannelKeys(existingKeys []model.ChannelKey, desiredKeys []model.ChannelKey) ([]model.ChannelKeyAddRequest, []model.ChannelKeyUpdateRequest, []int) {
	used := make(map[int]struct{}, len(existingKeys))
	adds := make([]model.ChannelKeyAddRequest, 0)
	updates := make([]model.ChannelKeyUpdateRequest, 0)

	for _, desired := range desiredKeys {
		matchedIndex := -1
		for i, existing := range existingKeys {
			if existing.ChannelKey != desired.ChannelKey {
				continue
			}
			if _, ok := used[existing.ID]; ok {
				continue
			}
			matchedIndex = i
			break
		}
		if matchedIndex == -1 {
			adds = append(adds, model.ChannelKeyAddRequest{
				Enabled:    desired.Enabled,
				ChannelKey: desired.ChannelKey,
				Remark:     desired.Remark,
			})
			continue
		}

		existing := existingKeys[matchedIndex]
		used[existing.ID] = struct{}{}
		update := model.ChannelKeyUpdateRequest{ID: existing.ID}
		if existing.Enabled != desired.Enabled {
			enabled := desired.Enabled
			update.Enabled = &enabled
		}
		if existing.Remark != desired.Remark {
			remark := desired.Remark
			update.Remark = &remark
		}
		if update.Enabled != nil || update.Remark != nil {
			updates = append(updates, update)
		}
	}

	deletes := make([]int, 0)
	for _, existing := range existingKeys {
		if _, ok := used[existing.ID]; ok {
			continue
		}
		deletes = append(deletes, existing.ID)
	}
	return adds, updates, deletes
}
