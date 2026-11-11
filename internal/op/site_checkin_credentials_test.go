package op

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCheckinBackupRemapsLinkedAccountAndPreservesCookie(t *testing.T) {
	ctx := setupBackupTestDB(t)
	occupied := &model.Site{Name: "Existing", Platform: model.SitePlatformAPI, BaseURL: "https://existing.example"}
	if err := SiteCreate(occupied, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SiteAccountCreate(&model.SiteAccount{SiteID: occupied.ID, Name: "Existing", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "existing-key"}, ctx); err != nil {
		t.Fatal(err)
	}
	sourceSiteID, sourceID := 1, 1
	dump := &model.DBDump{Version: 1, Sites: []model.Site{
		{ID: 2, Name: "Rewards", Kind: model.SiteKindCheckin, LinkedSiteID: &sourceSiteID, Platform: model.SitePlatformOneAPI, BaseURL: "https://platform.example"},
		{ID: 1, Name: "Subscription", Kind: model.SiteKindRelay, Platform: model.SitePlatformOneAPI, BaseURL: "https://platform.example"},
		{ID: 3, Name: "External", Kind: model.SiteKindCheckin, LinkedSiteID: &sourceSiteID, Platform: model.SitePlatformAPI, BaseURL: "https://external.example", CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily", CheckinRewardExtractor: "return response.data?.amount ?? null;"},
	}, SiteAccounts: []model.SiteAccount{
		{ID: 2, SiteID: 2, Name: "Rewards", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &sourceID},
		{ID: 1, SiteID: 1, Name: "Source", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token"},
		{ID: 3, SiteID: 3, Name: "Cookie", CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=backup-cookie", LinkedAccountID: &sourceID},
	}}
	dump.Sites[2].CheckinStatus = &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.done"}
	if err := dump.Sites[2].Validate(); err != nil {
		t.Fatal(err)
	}
	dump.SiteAccounts[2].Normalize()
	dump.SiteAccounts[2].CheckinPending = &model.SiteCheckinPending{LogID: 456, Date: "2026-11-11", Submitted: true,
		StartedAt: time.Now(), Deadline: time.Now().Add(time.Hour), Fingerprint: dump.Sites[2].CheckinWorkflowFingerprint(&dump.SiteAccounts[2])}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	var account, cookie model.SiteAccount
	if err := db.GetDB().Where("name = ?", "Rewards").First(&account).Error; err != nil {
		t.Fatal(err)
	}
	site, err := SiteGet(account.SiteID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	source, err := SiteCheckinLinkedAccount(site, &account, ctx)
	if err != nil || source.ID == sourceID || source.AccessToken != "source-token" {
		t.Fatalf("binding not remapped: %+v, %v", source, err)
	}
	if err := db.GetDB().Where("name = ?", "Cookie").First(&cookie).Error; err != nil {
		t.Fatal(err)
	}
	if cookie.Cookie != "session=backup-cookie" || cookie.CredentialType != model.SiteCredentialTypeCookie || cookie.LinkedAccountID == nil || *cookie.LinkedAccountID != source.ID {
		t.Fatal("cookie or balance account link lost in backup")
	}
	external, err := SiteGet(cookie.SiteID, ctx)
	if err != nil || external.CheckinRewardExtractor != "return response.data?.amount ?? null;" {
		t.Fatalf("extractor lost in backup: %+v, %v", external, err)
	}
	if external.CheckinStatus == nil || cookie.CheckinPending == nil || cookie.CheckinPending.Fingerprint != external.CheckinWorkflowFingerprint(&cookie) {
		t.Fatal("pending workflow not remapped with linked subscription")
	}
	_, balanceAccount, err := SiteCheckinBalanceAccount(external, &cookie, ctx)
	if err != nil || balanceAccount.ID != source.ID {
		t.Fatalf("external balance link not remapped: %+v, %v", balanceAccount, err)
	}
	if err := SiteAccountDel(source.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	account = model.SiteAccount{}
	if err := db.GetDB().Where("name = ?", "Rewards").First(&account).Error; err != nil {
		t.Fatal(err)
	}
	if account.LinkedAccountID != nil {
		t.Fatal("reimport rebound a deleted source")
	}
	savedCookie, err := SiteAccountGet(cookie.ID, ctx)
	if err != nil || savedCookie.LinkedAccountID != nil || savedCookie.CredentialType != model.SiteCredentialTypeCookie || savedCookie.Cookie != "session=backup-cookie" {
		t.Fatal("deleting the balance source changed cookie credentials")
	}
}

func TestCheckinStatusConfigurationAndPendingBackup(t *testing.T) {
	ctx := setupBackupTestDB(t)
	site := &model.Site{Name: "Status", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI, BaseURL: "https://status.example", CheckinHTTPEnabled: true, CheckinHTTPPath: "/checkin"}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	var request model.SiteUpdateRequest
	if err := json.Unmarshal([]byte(`{"id":1,"checkin_status":{"path":"/status","checked_path":"data.done"}}`), &request); err != nil {
		t.Fatal(err)
	}
	request.ID = site.ID
	saved, err := SiteUpdate(&request, ctx)
	if err != nil || saved.CheckinStatus == nil || saved.CheckinStatus.MaxAttempts != 3 {
		t.Fatalf("config not saved: %+v %v", saved, err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Pending", CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=backup", Enabled: true}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	account.CheckinPending = &model.SiteCheckinPending{LogID: 123, Date: "2026-11-11", Submitted: true, BeforeUnchecked: true, StartedAt: time.Now(), Deadline: time.Now().Add(time.Hour), Fingerprint: saved.CheckinWorkflowFingerprint(account)}
	if err := db.GetDB().Model(account).Select("checkin_pending").Updates(account).Error; err != nil {
		t.Fatal(err)
	}
	dump, err := DBExportAll(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	// Import into a fresh DB with occupied IDs so both site/account IDs change.
	ctx = setupBackupTestDB(t)
	occupied := &model.Site{Name: "Occupied", Platform: model.SitePlatformAPI, BaseURL: "https://occupied.example"}
	if err := SiteCreate(occupied, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SiteAccountCreate(&model.SiteAccount{SiteID: occupied.ID, Name: "Occupied", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "key"}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	var restored model.SiteAccount
	if err := db.GetDB().Where("name = ?", "Pending").First(&restored).Error; err != nil {
		t.Fatal(err)
	}
	restoredSite, _ := SiteGet(restored.SiteID, ctx)
	if restored.ID == account.ID || restored.CheckinPending == nil || !restored.CheckinPending.Submitted || restored.CheckinPending.LogID != 123 || restored.CheckinPending.Fingerprint != restoredSite.CheckinWorkflowFingerprint(&restored) {
		t.Fatalf("pending workflow not restored safely: %+v", restored)
	}
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: restoredSite.ID, CheckinStatusSet: true}, ctx); err != nil {
		t.Fatal(err)
	}
	cleared, _ := SiteGet(restoredSite.ID, ctx)
	if cleared.CheckinStatus != nil {
		t.Fatal("status config not cleared")
	}
}

func TestCheckinCredentialsRejectWrongSiteAndAcceptCookieUpdates(t *testing.T) {
	ctx := setupBackupTestDB(t)
	site := &model.Site{Name: "Subscription", Platform: model.SitePlatformOneAPI, BaseURL: "https://platform.example"}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SiteAccountCreate(&model.SiteAccount{SiteID: site.ID, Name: "Invalid", CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=secret"}, ctx); err == nil {
		t.Fatal("subscription accepted cookie credentials")
	}
	source := &model.SiteAccount{SiteID: site.ID, Name: "Source", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "token"}
	if err := SiteAccountCreate(source, ctx); err != nil {
		t.Fatal(err)
	}
	checkin := &model.Site{Name: "External", Kind: model.SiteKindCheckin, Platform: site.Platform, BaseURL: "https://other.example", LinkedSiteID: &site.ID}
	if err := SiteCreate(checkin, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: checkin.ID, Name: "Linked", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &source.ID}
	if err := SiteAccountCreate(account, ctx); err == nil {
		t.Fatal("platform credentials could be sent to another URL")
	}
	custom, path := true, "/daily"
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: checkin.ID, CheckinHTTPEnabled: &custom, CheckinHTTPPath: &path}, ctx); err != nil {
		t.Fatal(err)
	}
	account.CredentialType, account.LinkedAccountID, account.Cookie = model.SiteCredentialTypeCookie, &source.ID, "session=old"
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	var req model.SiteAccountUpdateRequest
	if err := json.Unmarshal([]byte(`{"cookie":"session=new","linked_account_id":null}`), &req); err != nil {
		t.Fatal(err)
	}
	req.ID = account.ID
	updated, err := SiteAccountUpdate(&req, ctx)
	if err != nil || updated.Cookie != "session=new" || !req.LinkedAccountIDSet || updated.LinkedAccountID != nil {
		t.Fatalf("cookie update: %+v, %v", updated, err)
	}
	// A cookie account cannot query an account outside its associated site.
	other := &model.Site{Name: "Other", Platform: site.Platform, BaseURL: "https://third.example"}
	if err := SiteCreate(other, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: checkin.ID, LinkedSiteID: &other.ID, LinkedSiteIDSet: true}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, LinkedAccountID: &source.ID, LinkedAccountIDSet: true}, ctx); err == nil {
		t.Fatal("cookie account accepted a balance account from the wrong subscription")
	}
}

func TestCheckinRewardExtractorConfigurationCanBeSavedAndCleared(t *testing.T) {
	ctx := setupBackupTestDB(t)
	site := &model.Site{Name: "External", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI, BaseURL: "https://external.example",
		CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily", CheckinRewardExtractor: "  return response.reward;  "}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := SiteGet(site.ID, ctx)
	if err != nil || saved.CheckinRewardExtractor != "return response.reward;" {
		t.Fatalf("extractor not persisted: %+v, %v", saved, err)
	}
	for _, code := range []string{" return response.data?.reward; ", ""} {
		body, _ := json.Marshal(map[string]any{"id": site.ID, "checkin_reward_extractor": code})
		var request model.SiteUpdateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		updated, err := SiteUpdate(&request, ctx)
		if err != nil || updated.CheckinRewardExtractor != strings.TrimSpace(code) {
			t.Fatalf("extractor update: %+v, %v", updated, err)
		}
	}
	tooLong := strings.Repeat("a", model.CheckinRewardExtractorMaxBytes+1)
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinRewardExtractor: &tooLong}, ctx); err == nil {
		t.Fatal("oversized extractor accepted")
	}
}
