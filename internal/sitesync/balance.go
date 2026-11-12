package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const (
	siteBalanceQuotaPerUSD = 500000.0
	logFallbackPageSize    = 100
	logFallbackMaxPages    = 6
)

var (
	logIncomeTypes           = []int{1, 4}
	logIncomeContentNumberRE = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
)

type siteBalanceFetchResult struct {
	balance     float64
	balanceUsed float64
	todayIncome float64
	usedKnown   bool
	incomeKnown bool
	ok          bool
	reason      string
	err         error
}

// refreshAccountBalanceAfterCheckin performs a bounded, best-effort refresh
// after the check-in transaction has committed. A failed upstream probe leaves
// the previous projection untouched, including a legitimate zero balance.
func refreshAccountBalanceAfterCheckin(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) siteBalanceFetchResult {
	if siteRecord == nil || account == nil {
		return siteBalanceFetchResult{}
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := fetchSiteAccountBalanceResult(refreshCtx, siteRecord, account, accessToken, firstManagedPlatformUserID(account), true)
	if !result.ok {
		return result
	}
	updates := map[string]any{"balance": result.balance}
	if result.usedKnown {
		updates["balance_used"] = result.balanceUsed
	}
	if result.incomeKnown {
		updates["today_income"] = result.todayIncome
	}
	saved := db.GetDB().WithContext(refreshCtx).Model(&model.SiteAccount{}).Where("id = ? AND site_id = ?", account.ID, siteRecord.ID).Updates(updates)
	if saved.Error != nil {
		log.Warnf("could not save balance after checkin for account %d: %v", account.ID, saved.Error)
		return siteBalanceFetchResult{}
	}
	if saved.RowsAffected == 0 {
		return siteBalanceFetchResult{}
	}
	account.Balance = result.balance
	if result.usedKnown {
		account.BalanceUsed = result.balanceUsed
	}
	if result.incomeKnown {
		account.TodayIncome = result.todayIncome
	}
	return result
}

func fetchSiteAccountBalance(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (float64, float64, float64) {
	result := fetchSiteAccountBalanceResult(ctx, siteRecord, account, accessToken, userID, true)
	return result.balance, result.balanceUsed, result.todayIncome
}

func fetchSiteAccountBalanceResult(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int, includeIncomeLogs bool) (result siteBalanceFetchResult) {
	defer func() {
		if !result.ok && result.reason != "" {
			logSiteDataWarning(siteRecord, account, accessToken, "balance", result.reason, result.err)
		}
	}()
	if siteRecord == nil || account == nil {
		return siteBalanceFetchResult{}
	}
	switch siteRecord.Platform {
	case model.SitePlatformOneAPI,
		model.SitePlatformOneHub:
		return fetchManagementQuotaBalanceResult(ctx, siteRecord, account, accessToken, userID, false, includeIncomeLogs)
	case model.SitePlatformNewAPI,
		model.SitePlatformAnyRouter,
		model.SitePlatformAgentRouter,
		model.SitePlatformDoneHub:
		return fetchManagementQuotaBalanceResult(ctx, siteRecord, account, accessToken, userID, true, includeIncomeLogs)
	case model.SitePlatformSub2API:
		return fetchSub2APIBalanceDetails(ctx, siteRecord, account, accessToken)
	default:
		return siteBalanceFetchResult{}
	}
}

func fetchManagementQuotaBalanceResult(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int, quotaIsRemaining, includeIncomeLogs bool) siteBalanceFetchResult {
	if strings.TrimSpace(accessToken) == "" {
		return siteBalanceFetchResult{reason: "access_token_missing"}
	}
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/self")
	payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil,
		anyRouterAuthHeaders(accessToken, userID), account)
	if stopBalanceProbe(siteRecord, err) {
		return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: err}
	}
	// Most deployments accept the first profile request. Discover a missing user
	// ID only when necessary, especially for the extra pre-check-in snapshot.
	if userID <= 0 && !isValidUserSelfPayload(payload, err) {
		discovered, discoverErr := anyRouterDiscoverUserID(ctx, siteRecord, account, accessToken)
		if stopBalanceProbe(siteRecord, discoverErr) {
			return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: discoverErr}
		}
		if discovered > 0 {
			userID = discovered
			rememberManagedPlatformUserID(userID, account)
			payload, _, err = anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil,
				anyRouterAuthHeaders(accessToken, userID), account)
			if stopBalanceProbe(siteRecord, err) {
				return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: err}
			}
		}
	}

	// Attempt 2: cookie-based fallback with the same userID. AnyRouter often stores the access_token
	// as a raw session cookie value, so `Authorization: Bearer <cookie>` fails and only cookie auth works.
	// This runs even when knownUserID=true because the userID stays stable and cookie auth is how
	// shielded deployments expect requests to arrive.
	if !isValidUserSelfPayload(payload, err) {
		cookiePayload, _, cookieErr := anyRouterFetchUserSelfByCookie(ctx, siteRecord, account, accessToken, userID)
		if stopBalanceProbe(siteRecord, cookieErr) {
			return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: cookieErr}
		}
		if isValidUserSelfPayload(cookiePayload, cookieErr) {
			payload = cookiePayload
			err = nil
		} else if userID > 0 {
			// Attempt 3: probe for an alternate userID (e.g., the real gob-encoded user inside the
			// session cookie) when the passed-in userID doesn't match reality. Safe for multi-account:
			// anyRouterProbeAlternateUserIDByCookie returns 0 when the probed ID matches the current
			// one, and it only returns IDs that genuinely validate against the session.
			alt, probeErr := anyRouterProbeAlternateUserIDByCookie(ctx, siteRecord, account, accessToken, userID)
			if stopBalanceProbe(siteRecord, probeErr) {
				return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: probeErr}
			}
			if alt > 0 {
				altPayload, _, altErr := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil,
					anyRouterAuthHeaders(accessToken, alt), account)
				if stopBalanceProbe(siteRecord, altErr) {
					return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: altErr}
				}
				if isValidUserSelfPayload(altPayload, altErr) {
					payload = altPayload
					err = nil
					rememberManagedPlatformUserID(alt, account)
				} else {
					altCookiePayload, _, altCookieErr := anyRouterFetchUserSelfByCookie(ctx, siteRecord, account, accessToken, alt)
					if stopBalanceProbe(siteRecord, altCookieErr) {
						return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: altCookieErr}
					}
					if isValidUserSelfPayload(altCookiePayload, altCookieErr) {
						payload = altCookiePayload
						err = nil
						rememberManagedPlatformUserID(alt, account)
					}
				}
			}
		}
	}

	if !isValidUserSelfPayload(payload, err) {
		return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: err}
	}
	if userID <= 0 {
		if discovered := anyRouterExtractUserID(payload); discovered > 0 {
			userID = discovered
			rememberManagedPlatformUserID(userID, account)
		}
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		data = payload
	}
	quota, quotaKnown := balanceJSONNumber(data["quota"])
	used, usedKnown := balanceJSONNumber(data["used_quota"])
	if !quotaKnown || (!quotaIsRemaining && !usedKnown) {
		return siteBalanceFetchResult{reason: "invalid_balance_fields"}
	}

	var balance, balanceUsed float64
	quotaPerUnit := siteBalanceQuotaPerUSD
	if siteRecord.Platform == model.SitePlatformAgentRouter {
		quotaPerUnit = agentRouterQuotaPerUnit(ctx, siteRecord, account)
	}
	if quotaIsRemaining {
		balance = quota / quotaPerUnit
		balanceUsed = used / quotaPerUnit
	} else {
		remaining := quota - used
		if remaining < 0 {
			remaining = 0
		}
		balance = remaining / quotaPerUnit
		balanceUsed = used / quotaPerUnit
	}

	todayIncome, todayIncomeKnown := balanceJSONNumber(data["today_income"])
	if todayIncomeKnown {
		todayIncome /= quotaPerUnit
	}

	if includeIncomeLogs && !todayIncomeKnown && supportsTodayIncomeLogFallback(siteRecord.Platform) {
		if fallback, ok := fetchTodayIncomeFromLogsWithQuota(ctx, siteRecord, account, accessToken, userID, quotaPerUnit); ok {
			todayIncome = fallback
			todayIncomeKnown = true
		}
	}

	if math.IsInf(balance, 0) || math.IsNaN(balance) {
		return siteBalanceFetchResult{reason: "invalid_balance_number"}
	}
	return siteBalanceFetchResult{balance: balance, balanceUsed: balanceUsed, todayIncome: todayIncome, usedKnown: usedKnown, incomeKnown: todayIncomeKnown, ok: true}
}

func stopBalanceProbe(site *model.Site, err error) bool {
	return IsCloudflareProtectionError(err) || (usesRouterSession(site.Platform) && anyRouterRequestMustStop(err))
}

func supportsTodayIncomeLogFallback(platform model.SitePlatform) bool {
	switch platform {
	case model.SitePlatformNewAPI,
		model.SitePlatformAnyRouter,
		model.SitePlatformAgentRouter,
		model.SitePlatformOneAPI:
		return true
	default:
		return false
	}
}

func fetchTodayIncomeFromLogs(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (float64, bool) {
	return fetchTodayIncomeFromLogsWithQuota(ctx, siteRecord, account, accessToken, userID, siteBalanceQuotaPerUSD)
}

func fetchTodayIncomeFromLogsWithQuota(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int, quotaPerUnit float64) (float64, bool) {
	if strings.TrimSpace(accessToken) == "" {
		return 0, false
	}

	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endOfDay := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999_999_999, now.Location())
	startTs := startOfDay.Unix()
	endTs := endOfDay.Unix()

	baseURL := buildSiteURL(siteRecord.BaseURL, "/api/log/self")
	headers := anyRouterAuthHeaders(accessToken, userID)

	var total float64
	for _, logType := range logIncomeTypes {
		complete := false
		for page := 1; page <= logFallbackMaxPages; page++ {
			requestURL := fmt.Sprintf("%s?p=%d&page_size=%d&type=%d&token_name=&model_name=&start_timestamp=%d&end_timestamp=%d&group=",
				baseURL, page, logFallbackPageSize, logType, startTs, endTs)

			payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil,
				headers, account)
			if err != nil || payload == nil {
				return 0, false
			}
			if success, exists := payload["success"]; exists && !jsonBool(success) {
				return 0, false
			}

			items := extractLogItems(payload)
			if items == nil {
				return 0, false
			}
			for _, item := range items {
				quotaRaw := jsonFloat(item["quota"])
				if quotaRaw > 0 {
					total += quotaRaw / quotaPerUnit
					continue
				}
				total += parseIncomeFromLogContent(jsonString(item["content"]))
			}

			if len(items) == 0 {
				complete = true
				break
			}
			if totalCount, ok := extractLogTotalCount(payload); ok && page*logFallbackPageSize >= totalCount {
				complete = true
				break
			}
		}
		if !complete {
			return 0, false
		}
	}

	if math.IsNaN(total) || math.IsInf(total*1_000_000, 0) {
		return 0, false
	}
	return math.Round(total*1_000_000) / 1_000_000, true
}

func extractLogItems(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	for _, candidate := range []any{
		nestedValue(payload, "data", "items"),
		payload["items"],
		payload["data"],
	} {
		arr, ok := candidate.([]any)
		if !ok {
			continue
		}
		items := make([]map[string]any, 0, len(arr))
		for _, entry := range arr {
			if m, ok := entry.(map[string]any); ok {
				items = append(items, m)
			}
		}
		return items
	}
	return nil
}

func extractLogTotalCount(payload map[string]any) (int, bool) {
	if payload == nil {
		return 0, false
	}
	for _, candidate := range []any{
		nestedValue(payload, "data", "total"),
		payload["total"],
	} {
		switch v := candidate.(type) {
		case float64:
			if v >= 0 {
				return int(v), true
			}
		case int:
			if v >= 0 {
				return v, true
			}
		case int64:
			if v >= 0 {
				return int(v), true
			}
		case string:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				continue
			}
			parsed, err := strconv.Atoi(trimmed)
			if err == nil && parsed >= 0 {
				return parsed, true
			}
		}
	}
	return 0, false
}

func parseIncomeFromLogContent(content string) float64 {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0
	}
	normalized := strings.ReplaceAll(trimmed, ",", "")
	match := logIncomeContentNumberRE.FindString(normalized)
	if match == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return 0
	}
	if parsed <= 0 {
		return 0
	}
	return parsed
}

func isValidUserSelfPayload(payload map[string]any, err error) bool {
	if err != nil || payload == nil {
		return false
	}
	if _, ok := payload["success"]; ok {
		if !jsonBool(payload["success"]) {
			return false
		}
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return false
	}
	if _, hasQuota := data["quota"]; hasQuota {
		return true
	}
	if _, hasUsed := data["used_quota"]; hasUsed {
		return true
	}
	return false
}

func fetchSub2APIBalanceDetails(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) siteBalanceFetchResult {
	if account.CredentialType == model.SiteCredentialTypeAccessToken {
		var err error
		accessToken, err = ensureFreshSub2APIAccessToken(ctx, siteRecord, account, false)
		if err != nil {
			return siteBalanceFetchResult{reason: "credentials_unavailable", err: err}
		}
	}
	token := stripBearerPrefix(accessToken)
	if token == "" {
		return siteBalanceFetchResult{reason: "access_token_missing"}
	}
	payload, err := requestSub2APIJSON(ctx, siteRecord, http.MethodGet, "/api/v1/auth/me", nil, account, token)
	if err != nil {
		return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: err}
	}
	unwrapped, err := unwrapSub2APIData(payload, "/api/v1/auth/me")
	if err != nil {
		return siteBalanceFetchResult{reason: "upstream_profile_unavailable", err: err}
	}
	data, ok := unwrapped.(map[string]any)
	if !ok {
		return siteBalanceFetchResult{reason: "invalid_profile_response"}
	}
	balance, known := balanceJSONNumber(data["balance"])
	if !known {
		return siteBalanceFetchResult{reason: "invalid_balance_fields"}
	}
	return siteBalanceFetchResult{balance: balance, ok: true}
}

// A missing, null or malformed field is different from a valid zero balance.
func balanceJSONNumber(value any) (float64, bool) {
	var number float64
	var err error
	switch value := value.(type) {
	case float64:
		number = value
	case json.Number:
		number, err = value.Float64()
	case string:
		number, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
	default:
		return 0, false
	}
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}
