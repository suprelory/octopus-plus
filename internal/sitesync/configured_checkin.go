package sitesync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const siteCheckinResponseLimit = 1 << 20

func checkinConfiguredHTTP(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*model.SiteCheckinResult, string, error) {
	token := ""
	if siteRecord.CheckinUseSubscriptionToken {
		sourceSite, source, err := op.SiteCheckinSubscriptionTokenAccount(siteRecord, account, ctx)
		if err != nil {
			return nil, "", err
		}
		auth := &sub2APICheckinAuth{site: sourceSite, account: source, token: stripBearerPrefix(source.AccessToken)}
		ctx = context.WithValue(withSub2APIRecoveryBudget(ctx), sub2APICheckinAuthKey{}, auth)
		refreshed, refreshErr := ensureFreshSub2APIAccessToken(ctx, sourceSite, source, false)
		if refreshErr != nil {
			auth.err = refreshErr
			if siteRecord.CheckinStatus == nil {
				return nil, auth.token, refreshErr
			}
		} else {
			auth.token = refreshed
		}
		token = auth.token
	}
	if siteRecord.CheckinStatus != nil {
		result, _, err := checkinWithStatus(ctx, siteRecord, account, token)
		return result, configuredCheckinToken(ctx, token), err
	}
	response, _, err := requestConfiguredCheckinHTTP(ctx, siteRecord, account, token)
	if err != nil {
		return nil, token, err
	}
	result, _, interpretErr := interpretConfiguredCheckinResponse(ctx, siteRecord, response)
	if result != nil && result.Status == model.SiteExecutionStatusSuccess {
		return result, token, interpretErr
	}
	if siteRecord.CheckinUseSubscriptionToken && response.StatusCode == http.StatusUnauthorized {
		recovered, refreshErr := recoverSub2APICheckinAuth(ctx, response)
		if refreshErr != nil {
			return nil, configuredCheckinToken(ctx, token), refreshErr
		}
		if recovered {
			return &model.SiteCheckinResult{Status: model.SiteExecutionStatusFailed, Reason: "checkin_auth_recovered",
				Message: "签到接口返回 401，登录凭据已更新；本次未自动重发签到请求，请确认站点状态后再操作"}, configuredCheckinToken(ctx, token), nil
		}
	}
	return result, token, interpretErr
}

type configuredCheckinResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func validateConfiguredCheckinCredentials(site *model.Site, account *model.SiteAccount, token string) error {
	if !site.CheckinUseSubscriptionToken {
		return model.ValidateSiteCheckinCookie(account.Cookie)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("linked subscription Access Token is required for checkin")
	}
	if len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("linked subscription Access Token must be a single header value of at most 8 KiB")
	}
	return nil
}

func requestConfiguredCheckinHTTP(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, token string) (*configuredCheckinResponse, string, error) {
	// Tokens come only from an explicitly selected subscription account.
	// The check-in account's retained platform credentials are never used.
	if err := validateConfiguredCheckinCredentials(siteRecord, account, token); err != nil {
		return nil, token, err
	}

	method := strings.ToUpper(strings.TrimSpace(siteRecord.CheckinHTTPMethod))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, token, fmt.Errorf("configured checkin method must be GET or POST")
	}
	endpoint, err := buildConfiguredCheckinURL(siteRecord.BaseURL, siteRecord.CheckinHTTPPath)
	if err != nil {
		return nil, token, err
	}
	body := expandCheckinHTTPBodyTemplate(siteRecord.CheckinHTTPBody, account, token)
	if len(body) > 64<<10 {
		return nil, token, fmt.Errorf("configured checkin body exceeds 64 KiB after credential substitution")
	}
	if body != "" && !json.Valid([]byte(body)) {
		return nil, token, fmt.Errorf("configured checkin body is invalid after credential substitution")
	}

	httpClient, err := configuredCheckinHTTPClient(ctx, siteRecord, account)
	if err != nil {
		return nil, token, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, siteRequestTimeout)
	defer cancel()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bodyReader)
	if err != nil {
		return nil, token, err
	}
	applyDefaultSiteRequestHeaders(req, method == http.MethodPost)
	if parsed, parseErr := url.Parse(siteRecord.BaseURL); parseErr == nil {
		origin := parsed.Scheme + "://" + parsed.Host
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
	}
	for _, header := range siteRecord.CustomHeader {
		if key := strings.TrimSpace(header.HeaderKey); key != "" {
			req.Header.Set(key, header.HeaderValue)
		}
	}
	for _, header := range siteRecord.CheckinHTTPHeaders {
		key := strings.TrimSpace(header.HeaderKey)
		if key != "" && !strings.EqualFold(key, "Cookie") {
			value := expandCheckinHTTPTemplate(header.HeaderValue, account, token)
			if len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
				return nil, token, fmt.Errorf("configured checkin header %q is invalid after credential substitution", key)
			}
			req.Header.Set(key, value)
		}
	}
	if siteRecord.CheckinUseSubscriptionToken {
		// Apply last so stale shared/endpoint headers cannot replace the selected
		// account's identity. A previous cookie login must not compete with it.
		req.Header.Del("Cookie")
		req.Header.Set("Authorization", ensureBearer(token))
		applySub2APICheckinUserAgent(ctx, req)
	} else {
		// The account cookie takes precedence over legacy shared Cookie headers.
		req.Header.Set("Cookie", strings.TrimSpace(account.Cookie))
	}
	resp, err := checkinHTTPClient(httpClient).Do(req)
	if err != nil {
		return nil, token, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, siteCheckinResponseLimit+1))
	if err != nil {
		return nil, token, fmt.Errorf("read checkin response: %w", err)
	}
	if len(responseBody) > siteCheckinResponseLimit {
		return nil, token, fmt.Errorf("checkin response exceeds 1 MiB")
	}
	return &configuredCheckinResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: responseBody}, token, nil
}

func interpretConfiguredCheckinResponse(ctx context.Context, siteRecord *model.Site, resp *configuredCheckinResponse) (*model.SiteCheckinResult, string, error) {
	const token = ""
	responseBody := resp.Body
	payload, isJSON := parseSiteJSONMap(responseBody)
	message := ""
	if isJSON {
		message = extractSiteResponseMessage(payload)
	}
	// An explicit already-checked-in hint takes precedence over HTTP status and
	// success=false. Keep non-JSON response bodies out of the persisted message.
	if strings.Contains(message, "今日已签到") || (!isJSON && strings.Contains(string(responseBody), "今日已签到")) {
		result := newSuccessfulCheckinResult(firstNonEmptyString(message, "今日已签到"), "")
		result.Reason = model.SiteCheckinReasonAlreadyCheckedIn
		return result, token, nil
	}
	if IsCloudflareProtectionResponse(resp.StatusCode, resp.Header, responseBody) {
		return nil, token, wrapCloudflareProtectionError(newCloudflareProtectionError(resp.StatusCode, resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isJSON && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict) && isAlreadyCheckedInMessage(message) {
			return newSuccessfulCheckinResult(message, ""), token, nil
		}
		err := formatSiteHTTPError(resp.StatusCode, resp.Header, responseBody)
		if isUnsupportedCheckinError(err) {
			return newUnsupportedCheckinResult(""), token, nil
		}
		return nil, token, err
	}
	if !isJSON {
		return newUnconfirmedCheckinResult(siteRecord), token, nil
	}
	if !isAlreadyCheckedInMessage(message) {
		if !jsonBool(payload["success"]) && isUnsupportedCheckinMessage(message) {
			return newUnsupportedCheckinResult(message), token, nil
		}
		if success, present := payload["success"]; present && success != nil && !jsonBool(success) {
			return &model.SiteCheckinResult{
				Status:  model.SiteExecutionStatusFailed,
				Message: firstNonEmptyString(message, "checkin failed"),
			}, token, nil
		}
	}
	if !jsonBool(payload["success"]) && !isAlreadyCheckedInMessage(message) {
		return newUnconfirmedCheckinResult(siteRecord), token, nil
	}
	result := newSuccessfulCheckinResult(message, checkinRewardString(nestedValue(payload, "data", "reward")))
	if result.Reason == model.SiteCheckinReasonCheckedIn && strings.TrimSpace(siteRecord.CheckinRewardExtractor) != "" {
		reward, extractErr := ExtractCheckinReward(ctx, siteRecord.CheckinRewardExtractor, responseBody)
		if extractErr != nil {
			log.Warnf("checkin reward extractor failed for site %d: %v", siteRecord.ID, extractErr)
		} else if reward != "" {
			result.Reward = reward
		}
	}
	return result, token, nil
}

func newUnconfirmedCheckinResult(site *model.Site) *model.SiteCheckinResult {
	// A successful HTTP exchange does not establish that the account checked in.
	// Keep the evidence unknown, and allow normal failure backoff without
	// recording a success timestamp, extracting rewards or refreshing balances.
	message := "签到响应未确认成功，请检查签到接口和 Cookie"
	if site.CheckinUseSubscriptionToken {
		message = "签到响应未确认成功，请检查签到接口和订阅账号 Access Token"
	}
	return &model.SiteCheckinResult{
		Status: model.SiteExecutionStatusFailed, Reason: model.SiteCheckinReasonUnconfirmed,
		Message:            message,
		CapabilityEvidence: model.SiteCheckinSupportUnknown,
	}
}

func expandCheckinHTTPTemplate(value string, account *model.SiteAccount, resolvedToken string) string {
	return strings.NewReplacer(checkinHTTPTemplateReplacements(account, resolvedToken)...).Replace(value)
}

func expandCheckinHTTPBodyTemplate(value string, account *model.SiteAccount, resolvedToken string) string {
	replacements := checkinHTTPTemplateReplacements(account, resolvedToken)
	for i := 1; i < len(replacements); i += 2 {
		encoded, _ := json.Marshal(replacements[i])
		replacements[i] = string(encoded[1 : len(encoded)-1])
	}
	return strings.NewReplacer(replacements...).Replace(value)
}

func checkinHTTPTemplateReplacements(account *model.SiteAccount, _ string) []string {
	return []string{
		"{{cookie}}", account.Cookie,
		// Retired platform credential placeholders must not disclose stale
		// credentials retained in a migrated external check-in account.
		"{{access_token}}", "",
		"{{api_key}}", "",
		"{{username}}", account.Username,
		"{{password}}", "",
		"{{refresh_token}}", "",
		"{{platform_user_id}}", "",
	}
}

func buildConfiguredCheckinURL(baseURL, checkinPath string) (string, error) {
	if err := model.ValidateSiteCheckinHTTPPath(checkinPath); err != nil {
		return "", err
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !base.IsAbs() || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return "", fmt.Errorf("site base URL is invalid")
	}
	path, err := url.ParseRequestURI(checkinPath)
	if err != nil {
		return "", fmt.Errorf("configured checkin path is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path.Path
	base.RawPath = ""
	base.RawQuery = path.RawQuery
	base.Fragment = ""
	return base.String(), nil
}

func checkinHTTPClient(client *http.Client) *http.Client {
	clone := *client
	previousCheckRedirect := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameCheckinOrigin(via[len(via)-1].URL, request.URL) {
			return http.ErrUseLastResponse
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 checkin redirects")
		}
		return nil
	}
	return &clone
}

func sameCheckinOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || left.User != nil || right.User != nil || !strings.EqualFold(left.Scheme, right.Scheme) || !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	port := func(value *url.URL) string {
		if explicit := value.Port(); explicit != "" {
			return explicit
		}
		if strings.EqualFold(value.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(value.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return net.JoinHostPort(strings.ToLower(left.Hostname()), port(left)) == net.JoinHostPort(strings.ToLower(right.Hostname()), port(right))
}
