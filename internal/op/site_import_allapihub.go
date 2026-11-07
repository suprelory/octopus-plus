package op

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func SiteImportAllAPIHub(ctx context.Context, body []byte) (*model.AllAPIHubImportResult, []int, error) {
	var payload rawImportObject
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, newSiteImportInvalidJSONError()
	}
	if len(payload) == 0 {
		return nil, nil, newSiteImportEmptyPayloadError()
	}

	inputs, warnings, skipped, err := extractAllAPIHubAccounts(payload)
	if err != nil {
		return nil, nil, err
	}
	if len(inputs) == 0 {
		return nil, nil, newSiteImportNoImportableAllAPIHubError()
	}

	result := &model.AllAPIHubImportResult{
		SkippedAccounts: skipped,
		Warnings:        warnings,
	}
	createdSiteIDs := make(map[int]struct{})
	reusedSiteIDs := make(map[int]struct{})
	syncAccountIDs := make(map[int]struct{})

	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, input := range inputs {
			siteRecord, created, err := upsertImportedSite(tx, input.Site)
			if err != nil {
				return err
			}
			if created {
				createdSiteIDs[siteRecord.ID] = struct{}{}
			} else if _, ok := createdSiteIDs[siteRecord.ID]; !ok {
				reusedSiteIDs[siteRecord.ID] = struct{}{}
			}

			accountRecord, createdAccount, updatedAccount, err := upsertImportedAccount(tx, siteRecord, input)
			if err != nil {
				return err
			}
			if createdAccount {
				result.CreatedAccounts++
			}
			if updatedAccount {
				result.UpdatedAccounts++
			}
			if siteRecord.Enabled && accountRecord.Enabled && accountRecord.AutoSync {
				syncAccountIDs[accountRecord.ID] = struct{}{}
			}
		}
		return db.SeparateLegacySiteCheckins(tx)
	}); err != nil {
		return nil, nil, wrapSiteImportPersistFailedError(err)
	}

	result.CreatedSites = len(createdSiteIDs)
	result.ReusedSites = len(reusedSiteIDs)

	accountIDs := make([]int, 0, len(syncAccountIDs))
	for accountID := range syncAccountIDs {
		accountIDs = append(accountIDs, accountID)
	}
	slices.Sort(accountIDs)
	result.ScheduledSyncAccounts = len(accountIDs)

	return result, accountIDs, nil
}

func extractAllAPIHubAccounts(payload rawImportObject) ([]importedAccountInput, []string, int, error) {
	var warnings []string
	inputs := make([]importedAccountInput, 0)
	skipped := 0

	accountsContainer := asObject(payload["accounts"])
	rows := asObjectSlice(accountsContainer["accounts"])
	for _, row := range rows {
		input, warning, ok := parseAllAPIHubAccountRow(row)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if !ok {
			skipped++
			continue
		}
		inputs = append(inputs, input)
	}

	profilesContainer := asObject(payload["apiCredentialProfiles"])
	profiles := asObjectSlice(profilesContainer["profiles"])
	for _, profile := range profiles {
		input, warning, ok := parseAllAPIHubProfile(profile)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if !ok {
			skipped++
			continue
		}
		inputs = append(inputs, input)
	}

	if len(rows) == 0 && len(profiles) == 0 {
		return nil, warnings, skipped, newSiteImportUnrecognizedAllAPIHubError()
	}

	return inputs, warnings, skipped, nil
}

func parseAllAPIHubAccountRow(row rawImportObject) (importedAccountInput, string, bool) {
	siteURL := normalizeImportBaseURL(asString(row["site_url"]))
	siteName := firstNonEmptyString(asString(row["site_name"]), siteURL)
	rowID := firstNonEmptyString(asString(row["id"]), asString(row["username"]), siteName, "unknown")
	if siteURL == "" {
		return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：site_url 无效", rowID), false
	}

	platform, ok := resolveImportedPlatform(row["site_type"], siteURL)
	if !ok {
		return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：站点平台不受支持", rowID), false
	}

	accountInfo := asObject(row["account_info"])
	cookieAuth := asObject(row["cookieAuth"])
	checkin := asObject(row["checkIn"])
	authType := strings.ToLower(strings.TrimSpace(asString(row["authType"])))
	username := firstNonEmptyString(asString(accountInfo["username"]), asString(row["username"]), rowID)
	accessTokenCandidate := firstNonEmptyString(asString(accountInfo["access_token"]), asString(row["access_token"]))
	refreshTokenCandidate := firstNonEmptyString(asString(accountInfo["refresh_token"]), asString(row["refresh_token"]))
	tokenExpiresAt := asInt64(accountInfo["token_expires_at"])
	cookieSession := asString(cookieAuth["sessionCookie"])
	platformUserID := asIntPointer(accountInfo["id"])

	input := importedAccountInput{
		Site: importedSiteInput{
			Name:     siteName,
			Platform: platform,
			BaseURL:  siteURL,
		},
		Name:           username,
		Enabled:        !asBool(row["disabled"], false),
		AutoSync:       true,
		AutoCheckin:    asBool(checkin["autoCheckInEnabled"], model.PlatformCheckinDefaults(platform).Enabled),
		RefreshToken:   refreshTokenCandidate,
		TokenExpiresAt: tokenExpiresAt,
	}
	if platformUserID != nil {
		input.PlatformUserID = platformUserID
	}

	switch authType {
	case "cookie":
		if cookieSession == "" {
			return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：cookieAuth.sessionCookie 缺失", rowID), false
		}
		input.CredentialType = model.SiteCredentialTypeAccessToken
		input.AccessToken = cookieSession
	case "access_token", "session":
		if accessTokenCandidate == "" {
			return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：access_token 缺失", rowID), false
		}
		if isDirectImportPlatform(platform) {
			input.CredentialType = model.SiteCredentialTypeAPIKey
			input.APIKey = accessTokenCandidate
		} else {
			input.CredentialType = model.SiteCredentialTypeAccessToken
			input.AccessToken = accessTokenCandidate
		}
	case "api_key":
		if accessTokenCandidate == "" {
			return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：api_key 缺失", rowID), false
		}
		input.CredentialType = model.SiteCredentialTypeAPIKey
		input.APIKey = accessTokenCandidate
	default:
		return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub 账号 %s：authType=%s 不支持离线导入", rowID, firstNonEmptyString(authType, "unknown")), false
	}

	return input, "", true
}

func parseAllAPIHubProfile(profile rawImportObject) (importedAccountInput, string, bool) {
	baseURL := normalizeImportBaseURL(asString(profile["baseUrl"]))
	apiKey := asString(profile["apiKey"])
	profileID := firstNonEmptyString(asString(profile["id"]), asString(profile["name"]), baseURL, "unknown")
	if baseURL == "" || apiKey == "" {
		return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub API 凭据 %s：baseUrl 或 apiKey 缺失", profileID), false
	}

	platform, ok := resolveImportedProfilePlatform(profile["apiType"], baseURL)
	if !ok {
		return importedAccountInput{}, fmt.Sprintf("跳过 ALL-API-Hub API 凭据 %s：站点平台不受支持", profileID), false
	}

	return importedAccountInput{
		Site: importedSiteInput{
			Name:     baseURL,
			Platform: platform,
			BaseURL:  baseURL,
		},
		Name:           firstNonEmptyString(asString(profile["name"]), profileID, baseURL),
		CredentialType: model.SiteCredentialTypeAPIKey,
		APIKey:         apiKey,
		Enabled:        true,
		AutoSync:       true,
		AutoCheckin:    false,
	}, "", true
}
