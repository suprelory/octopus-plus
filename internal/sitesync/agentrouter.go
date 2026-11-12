package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// AgentRouter uses New API management endpoints, but login itself performs the
// daily check-in. Never substitute /checkin or /sign_in for this operation.
func loginAgentRouter(ctx context.Context, site *model.Site, account *model.SiteAccount) (map[string]any, string, error) {
	if account.CredentialType != model.SiteCredentialTypeUsernamePassword || strings.TrimSpace(account.Username) == "" || strings.TrimSpace(account.Password) == "" {
		return nil, "", fmt.Errorf("AgentRouter checkin requires a username/email and password")
	}
	payload, cookie, err := anyRouterRequestJSONWithCookies(ctx, site, http.MethodPost,
		buildSiteURL(site.BaseURL, "/api/user/login"),
		map[string]any{"username": strings.TrimSpace(account.Username), "password": account.Password}, nil, account)
	if err != nil {
		return nil, "", err
	}
	if success, ok := payload["success"].(bool); !ok || !success {
		return nil, "", newSiteLoginFailedError(firstNonEmptyString(extractSiteResponseMessage(payload), "AgentRouter login was not confirmed"))
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, "", wrapSiteDecodeError("AgentRouter login response is missing user data", nil)
	}
	rememberManagedPlatformUserID(anyRouterExtractUserID(payload), account)
	if anyRouterHasUsableSessionCookie(cookie) {
		rememberAnyRouterCookieAuth(ctx, site, account, cookie)
		return data, cookie, nil
	}
	// Some older deployments also return an access token. Missing credentials
	// prevent subsequent reads, but cannot undo an explicitly confirmed check-in.
	return data, firstNonEmptyString(jsonString(data["access_token"]), jsonString(data["token"])), nil
}

func syncAgentRouter(ctx context.Context, site *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	ctx = withAnyRouterSession(ctx, site, account)
	snapshot, err := syncAnyRouter(ctx, site, account)
	if session := anyRouterSessionFromContext(ctx, site, account); session != nil {
		if snapshot != nil {
			snapshot.accessToken = session.resolvedCredential(site, snapshot.accessToken)
		}
		err = session.redactError(err)
	}
	return snapshot, err
}

func checkinAgentRouter(ctx context.Context, site *model.Site, account *model.SiteAccount) (result *model.SiteCheckinResult, token string, runErr error) {
	ctx = withAnyRouterSession(ctx, site, account)
	defer func() {
		if session := anyRouterSessionFromContext(ctx, site, account); session != nil {
			token = session.resolvedCredential(site, token)
			if result != nil {
				result.Message = session.redact(result.Message)
			}
			runErr = session.redactError(runErr)
		}
	}()
	started := time.Now()
	data, token, err := loginAgentRouter(ctx, site, account)
	if err != nil {
		return nil, token, err
	}
	checkedIn, _ := data["checked_in"].(bool)
	logTime := agentRouterCheckinLogTime(ctx, site, account, token, time.Now())
	if !checkedIn && logTime.IsZero() {
		return &model.SiteCheckinResult{
			Status: model.SiteExecutionStatusFailed, Reason: model.SiteCheckinReasonUnconfirmed,
			Message: "AgentRouter 登录成功，但未返回 checked_in=true，且没有可确认的当日签到日志",
		}, token, nil
	}
	message := "AgentRouter 登录响应已确认当日签到"
	if !logTime.IsZero() {
		message = "AgentRouter 当日签到已通过日志确认"
	}
	result = newSuccessfulCheckinResult(message, "")
	// checked_in=true can also mean the allowance was granted on an earlier
	// login. Neither the returned balance nor a cached balance is a reward.
	result.SkipBalanceReward = true
	if !logTime.IsZero() && logTime.Unix() < started.Unix() {
		result.Reason = model.SiteCheckinReasonAlreadyCheckedIn
		result.Message = "AgentRouter 日志确认今日已签到"
	}
	return result, token, nil
}

// Logs are supplemental evidence. Only an actual check-in log in the configured
// calendar day qualifies; an unrelated type=4 credit or yesterday's log does not.
func agentRouterCheckinLogTime(ctx context.Context, site *model.Site, account *model.SiteAccount, token string, now time.Time) time.Time {
	userID := firstManagedPlatformUserID(account)
	if token == "" || userID <= 0 {
		return time.Time{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	local := now.In(resolveSiteCheckinWindow(site).location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	requestURL := fmt.Sprintf("%s?p=1&page_size=20&type=4&start_timestamp=%d&end_timestamp=%d",
		buildSiteURL(site.BaseURL, "/api/log/self/"), start.Unix(), now.Unix())
	payload, _, err := anyRouterRequestJSONWithCookies(ctx, site, http.MethodGet, requestURL, nil, anyRouterAuthHeaders(token, userID), account)
	if err != nil || payload["success"] != true {
		return time.Time{}
	}
	var newest time.Time
	for _, item := range extractLogItems(payload) {
		kind, validKind := balanceJSONNumber(item["type"])
		content := strings.ToLower(jsonString(item["content"]))
		if !validKind || kind != 4 || !(strings.Contains(content, "签到成功") || strings.Contains(content, "daily check-in success") || strings.Contains(content, "daily checkin success")) {
			continue
		}
		stamp, validStamp := balanceJSONNumber(item["created_at"])
		if !validStamp || stamp < float64(start.Unix()) || stamp > float64(now.Unix()) || stamp != float64(int64(stamp)) {
			continue
		}
		at := time.Unix(int64(stamp), 0)
		if at.After(newest) {
			newest = at
		}
	}
	return newest
}

func agentRouterQuotaPerUnit(ctx context.Context, site *model.Site, account *model.SiteAccount) float64 {
	payload, _, err := anyRouterRequestJSONWithCookies(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/status"), nil, nil, account)
	if err == nil && payload["success"] == true {
		if scale, ok := balanceJSONNumber(nestedValue(payload, "data", "quota_per_unit")); ok && scale > 0 {
			return scale
		}
	}
	return siteBalanceQuotaPerUSD
}
