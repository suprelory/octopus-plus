package sitesync

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestConfiguredHTTPCheckinUnconfirmedResponsesDoNotAwardOrSucceed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"HTML login", "<html><body>Login</body></html>", http.StatusOK},
		{"generic text", "OK", http.StatusOK},
		{"empty response", "", http.StatusNoContent},
		{"empty JSON", `{}`, http.StatusOK},
		{"reward without success", `{"message":"Login required","data":{"reward":999}}`, http.StatusOK},
		{"null success", `{"success":null,"data":{"reward":999}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			site := &model.Site{
				Name: "Custom checkin", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI,
				BaseURL: server.URL, Enabled: true, CheckinHTTPEnabled: true,
				CheckinHTTPMethod: "GET", CheckinHTTPPath: "/checkin", CheckinRewardExtractor: "return 123;",
			}
			if err := op.SiteCreate(site, ctx); err != nil {
				t.Fatal(err)
			}
			previous := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Millisecond)
			if err := db.GetDB().Model(site).Updates(map[string]any{
				"checkin_verification_status":      model.SiteCheckinSupportSupported,
				"checkin_verification_fingerprint": site.CheckinConfigFingerprint(),
				"checkin_verified_at":              previous,
			}).Error; err != nil {
				t.Fatal(err)
			}
			account := &model.SiteAccount{
				SiteID: site.ID, Name: "Cookie account", CredentialType: model.SiteCredentialTypeCookie,
				Cookie: "session=checkin-cookie", Enabled: true, AutoCheckin: true,
				LastCheckinAt: &previous, LastCheckinSuccessAt: &previous, LastCheckinStatus: model.SiteExecutionStatusSuccess,
			}
			if err := op.SiteAccountCreate(account, ctx); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			result, runErr := CheckinAccount(ctx, account.ID)
			if runErr == nil || result == nil || result.Status != model.SiteExecutionStatusFailed || result.Reason != model.SiteCheckinReasonUnconfirmed || result.Reward != "" || result.LogID == 0 {
				t.Fatalf("unconfirmed response was accepted: %+v, %v", result, runErr)
			}
			if result.Capability == nil || result.Capability.Support != model.SiteCheckinSupportSupported || result.Capability.VerifiedAt == nil || !result.Capability.VerifiedAt.Equal(previous) {
				t.Fatalf("inconclusive response changed established site evidence: %+v", result.Capability)
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.LastCheckinStatus != model.SiteExecutionStatusFailed || saved.LastCheckinSuccessAt == nil || !saved.LastCheckinSuccessAt.Equal(previous) || saved.CheckinFailureCount != 1 || saved.NextAutoCheckinAt == nil || !saved.NextAutoCheckinAt.After(started) {
				t.Fatalf("unconfirmed response did not enter failure retry state: %+v", saved)
			}
			logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(logs.Items) != 1 || logs.Items[0].Status != model.SiteExecutionStatusFailed || logs.Items[0].Reason != model.SiteCheckinReasonUnconfirmed || logs.Items[0].Reward != "" {
				t.Fatalf("unconfirmed response recorded a reward or success: %+v, %v", logs, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || stats.FailedCount != 1 || stats.SuccessCount != 0 || stats.TotalReward != 0 {
				t.Fatalf("unconfirmed response inflated statistics: %+v, %v", stats, err)
			}
		})
	}
}
