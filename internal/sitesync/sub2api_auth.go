package sitesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

const sub2APIAccessTokenRefreshLead = 5 * time.Minute

type sub2APIRefreshedCredentials struct {
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt int64
}

func ensureFreshSub2APIAccessToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, forceRefresh bool) (string, error) {
	if siteRecord == nil || account == nil {
		return "", fmt.Errorf("site or account is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Unsaved accounts may make read-only probes, but cannot rotate a session
	// whose replacement credentials have nowhere durable to go.
	if account.ID <= 0 {
		if forceRefresh || shouldProactivelyRefreshSub2API(account) {
			return "", newSub2APISessionError(sub2APISessionSaveFailed, "请先保存订阅账号，再刷新登录凭据")
		}
		return usableSub2APIAccessToken(account)
	}
	result, err := resolveSub2APISession(ctx, siteRecord, account, forceRefresh)
	if result.account != nil {
		copySub2APICredentials(account, result.account)
	}
	if err != nil {
		if !forceRefresh && isSub2APITransientRefreshError(err) && sub2APIAccessTokenExpiresAt(account) > time.Now().Add(5*time.Second).UnixMilli() {
			return stripBearerPrefix(account.AccessToken), nil
		}
		return "", err
	}
	return usableSub2APIAccessToken(account)
}

func shouldProactivelyRefreshSub2API(account *model.SiteAccount) bool {
	if account == nil || strings.TrimSpace(account.RefreshToken) == "" {
		return false
	}
	expiresAt := sub2APIAccessTokenExpiresAt(account)
	return expiresAt > 0 && time.Until(time.UnixMilli(expiresAt)) <= sub2APIAccessTokenRefreshLead
}

func shouldRetrySub2APIAfterRefresh(err error, account *model.SiteAccount) bool {
	if err == nil || account == nil || account.CredentialType != model.SiteCredentialTypeAccessToken {
		return false
	}
	status, reason := sub2APIUpstreamErrorDetails(err)
	if status != http.StatusUnauthorized {
		return false
	}
	switch reason {
	case "", "UNAUTHORIZED", "TOKEN_EXPIRED", "ACCESS_TOKEN_EXPIRED", "INVALID_TOKEN":
		return true
	default:
		return false
	}
}

func usableSub2APIAccessToken(account *model.SiteAccount) (string, error) {
	token := stripBearerPrefix(account.AccessToken)
	if token == "" {
		return "", newAccessTokenRequiredError()
	}
	if expiresAt := sub2APIAccessTokenExpiresAt(account); expiresAt > 0 && expiresAt <= time.Now().UnixMilli() {
		return "", newSub2APISessionError(sub2APIRefreshMissing, "Access Token 已过期，请补充有效的 Refresh Token 或更新登录凭据")
	}
	return token, nil
}

// JWT claims are only a scheduling hint. Authentication remains the upstream's
// responsibility; parsing an unverified exp never grants access locally.
func sub2APIAccessTokenExpiresAt(account *model.SiteAccount) int64 {
	if account == nil {
		return 0
	}
	expiresAt := account.TokenExpiresAt
	parts := strings.Split(stripBearerPrefix(account.AccessToken), ".")
	if len(parts) != 3 || len(parts[1]) > 16<<10 {
		return expiresAt
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return expiresAt
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return expiresAt
	}
	seconds, err := claims.Exp.Int64()
	if err == nil && seconds > 0 && seconds <= math.MaxInt64/1000 && (expiresAt <= 0 || seconds*1000 < expiresAt) {
		return seconds * 1000
	}
	return expiresAt
}

func copySub2APICredentials(dst, src *model.SiteAccount) {
	dst.AccessToken, dst.RefreshToken = src.AccessToken, src.RefreshToken
	dst.APIKey = src.APIKey
	dst.TokenExpiresAt, dst.CredentialVersion = src.TokenExpiresAt, src.CredentialVersion
	dst.CredentialType = src.CredentialType
	dst.ProxyMode, dst.ProxyConfigID = src.ProxyMode, src.ProxyConfigID
}

func sub2APIUpstreamErrorDetails(err error) (int, string) {
	if apperror.Code(err) != CodeSiteUpstreamHTTPError {
		return 0, ""
	}
	params := apperror.Params(err)
	return int(anyToInt64(params["statusCode"])), strings.ToUpper(jsonString(params["upstreamReason"]))
}

func parseSub2APIRefreshPayload(payload map[string]any) (sub2APIRefreshedCredentials, bool) {
	if payload == nil {
		return sub2APIRefreshedCredentials{}, false
	}

	if rawCode, ok := payload["code"]; ok {
		code := anyToInt64(rawCode)
		if code != 0 {
			return sub2APIRefreshedCredentials{}, false
		}
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		return sub2APIRefreshedCredentials{}, false
	}

	accessToken := stripBearerPrefix(jsonString(data["access_token"]))
	refreshToken := strings.TrimSpace(jsonString(data["refresh_token"]))
	expiresInSeconds := anyToInt64(data["expires_in"])
	if accessToken == "" || refreshToken == "" || expiresInSeconds <= 0 || expiresInSeconds > math.MaxInt64/int64(time.Second) {
		return sub2APIRefreshedCredentials{}, false
	}

	return sub2APIRefreshedCredentials{
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		TokenExpiresAt: time.Now().Add(time.Duration(expiresInSeconds) * time.Second).UnixMilli(),
	}, true
}

func anyToInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		var parsed int64
		if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}
