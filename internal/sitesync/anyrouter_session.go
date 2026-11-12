package sitesync

import (
	"context"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"golang.org/x/net/publicsuffix"
)

type anyRouterSessionKey struct{}

// A session belongs to one account operation, never to the shared HTTP client.
type anyRouterSession struct {
	account *model.SiteAccount
	origin  string
	mu      sync.Mutex
	cookies *cookiejar.Jar
	// Mark server-owned cookie names with the same domain/path rules. Keeping
	// these markers prevents an expired/deleted cookie being reseeded from an
	// older credential string on the next request.
	replaced   *cookiejar.Jar
	cookieAuth string
	secrets    map[string]struct{}
}

func newAnyRouterSession(site *model.Site, account *model.SiteAccount) *anyRouterSession {
	u, _ := url.Parse(site.BaseURL)
	origin := ""
	if u != nil {
		origin = u.Scheme + "://" + u.Host
	}
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	markers, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &anyRouterSession{account: account, origin: origin, cookies: jar, replaced: markers, secrets: make(map[string]struct{})}
}

func withAnyRouterSession(ctx context.Context, site *model.Site, account *model.SiteAccount) context.Context {
	if site == nil || !usesRouterSession(site.Platform) {
		return ctx
	}
	if anyRouterSessionFromContext(ctx, site, account) != nil {
		return ctx
	}
	return context.WithValue(ctx, anyRouterSessionKey{}, newAnyRouterSession(site, account))
}

func anyRouterSessionFromContext(ctx context.Context, site *model.Site, account *model.SiteAccount) *anyRouterSession {
	session, _ := ctx.Value(anyRouterSessionKey{}).(*anyRouterSession)
	if session == nil || site == nil || session.account != account {
		return nil
	}
	u, err := url.Parse(site.BaseURL)
	if err != nil || session.origin != u.Scheme+"://"+u.Host {
		return nil
	}
	return session
}

func (s *anyRouterSession) persist(ctx context.Context, site *model.Site, original *model.SiteAccount, resolvedToken string) {
	if original.ID <= 0 || db.GetDB() == nil {
		return
	}
	updates := make(map[string]any)
	if resolvedToken != "" && resolvedToken != original.AccessToken {
		updates["access_token"] = resolvedToken
	}
	if userID := firstManagedPlatformUserID(s.account); userID > 0 && userID != firstManagedPlatformUserID(original) {
		updates["platform_user_id"] = userID
	}
	if token := s.resolvedCredential(site, resolvedToken); token != "" && token != original.AccessToken {
		updates["access_token"] = token
	}
	if len(updates) == 0 {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Do not overwrite a credential edit or another operation's different session.
	err := db.GetDB().WithContext(persistCtx).Model(&model.SiteAccount{}).
		Where("id = ? AND credential_version = ? AND access_token = ?", original.ID, original.CredentialVersion, original.AccessToken).
		Updates(updates).Error
	if err != nil {
		log.Warnf("could not save router session for account %d: %v", original.ID, err)
	}
}

func (s *anyRouterSession) resolvedCredential(site *model.Site, token string) string {
	cookie := s.authCookie()
	if cookie == "" && anyRouterIsCookieCredential(token) {
		cookie = stripBearerPrefix(token)
	}
	if cookie != "" {
		u, err := url.Parse(buildSiteURL(site.BaseURL, "/api/user/self"))
		if err == nil {
			updated := s.cookieHeader(u, cookie)
			if anyRouterHasUsableSessionCookie(updated) {
				return updated
			}
		}
	}
	return token
}

func (s *anyRouterSession) redact(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}

func (s *anyRouterSession) redactError(err error) error {
	if err == nil {
		return nil
	}
	params := maps.Clone(apperror.Params(err))
	for key, value := range params {
		if text, ok := value.(string); ok {
			params[key] = s.redact(text)
		}
	}
	message := s.redact(apperror.Message(err))
	return apperror.Wrap(apperror.Code(err), message, err).
		WithStatus(apperror.Status(err)).WithParams(params).WithLogMessage(message)
}

func (s *anyRouterSession) cookieHeader(u *url.URL, base string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	overridden := make(map[string]bool)
	for _, cookie := range s.replaced.Cookies(u) {
		overridden[cookie.Name] = true
	}
	request := &http.Request{Header: http.Header{"Cookie": []string{base}}}
	merged := &http.Request{Header: make(http.Header)}
	for _, cookie := range request.Cookies() {
		if !overridden[cookie.Name] {
			merged.AddCookie(cookie)
		}
	}
	for _, cookie := range s.cookies.Cookies(u) {
		merged.AddCookie(cookie)
	}
	return merged.Header.Get("Cookie")
}

func (s *anyRouterSession) receiveCookies(u *url.URL, cookies []*http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookies.SetCookies(u, cookies)
	markers := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Value != "" {
			s.secrets[cookie.Value] = struct{}{}
		}
		marker := *cookie
		marker.Value, marker.MaxAge, marker.Expires = "updated", 0, time.Now().Add(24*time.Hour)
		markers = append(markers, &marker)
	}
	s.replaced.SetCookies(u, markers)
}

func (s *anyRouterSession) useCookieAuth(cookie string) {
	s.mu.Lock()
	s.cookieAuth = cookie
	s.mu.Unlock()
}

func (s *anyRouterSession) authCookie() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cookieAuth
}

func rememberAnyRouterCookieAuth(ctx context.Context, site *model.Site, account *model.SiteAccount, cookie string) {
	if session := anyRouterSessionFromContext(ctx, site, account); session != nil {
		session.useCookieAuth(cookie)
	}
}

type anyRouterSessionTransport struct {
	base    http.RoundTripper
	session *anyRouterSession
}

func usesRouterSession(platform model.SitePlatform) bool {
	return platform == model.SitePlatformAnyRouter || platform == model.SitePlatformAgentRouter
}

func (t anyRouterSessionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	if t.session.origin == request.URL.Scheme+"://"+request.URL.Host {
		cookie := t.session.cookieHeader(request.URL, request.Header.Get("Cookie"))
		request.Header.Del("Cookie")
		if cookie != "" {
			request.Header.Set("Cookie", cookie)
		}
	}
	response, err := t.base.RoundTrip(request)
	if err == nil && t.session.origin == request.URL.Scheme+"://"+request.URL.Host {
		t.session.receiveCookies(request.URL, response.Cookies())
	}
	return response, err
}

func anyRouterIsCookieCredential(token string) bool {
	raw := stripBearerPrefix(strings.TrimSpace(token))
	return strings.Contains(raw, ";") || anyRouterHasUsableSessionCookie(raw)
}
