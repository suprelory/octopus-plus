package sitesync

import (
	"sort"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

func mergePersistedSiteTokens(accountID int, existingTokens []model.SiteToken, incomingTokens []model.SiteToken, now time.Time) []model.SiteToken {
	preparedExisting := make([]model.SiteToken, 0, len(existingTokens))
	for _, token := range existingTokens {
		token.SiteAccountID = accountID
		token.GroupKey = model.NormalizeSiteGroupKey(token.GroupKey)
		token.GroupName = model.NormalizeSiteGroupName(token.GroupKey, token.GroupName)
		token.Token = strings.TrimSpace(token.Token)
		token.ValueStatus = model.NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token)
		if token.ValueStatus == model.SiteTokenValueStatusMaskedPending {
			token.Enabled = false
			token.IsDefault = false
		}
		preparedExisting = append(preparedExisting, token)
	}

	readyCandidates := make([]model.SiteToken, 0)
	for _, token := range preparedExisting {
		if !model.IsReadySiteToken(token) || model.IsMaskedSiteTokenValue(token.Token) {
			continue
		}
		readyCandidates = append(readyCandidates, token)
	}

	result := make([]model.SiteToken, 0, len(incomingTokens)+len(preparedExisting))
	usedExistingIDs := make(map[int]struct{}, len(preparedExisting))

	for _, incoming := range incomingTokens {
		incoming.SiteAccountID = accountID
		incoming.GroupKey = model.NormalizeSiteGroupKey(incoming.GroupKey)
		incoming.GroupName = model.NormalizeSiteGroupName(incoming.GroupKey, incoming.GroupName)
		incoming.Token = strings.TrimSpace(incoming.Token)
		incoming.LastSyncAt = &now

		var merged model.SiteToken
		if model.IsMaskedSiteTokenValue(incoming.Token) {
			merged = mergeMaskedIncomingSiteToken(incoming, preparedExisting, readyCandidates, usedExistingIDs)
		} else {
			merged = mergeReadyIncomingSiteToken(incoming, preparedExisting, usedExistingIDs)
		}
		merged.SiteAccountID = accountID
		merged.LastSyncAt = &now
		merged.ValueStatus = model.NormalizeSiteTokenValueStatus(merged.ValueStatus, merged.Token)
		if merged.ValueStatus == model.SiteTokenValueStatusMaskedPending {
			merged.Enabled = false
			merged.IsDefault = false
		}
		result = append(result, merged)
	}

	for _, existing := range preparedExisting {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if strings.TrimSpace(existing.Source) != "manual" {
			continue
		}
		existing.LastSyncAt = &now
		result = append(result, existing)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].GroupKey == result[j].GroupKey {
			if result[i].Name == result[j].Name {
				return result[i].ID < result[j].ID
			}
			return result[i].Name < result[j].Name
		}
		return result[i].GroupKey < result[j].GroupKey
	})

	for i := range result {
		result[i].ID = 0
	}

	return result
}

func mergeReadyIncomingSiteToken(incoming model.SiteToken, existingTokens []model.SiteToken, usedExistingIDs map[int]struct{}) model.SiteToken {
	incoming.ValueStatus = model.SiteTokenValueStatusReady
	for _, existing := range existingTokens {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if !sameComparableSiteTokenValue(existing.Token, incoming.Token) {
			continue
		}
		if model.NormalizeSiteGroupKey(existing.GroupKey) != incoming.GroupKey {
			continue
		}
		incoming.ID = existing.ID
		incomingsToken := strings.TrimSpace(incoming.Token)
		existingToken := strings.TrimSpace(existing.Token)
		if existingToken != "" && existingToken != incomingsToken {
			incoming.Token = existingToken
		}
		incoming.Enabled = existing.Enabled
		if existing.ID != 0 {
			usedExistingIDs[existing.ID] = struct{}{}
		}
		return incoming
	}
	for _, existing := range existingTokens {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if strings.TrimSpace(existing.Source) == "manual" {
			continue
		}
		if normalizeSiteTokenName(existing.Name) != normalizeSiteTokenName(incoming.Name) {
			continue
		}
		if model.NormalizeSiteGroupKey(existing.GroupKey) != incoming.GroupKey {
			continue
		}
		incoming.ID = existing.ID
		incoming.Enabled = existing.Enabled
		if existing.ID != 0 {
			usedExistingIDs[existing.ID] = struct{}{}
		}
		return incoming
	}
	return incoming
}

func mergeMaskedIncomingSiteToken(incoming model.SiteToken, existingTokens []model.SiteToken, readyCandidates []model.SiteToken, usedExistingIDs map[int]struct{}) model.SiteToken {
	incoming.ValueStatus = model.SiteTokenValueStatusMaskedPending

	for _, existing := range existingTokens {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if normalizeSiteTokenName(existing.Name) != normalizeSiteTokenName(incoming.Name) {
			continue
		}
		if model.NormalizeSiteGroupKey(existing.GroupKey) != incoming.GroupKey {
			continue
		}
		if model.IsReadySiteToken(existing) && !model.IsMaskedSiteTokenValue(existing.Token) && siteMaskedTokenMatches(existing.Token, incoming.Token) {
			incoming.ID = existing.ID
			incoming.Token = existing.Token
			incoming.ValueStatus = model.SiteTokenValueStatusReady
			incoming.Enabled = existing.Enabled
			usedExistingIDs[existing.ID] = struct{}{}
			return incoming
		}
	}

	matches := make([]model.SiteToken, 0, 2)
	for _, existing := range readyCandidates {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if model.NormalizeSiteGroupKey(existing.GroupKey) != incoming.GroupKey {
			continue
		}
		if normalizeSiteTokenName(incoming.Name) != "" && normalizeSiteTokenName(existing.Name) != normalizeSiteTokenName(incoming.Name) {
			continue
		}
		if !siteMaskedTokenMatches(existing.Token, incoming.Token) {
			continue
		}
		matches = append(matches, existing)
		if len(matches) > 1 {
			break
		}
	}
	if len(matches) == 1 {
		incoming.ID = matches[0].ID
		incoming.Token = matches[0].Token
		incoming.ValueStatus = model.SiteTokenValueStatusReady
		incoming.Enabled = matches[0].Enabled
		usedExistingIDs[matches[0].ID] = struct{}{}
		return incoming
	}

	for _, existing := range existingTokens {
		if existing.ID != 0 {
			if _, used := usedExistingIDs[existing.ID]; used {
				continue
			}
		}
		if normalizeSiteTokenName(existing.Name) != normalizeSiteTokenName(incoming.Name) {
			continue
		}
		if model.NormalizeSiteGroupKey(existing.GroupKey) != incoming.GroupKey {
			continue
		}
		if model.IsReadySiteToken(existing) && !model.IsMaskedSiteTokenValue(existing.Token) {
			log.Warnf("site token demoted to masked_pending due to mask mismatch (account=%d, group=%s, token_id=%d)", existing.SiteAccountID, existing.GroupKey, existing.ID)
			incoming.ID = existing.ID
			incoming.Enabled = false
			incoming.IsDefault = false
			if existing.ID != 0 {
				usedExistingIDs[existing.ID] = struct{}{}
			}
			return incoming
		}
		incoming.ID = existing.ID
		incomingsToken := strings.TrimSpace(incoming.Token)
		existingToken := strings.TrimSpace(existing.Token)
		if existingToken != "" && existingToken != incomingsToken {
			incoming.Token = existingToken
		}
		incoming.Enabled = false
		incoming.IsDefault = false
		if existing.ID != 0 {
			usedExistingIDs[existing.ID] = struct{}{}
		}
		return incoming
	}

	incoming.Enabled = false
	incoming.IsDefault = false
	return incoming
}

func normalizeSiteTokenName(name string) string {
	return strings.TrimSpace(name)
}

func siteMaskedTokenMatches(fullToken string, maskedToken string) bool {
	return model.SiteMaskedTokenMatches(fullToken, maskedToken)
}

func sameComparableSiteTokenValue(left string, right string) bool {
	normalizedLeft := model.NormalizeComparableSiteTokenValue(left)
	normalizedRight := model.NormalizeComparableSiteTokenValue(right)
	if normalizedLeft == "" || normalizedRight == "" {
		return false
	}
	return normalizedLeft == normalizedRight
}
