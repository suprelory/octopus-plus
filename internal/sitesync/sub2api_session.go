package sitesync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	sub2APIRefreshTimeout  = 30 * time.Second
	sub2APISaveTimeout     = 5 * time.Second
	sub2APIRefreshCooldown = 30 * time.Second

	sub2APIRefreshMissing    = "site.sub2api.refresh_missing"
	sub2APISessionInvalid    = "site.sub2api.session_invalid"
	sub2APISessionBinding    = "site.sub2api.session_binding_mismatch"
	sub2APISessionInactive   = "site.sub2api.account_inactive"
	sub2APISessionChanged    = "site.sub2api.credentials_changed"
	sub2APISessionSaveFailed = "site.sub2api.session_save_failed"
	sub2APIRefreshTransient  = "site.sub2api.refresh_unavailable"
	sub2APIRefreshRejected   = "site.sub2api.refresh_rejected"
)

type sub2APISessionResult struct {
	account   *model.SiteAccount
	refreshed bool
}

type sub2APISessionKey struct {
	conn      *gorm.DB
	accountID int
}

type sub2APIPendingSession struct {
	before      model.SiteAccount
	credentials sub2APIRefreshedCredentials
}

type sub2APIRefreshFailure struct {
	version     int64
	fingerprint [32]byte
	retryAt     time.Time // Zero means a new credential version is required.
	err         error
	pending     *sub2APIPendingSession
}

var sub2APISessions = struct {
	flight singleflight.Group
	sync.Mutex
	failures map[sub2APISessionKey]sub2APIRefreshFailure
}{failures: make(map[sub2APISessionKey]sub2APIRefreshFailure)}

func newSub2APISessionError(code, message string) *apperror.Error {
	return apperror.New(code, message).WithStatus(http.StatusBadGateway).WithLogMessage(message)
}

func isSub2APITransientRefreshError(err error) bool {
	return apperror.Code(err) == sub2APIRefreshTransient
}

func sub2APIPermanentAuthError(err error) error {
	_, reason := sub2APIUpstreamErrorDetails(err)
	switch reason {
	case "SESSION_BINDING_MISMATCH":
		return newSub2APISessionError(sub2APISessionBinding, "Sub2API 会话的出口 IP 或 User-Agent 不匹配，请统一网络配置并重新获取登录凭据")
	case "USER_INACTIVE", "USER_NOT_ACTIVE", "USER_NOT_FOUND":
		return newSub2APISessionError(sub2APISessionInactive, "Sub2API 账号不可用，请检查账号状态")
	case "TOKEN_REVOKED", "REFRESH_TOKEN_INVALID", "REFRESH_TOKEN_EXPIRED", "REFRESH_TOKEN_REUSED":
		return newSub2APISessionError(sub2APISessionInvalid, "Sub2API 登录会话或 Refresh Token 已失效，请重新获取独立会话的登录凭据")
	}
	return nil
}

func sub2APICredentialFingerprint(account *model.SiteAccount) [32]byte {
	return sha256.Sum256([]byte(account.AccessToken + "\x00" + account.RefreshToken))
}

func sub2APIFailure(key sub2APISessionKey, account *model.SiteAccount) (sub2APIRefreshFailure, bool) {
	sub2APISessions.Lock()
	defer sub2APISessions.Unlock()
	failure, ok := sub2APISessions.failures[key]
	if ok && (failure.version != account.CredentialVersion || failure.fingerprint != sub2APICredentialFingerprint(account)) {
		delete(sub2APISessions.failures, key)
		return sub2APIRefreshFailure{}, false
	}
	return failure, ok
}

func rememberSub2APIFailure(key sub2APISessionKey, account *model.SiteAccount, err error, pending *sub2APIPendingSession) {
	failure := sub2APIRefreshFailure{version: account.CredentialVersion, fingerprint: sub2APICredentialFingerprint(account), err: err, pending: pending}
	if isSub2APITransientRefreshError(err) {
		failure.retryAt = time.Now().Add(sub2APIRefreshCooldown)
	} else if pending != nil {
		failure.retryAt = time.Now().Add(time.Second)
	}
	sub2APISessions.Lock()
	sub2APISessions.failures[key] = failure
	sub2APISessions.Unlock()
	log.Warnw("sitesync.sub2api.session", "account_id", account.ID, "stage", "refresh", "reason", apperror.Code(err), "message", err.Error())
}

func clearSub2APIFailure(key sub2APISessionKey) {
	sub2APISessions.Lock()
	delete(sub2APISessions.failures, key)
	sub2APISessions.Unlock()
}

func resolveSub2APISession(ctx context.Context, site *model.Site, account *model.SiteAccount, force bool) (sub2APISessionResult, error) {
	conn := db.GetDB()
	if conn == nil {
		return sub2APISessionResult{}, newSub2APISessionError(sub2APISessionSaveFailed, "订阅账号数据库不可用，无法安全刷新登录凭据")
	}
	key := sub2APISessionKey{conn: conn, accountID: account.ID}
	original, siteCopy := *account, *site
	flightKey := fmt.Sprintf("%p:%d", conn, account.ID)
	// A forced recovery can join a proactive read that did not refresh. Retry
	// that coordination once; never exchange the same refresh token twice.
	for attempt := 0; attempt < 2; attempt++ {
		ch := sub2APISessions.flight.DoChan(flightKey, func() (any, error) {
			workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*sub2APIRefreshTimeout+2*sub2APISaveTimeout)
			defer cancel()
			return loadOrRefreshSub2APISession(workCtx, key, &siteCopy, &original, force)
		})
		select {
		case <-ctx.Done():
			return sub2APISessionResult{}, ctx.Err()
		case completed := <-ch:
			result, _ := completed.Val.(sub2APISessionResult)
			if completed.Err != nil || !force || result.refreshed || result.account == nil ||
				result.account.CredentialVersion != original.CredentialVersion ||
				stripBearerPrefix(result.account.AccessToken) != stripBearerPrefix(original.AccessToken) {
				return result, completed.Err
			}
		}
	}
	return sub2APISessionResult{}, newSub2APISessionError(sub2APIRefreshTransient, "Sub2API 会话正在更新，请稍后重试")
}

func loadOrRefreshSub2APISession(ctx context.Context, key sub2APISessionKey, site *model.Site, original *model.SiteAccount, force bool) (sub2APISessionResult, error) {
	for {
		var current model.SiteAccount
		if err := key.conn.WithContext(ctx).First(&current, key.accountID).Error; err != nil {
			return sub2APISessionResult{}, newSub2APISessionError(sub2APISessionChanged, "无法读取订阅账号，请检查账号是否仍然存在")
		}
		result := sub2APISessionResult{account: &current}
		if current.SiteID != original.SiteID || (site.ID > 0 && current.SiteID != site.ID) || current.CredentialType != model.SiteCredentialTypeAccessToken {
			return result, newSub2APISessionError(sub2APISessionChanged, "订阅账号的凭据类型或所属站点已变更，请重新选择 Access Token 账号")
		}
		failure, hasFailure := sub2APIFailure(key, &current)
		if hasFailure && failure.pending != nil {
			if time.Now().Before(failure.retryAt) {
				return result, failure.err
			}
			saved, err := persistSub2APISession(key.conn, failure.pending)
			if err == nil {
				clearSub2APIFailure(key)
				return sub2APISessionResult{account: saved, refreshed: true}, nil
			}
			rememberSub2APIFailure(key, &current, err, failure.pending)
			return result, err
		}
		advanced := current.CredentialVersion != original.CredentialVersion || stripBearerPrefix(current.AccessToken) != stripBearerPrefix(original.AccessToken)
		if advanced && (sub2APIAccessTokenExpiresAt(&current) == 0 || sub2APIAccessTokenExpiresAt(&current) > time.Now().UnixMilli()) {
			return result, nil
		}
		if !force && !shouldProactivelyRefreshSub2API(&current) {
			return result, nil
		}
		if hasFailure && (failure.retryAt.IsZero() || time.Now().Before(failure.retryAt)) {
			return result, failure.err
		}
		if strings.TrimSpace(current.RefreshToken) == "" {
			return result, newSub2APISessionError(sub2APIRefreshMissing, "未配置 Refresh Token，无法自动恢复鉴权；请补充 Refresh Token 或更新 Access Token")
		}
		if current.TokenRefreshLeaseUntil > time.Now().UnixMilli() {
			if !waitSub2APISession(ctx, 100*time.Millisecond) {
				return result, newSub2APISessionError(sub2APIRefreshTransient, "其他任务正在刷新 Sub2API 会话，请稍后重试")
			}
			continue
		}
		lease := rand.Text()
		claimed := key.conn.WithContext(ctx).Model(&model.SiteAccount{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease_until <= ?", current.ID, current.CredentialVersion, time.Now().UnixMilli()).
			Updates(map[string]any{"token_refresh_lease": lease, "token_refresh_lease_until": time.Now().Add(sub2APIRefreshTimeout + 2*sub2APISaveTimeout + 5*time.Second).UnixMilli()})
		if claimed.Error != nil {
			return result, newSub2APISessionError(sub2APISessionSaveFailed, "无法取得订阅账号刷新权限，本次未刷新令牌")
		}
		if claimed.RowsAffected == 0 {
			if !waitSub2APISession(ctx, 100*time.Millisecond) {
				return result, newSub2APISessionError(sub2APIRefreshTransient, "订阅账号会话正在更新，请稍后重试")
			}
			continue
		}
		return rotateSub2APISession(ctx, key, &current, lease)
	}
}

func rotateSub2APISession(ctx context.Context, key sub2APISessionKey, current *model.SiteAccount, lease string) (sub2APISessionResult, error) {
	result := sub2APISessionResult{account: current}
	var site model.Site
	if err := key.conn.WithContext(ctx).First(&site, current.SiteID).Error; err != nil || site.Platform != model.SitePlatformSub2API {
		releaseSub2APIRefreshLease(key, current, lease)
		return result, newSub2APISessionError(sub2APISessionChanged, "订阅站配置已变更，无法刷新 Sub2API 会话")
	}
	requestCtx, cancel := context.WithTimeout(ctx, sub2APIRefreshTimeout)
	// The refresh endpoint is public. Omit the expired bearer, but retain
	// explicitly configured site headers such as a Cloudflare clearance cookie.
	site.CustomHeader = sub2APIRefreshHeaders(site.CustomHeader)
	payload, requestErr := requestJSON(requestCtx, &site, http.MethodPost,
		buildSiteURL(site.BaseURL, "/api/v1/auth/refresh"),
		map[string]any{"refresh_token": strings.TrimSpace(current.RefreshToken)},
		map[string]string{"Content-Type": "application/json"}, current)
	cancel()
	if requestErr != nil {
		releaseSub2APIRefreshLease(key, current, lease)
		var latest model.SiteAccount
		if key.conn.WithContext(ctx).First(&latest, current.ID).Error == nil && latest.CredentialVersion != current.CredentialVersion {
			if latest.SiteID == current.SiteID && latest.CredentialType == model.SiteCredentialTypeAccessToken {
				return sub2APISessionResult{account: &latest}, nil
			}
			return result, newSub2APISessionError(sub2APISessionChanged, "订阅账号凭据已变更，请重新执行")
		}
		err := classifySub2APIRefreshError(requestErr)
		rememberSub2APIFailure(key, current, err, nil)
		return result, err
	}
	credentials, ok := parseSub2APIRefreshPayload(payload)
	if !ok {
		releaseSub2APIRefreshLease(key, current, lease)
		err := newSub2APISessionError(sub2APIRefreshRejected, "Sub2API 刷新响应缺少有效的新令牌，旧 Refresh Token 可能已失效，请重新获取登录凭据")
		rememberSub2APIFailure(key, current, err, nil)
		return result, err
	}
	pending := &sub2APIPendingSession{before: *current, credentials: credentials}
	saved, err := persistSub2APISession(key.conn, pending)
	if err != nil {
		// Keep a received token pair in memory for subsequent save-only retries.
		// Do not release the lease while the DB still contains the consumed token.
		rememberSub2APIFailure(key, current, err, pending)
		return result, err
	}
	clearSub2APIFailure(key)
	return sub2APISessionResult{account: saved, refreshed: true}, nil
}

func persistSub2APISession(conn *gorm.DB, pending *sub2APIPendingSession) (*model.SiteAccount, error) {
	// Credentials must not appear in SQL debug logs either.
	conn = conn.Session(&gorm.Session{Logger: logger.Discard})
	ctx, cancel := context.WithTimeout(context.Background(), sub2APISaveTimeout)
	defer cancel()
	before, credentials := pending.before, pending.credentials
	for attempt := 0; attempt < 3; attempt++ {
		written := conn.WithContext(ctx).Model(&model.SiteAccount{}).
			Where("id = ? AND site_id = ? AND credential_type = ? AND credential_version = ?", before.ID, before.SiteID, model.SiteCredentialTypeAccessToken, before.CredentialVersion).
			Updates(map[string]any{
				"access_token": credentials.AccessToken, "refresh_token": credentials.RefreshToken,
				"token_expires_at": credentials.TokenExpiresAt, "credential_version": before.CredentialVersion + 1,
				"token_refresh_lease": "", "token_refresh_lease_until": 0,
			})
		if written.Error == nil && written.RowsAffected == 1 {
			before.AccessToken, before.RefreshToken = credentials.AccessToken, credentials.RefreshToken
			before.TokenExpiresAt, before.CredentialVersion = credentials.TokenExpiresAt, before.CredentialVersion+1
			before.TokenRefreshLease, before.TokenRefreshLeaseUntil = "", 0
			return &before, nil
		}
		// An interrupted write may have committed. Read before retrying, also
		// adopting a newer manual edit instead of overwriting it.
		var latest model.SiteAccount
		if err := conn.WithContext(ctx).First(&latest, before.ID).Error; err == nil && latest.CredentialVersion != before.CredentialVersion {
			if latest.SiteID != before.SiteID || latest.CredentialType != model.SiteCredentialTypeAccessToken {
				return nil, newSub2APISessionError(sub2APISessionChanged, "刷新期间订阅账号凭据已变更，新令牌未覆盖当前配置")
			}
			return &latest, nil
		}
		if !waitSub2APISession(ctx, time.Duration(attempt+1)*100*time.Millisecond) {
			break
		}
	}
	return nil, newSub2APISessionError(sub2APISessionSaveFailed, "Sub2API 已签发新令牌，但保存失败；请检查数据库后重试，本次不会继续使用旧令牌")
}

func releaseSub2APIRefreshLease(key sub2APISessionKey, account *model.SiteAccount, lease string) {
	ctx, cancel := context.WithTimeout(context.Background(), sub2APISaveTimeout)
	defer cancel()
	_ = key.conn.WithContext(ctx).Model(&model.SiteAccount{}).
		Where("id = ? AND credential_version = ? AND token_refresh_lease = ?", account.ID, account.CredentialVersion, lease).
		Updates(map[string]any{"token_refresh_lease": "", "token_refresh_lease_until": 0}).Error
}

func sub2APIRefreshHeaders(headers []model.CustomHeader) []model.CustomHeader {
	filtered := make([]model.CustomHeader, 0, len(headers))
	for _, header := range headers {
		key := strings.TrimSpace(header.HeaderKey)
		if !strings.EqualFold(key, "Authorization") {
			filtered = append(filtered, header)
		}
	}
	return filtered
}

func classifySub2APIRefreshError(err error) error {
	if permanent := sub2APIPermanentAuthError(err); permanent != nil {
		return permanent
	}
	status, _ := sub2APIUpstreamErrorDetails(err)
	if apperror.Code(err) == CodeSiteUpstreamDecodeFailed {
		return newSub2APISessionError(sub2APIRefreshRejected, "Sub2API 刷新响应无法解析，旧 Refresh Token 可能已失效，请重新获取登录凭据")
	}
	if status == http.StatusUnauthorized {
		return newSub2APISessionError(sub2APISessionInvalid, "Sub2API Refresh Token 已失效，请重新获取独立会话的登录凭据")
	}
	if status == 0 || status == http.StatusTooManyRequests || status >= 500 {
		return newSub2APISessionError(sub2APIRefreshTransient, "Sub2API 刷新暂时不可用或响应未收到，已保留凭据，请稍后重试")
	}
	return newSub2APISessionError(sub2APIRefreshRejected, fmt.Sprintf("Sub2API 刷新接口拒绝请求（HTTP %d），请检查权限及会话配置", status))
}

func waitSub2APISession(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
