package model

import "time"

// CheckinCredentialInput is used only for explicit secret-bearing writes.
// Unlike CheckinCredential, it can decode secrets and has no runtime fields.
type CheckinCredentialInput struct {
	Name        string                `json:"name"`
	Type        CheckinCredentialType `json:"type"`
	Platform    SitePlatform          `json:"platform"`
	AuthBaseURL string                `json:"auth_base_url"`
	CheckinNetwork
	AuthHeaders    []CustomHeader `json:"auth_headers"`
	Username       string         `json:"username"`
	Password       string         `json:"password"`
	AccessToken    string         `json:"access_token"`
	APIKey         string         `json:"api_key"`
	RefreshToken   string         `json:"refresh_token"`
	Cookie         string         `json:"cookie"`
	PlatformUserID *int           `json:"platform_user_id,omitempty"`
	TokenExpiresAt int64          `json:"token_expires_at"`
}

func (in CheckinCredentialInput) Credential() CheckinCredential {
	return CheckinCredential{
		Name: in.Name, Type: in.Type, Platform: in.Platform, AuthBaseURL: in.AuthBaseURL,
		CheckinNetwork: in.CheckinNetwork, AuthHeaders: in.AuthHeaders, Username: in.Username,
		Password: in.Password, AccessToken: in.AccessToken, APIKey: in.APIKey,
		RefreshToken: in.RefreshToken, Cookie: in.Cookie, PlatformUserID: in.PlatformUserID,
		TokenExpiresAt: in.TokenExpiresAt,
	}
}

// This explicit allowlist must not embed the stored credential or secret inputs.
type CheckinCredentialSummary struct {
	ID          int                   `json:"id"`
	Name        string                `json:"name"`
	Type        CheckinCredentialType `json:"type"`
	Platform    SitePlatform          `json:"platform"`
	AuthBaseURL string                `json:"auth_base_url"`
	CheckinNetwork
	Username              string `json:"username"`
	PlatformUserID        *int   `json:"platform_user_id,omitempty"`
	TokenExpiresAt        int64  `json:"token_expires_at"`
	CredentialVersion     int64  `json:"credential_version"`
	IdentityRevision      int64  `json:"identity_revision"`
	Configured            bool   `json:"configured"`
	SessionState          string `json:"session_state"`
	SessionRetryAt        int64  `json:"session_retry_at,omitempty"`
	HasPassword           bool   `json:"has_password"`
	HasAccessToken        bool   `json:"has_access_token"`
	HasAPIKey             bool   `json:"has_api_key"`
	HasRefreshToken       bool   `json:"has_refresh_token"`
	HasCookie             bool   `json:"has_cookie"`
	HasAuthHeaders        bool   `json:"has_auth_headers"`
	PrimaryReferenceCount int    `json:"primary_reference_count"`
	BalanceReferenceCount int    `json:"balance_reference_count"`
	ReferenceCount        int    `json:"reference_count"`
}

func (c CheckinCredential) Summary(primaryRefs, balanceRefs int) CheckinCredentialSummary {
	// Validate a copy to report incomplete imported credentials without exposing
	// secrets or silently replacing them with legacy subscription credentials.
	configured := c.Validate() == nil
	state := "idle"
	now := time.Now().UnixMilli()
	switch {
	case c.TokenRefreshLeaseUntil > now:
		state = "exchanging"
	case c.TokenRefreshStarted:
		state = "needs_credentials"
	case c.TokenRefreshRetryAt > now:
		state = "cooldown"
	}
	return CheckinCredentialSummary{
		ID: c.ID, Name: c.Name, Type: c.Type, Platform: c.Platform, AuthBaseURL: c.AuthBaseURL,
		CheckinNetwork: c.CheckinNetwork, Username: c.Username, PlatformUserID: c.PlatformUserID,
		TokenExpiresAt: c.TokenExpiresAt, CredentialVersion: c.CredentialVersion, IdentityRevision: c.IdentityRevision,
		Configured: configured, SessionState: state, SessionRetryAt: c.TokenRefreshRetryAt, HasPassword: c.Password != "", HasAccessToken: c.AccessToken != "",
		HasAPIKey: c.APIKey != "", HasRefreshToken: c.RefreshToken != "", HasCookie: c.Cookie != "", HasAuthHeaders: len(c.AuthHeaders) > 0,
		PrimaryReferenceCount: primaryRefs, BalanceReferenceCount: balanceRefs, ReferenceCount: primaryRefs + balanceRefs,
	}
}

// CheckinCredentialBackup is a deliberate secret export boundary for backup v2.
// Runtime ownership is neither exported nor restored. An unresolved exchange is
// exported as a repair flag so importing cannot re-use a possibly consumed token.
type CheckinCredentialBackup struct {
	CheckinCredentialInput
	SessionNeedsRepair bool  `json:"session_needs_repair,omitempty"`
	ID                 int   `json:"id"`
	CredentialVersion  int64 `json:"credential_version"`
	IdentityRevision   int64 `json:"identity_revision"`
	TokenRefreshedAt   int64 `json:"token_refreshed_at"`
}

func ExportCheckinCredential(c CheckinCredential) CheckinCredentialBackup {
	return CheckinCredentialBackup{
		CheckinCredentialInput: CheckinCredentialInput{
			Name: c.Name, Type: c.Type, Platform: c.Platform, AuthBaseURL: c.AuthBaseURL,
			CheckinNetwork: c.CheckinNetwork, AuthHeaders: c.AuthHeaders, Username: c.Username,
			Password: c.Password, AccessToken: c.AccessToken, APIKey: c.APIKey,
			RefreshToken: c.RefreshToken, Cookie: c.Cookie, PlatformUserID: c.PlatformUserID, TokenExpiresAt: c.TokenExpiresAt,
		},
		ID: c.ID, CredentialVersion: c.CredentialVersion, IdentityRevision: c.IdentityRevision, TokenRefreshedAt: c.TokenRefreshedAt, SessionNeedsRepair: c.TokenRefreshStarted,
	}
}

// The importer is responsible for ID mapping and conflict checks before saving.
func (b CheckinCredentialBackup) Credential() CheckinCredential {
	c := b.CheckinCredentialInput.Credential()
	c.ID, c.CredentialVersion, c.IdentityRevision, c.TokenRefreshedAt = b.ID, b.CredentialVersion, b.IdentityRevision, b.TokenRefreshedAt
	c.TokenRefreshStarted = b.SessionNeedsRepair
	return c
}
