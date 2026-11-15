package op

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func checkinPatch[T any](t *testing.T, body string) T {
	t.Helper()
	var patch T
	if err := json.Unmarshal([]byte(body), &patch); err != nil {
		t.Fatal(err)
	}
	return patch
}

func TestIndependentCheckinLifecyclePreservesSubmittedWorkflow(t *testing.T) {
	store, ctx := independentCheckinStore(t), context.Background()
	site := createIndependentSite(t, store)
	var err error
	site, err = store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"enabled":true,"external_checkin_url":"https://manual.example","checkin_status":{"path":"/status","checked_path":"checked"}}`))
	if err != nil {
		t.Fatal(err)
	}
	credential := testCheckinCredential()
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "one", AuthMode: model.CheckinAuthBearer,
		Enabled: true, AutoCheckin: true, RandomCheckin: true, CheckinRandomWindowMinutes: 10}, &credential)
	if err != nil {
		t.Fatal(err)
	}
	storedCredential, err := store.GetCredential(ctx, account.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	account.CheckinPending = &model.SiteCheckinPending{LogID: 123, Date: "2026-11-16", Submitted: true,
		Fingerprint: site.WorkflowFingerprint(*account, *storedCredential), StartedAt: now, Deadline: now.Add(time.Hour)}
	account.NextAutoCheckinAt, account.LastCheckinAt, account.LastCheckinSuccessAt = &now, &now, &now
	account.Balance, account.BalanceUsed, account.TodayIncome, account.CheckinFailureCount = 50, 12, 2, 3
	if err := store.conn.Select("checkin_pending", "next_auto_checkin_at", "last_checkin_at", "last_checkin_success_at", "balance", "balance_used", "today_income", "checkin_failure_count").Updates(account).Error; err != nil {
		t.Fatal(err)
	}
	site.VerificationFingerprint, site.VerificationStatus, site.VerifiedAt = site.ConfigFingerprint(), model.SiteCheckinSupportSupported, &now
	if err := store.conn.Select("verification_fingerprint", "verification_status", "verified_at").Updates(site).Error; err != nil {
		t.Fatal(err)
	}
	assertWorkflow := func() *model.CheckinAccount {
		t.Helper()
		stored, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored.CheckinPending, account.CheckinPending) || stored.Balance != 50 || stored.BalanceUsed != 12 || stored.TodayIncome != 2 || stored.CheckinFailureCount != 3 || stored.LastCheckinAt == nil || !stored.LastCheckinAt.Equal(now) || stored.LastCheckinSuccessAt == nil || !stored.LastCheckinSuccessAt.Equal(now) {
			t.Fatal("configuration change altered runtime state or submitted workflow")
		}
		return stored
	}
	renamed, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"name":"renamed"}`))
	if err != nil || renamed.VerificationStatus != model.SiteCheckinSupportSupported || renamed.VerificationFingerprint != site.VerificationFingerprint {
		t.Fatalf("cosmetic edit invalidated verification: %v", err)
	}
	if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"name":"renamed account"}`)); err != nil {
		t.Fatal(err)
	}
	if assertWorkflow().NextAutoCheckinAt == nil {
		t.Fatal("cosmetic edits changed schedule")
	}
	changed, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"checkin_http_path":"/new-checkin","external_checkin_url":null,"checkin_status":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if changed.VerificationStatus != model.SiteCheckinSupportUnknown || changed.VerificationFingerprint != "" || changed.VerifiedAt != nil || changed.ExternalCheckinURL != nil || changed.CheckinStatus != nil {
		t.Fatal("execution edit retained stale verification or ignored explicit null")
	}
	if assertWorkflow().NextAutoCheckinAt != nil {
		t.Fatal("execution change did not invalidate schedule")
	}
	if changed.WorkflowFingerprint(*account, *storedCredential) == account.CheckinPending.Fingerprint {
		t.Fatal("changed endpoint kept pending workflow authorized")
	}
	archived, err := store.ArchiveSite(ctx, site.ID)
	if err != nil || !archived.Archived || archived.ArchivedAt == nil || !archived.Enabled {
		t.Fatalf("archive did not preserve availability settings: %v", err)
	}
	again, err := store.ArchiveSite(ctx, site.ID)
	if err != nil || !again.ArchivedAt.Equal(*archived.ArchivedAt) {
		t.Fatalf("archive was not idempotent: %v", err)
	}
	if sites, err := store.ListSites(ctx, false); err != nil || len(sites) != 0 {
		t.Fatalf("archived site remained in active list: %v", err)
	}
	if sites, err := store.ListSites(ctx, true); err != nil || len(sites) != 1 {
		t.Fatalf("archived site disappeared: %v", err)
	}
	if _, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "archived", AuthMode: model.CheckinAuthBearer}, &credential); !errors.Is(err, ErrCheckinSiteArchived) {
		t.Fatalf("created account on archived site: %v", err)
	}
	if creds, err := store.ListCredentials(ctx); err != nil || len(creds) != 1 {
		t.Fatalf("archived site creation left an orphan credential: %v", err)
	}
	if !assertWorkflow().Enabled {
		t.Fatal("archive changed per-account enabled state")
	}
	if err := store.DeleteSite(ctx, site.ID); !errors.Is(err, ErrCheckinSiteInUse) {
		t.Fatalf("site deletion bypassed account protection: %v", err)
	}
	if err := store.DeleteAccount(ctx, account.ID); !errors.Is(err, ErrCheckinWorkflowPending) {
		t.Fatalf("submitted workflow deleted: %v", err)
	}
	if _, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	restored, err := store.RestoreSite(ctx, site.ID)
	if err != nil || restored.Archived || restored.ArchivedAt != nil || restored.Enabled {
		t.Fatalf("restore silently enabled a disabled site: %v", err)
	}
	if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"enabled":false,"auto_checkin":false,"random_checkin":false,"checkin_random_window_minutes":0}`)); err != nil {
		t.Fatal(err)
	}
	paused := assertWorkflow()
	if paused.Enabled || paused.AutoCheckin || paused.RandomCheckin || paused.CheckinRandomWindowMinutes != 0 {
		t.Fatal("false or zero account settings lost")
	}
	// Simulate completion by the future execution layer, then remove configuration.
	account.CheckinPending.Finished = true
	if err := store.conn.Select("checkin_pending").Updates(account).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAccount(ctx, account.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("account was not deleted: %v", err)
	}
	if _, err := store.GetCredential(ctx, account.CredentialID); err != nil {
		t.Fatalf("account deletion cascaded into shared credentials: %v", err)
	}
	if err := store.DeleteSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentCheckinEditingReferencesAndRollback(t *testing.T) {
	store, ctx := independentCheckinStore(t), context.Background()
	site := createIndependentSite(t, store)
	credential := testCheckinCredential()
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "one", AuthMode: model.CheckinAuthBearer}, &credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"name":"must rollback","checkin_http_enabled":false}`)); err == nil {
		t.Fatal("site edit broke the existing account authentication mode")
	}
	storedSite, err := store.GetSite(ctx, site.ID)
	if err != nil || storedSite.Name != site.Name || !storedSite.CheckinHTTPEnabled {
		t.Fatalf("invalid site edit partially committed: %v", err)
	}
	if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"name":"must rollback","credential_id":999}`)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing credential accepted: %v", err)
	}
	storedAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil || storedAccount.Name != account.Name || storedAccount.CredentialID != account.CredentialID {
		t.Fatalf("invalid account edit partially committed: %v", err)
	}
	custom := model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: model.SitePlatformSub2API,
		BaseURL: credential.AuthBaseURL, CredentialID: &account.CredentialID}
	if _, err := store.PatchAccount(ctx, account.ID, model.CheckinAccountPatch{BalanceConfig: model.CheckinPatchField[*model.CheckinBalanceConfig]{Present: true, Value: &custom}}); err != nil {
		t.Fatal(err)
	}
	checkSummary := func(balanceRefs int) {
		t.Helper()
		one, err := store.GetCredentialSummary(ctx, account.CredentialID)
		if err != nil || one.PrimaryReferenceCount != 1 || one.BalanceReferenceCount != balanceRefs || one.ReferenceCount != 1+balanceRefs || !one.HasAccessToken || !one.Configured {
			t.Fatalf("summary counts/configuration incorrect: %+v %v", one, err)
		}
		all, err := store.ListCredentialSummaries(ctx)
		if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], *one) {
			t.Fatalf("list summary disagreed with detail: %v", err)
		}
	}
	checkSummary(1)
	if _, err := store.ArchiveSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	checkSummary(1)
	if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"balance_config":null}`)); err != nil {
		t.Fatal(err)
	}
	checkSummary(0)
	if accounts, err := store.ListAccounts(ctx, site.ID); err != nil || len(accounts) != 1 || accounts[0].BalanceConfig.Mode != model.CheckinBalanceDisabled {
		t.Fatalf("balance reference not cleared: %v", err)
	}
}

func TestIndependentCheckinCredentialPatchesPreserveRotatedSecrets(t *testing.T) {
	store, ctx := independentCheckinStore(t), context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchCredential(ctx, credential.ID, checkinPatch[model.CheckinCredentialPatch](t, `{"name":"no version"}`)); err == nil {
		t.Fatal("patch without expected version accepted")
	}
	claim, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	patch := checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":0,"name":"renamed","proxy_mode":"system"}`)
	updated, err := store.PatchCredential(ctx, credential.ID, patch)
	if err != nil || updated.AccessToken != credential.AccessToken || updated.RefreshToken != credential.RefreshToken || updated.TokenRefreshLease != claim.Lease {
		t.Fatalf("patch lost omitted secrets or refresh ownership: %v", err)
	}
	pair := CheckinTokenPair{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", TokenExpiresAt: 12345}
	if _, err := store.CommitRefresh(ctx, *claim, pair); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchCredential(ctx, credential.ID, patch); !errors.Is(err, ErrCheckinCredentialConflict) {
		t.Fatalf("stale patch overwrote rotated tokens: %v", err)
	}
	updated, err = store.PatchCredential(ctx, credential.ID, checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":2,"name":"after refresh","platform_user_id":null,"auth_headers":null}`))
	if err != nil || updated.AccessToken != pair.AccessToken || updated.RefreshToken != pair.RefreshToken || updated.TokenExpiresAt != pair.TokenExpiresAt || updated.ProxyMode != model.ProxyUsageModeSystem {
		t.Fatalf("patch failed to merge the current stored session: %v", err)
	}
	if _, err := store.PatchCredential(ctx, credential.ID, checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":3,"access_token":""}`)); err == nil {
		t.Fatal("required credential secret was cleared")
	}
	updated, err = store.PatchCredential(ctx, credential.ID, checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":3,"refresh_token":""}`))
	if err != nil || updated.RefreshToken != "" || updated.AccessToken != pair.AccessToken || updated.IdentityRevision != 2 {
		t.Fatalf("explicit optional secret removal failed: %v", err)
	}
}

func TestIndependentCheckinConcurrentCredentialPatchesConflict(t *testing.T) {
	store, ctx := independentCheckinStore(t), context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	patches := []model.CheckinCredentialPatch{
		checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":0,"name":"one"}`),
		checkinPatch[model.CheckinCredentialPatch](t, `{"expected_version":0,"refresh_token":"manual-refresh"}`),
	}
	results := make(chan error, len(patches))
	var wg sync.WaitGroup
	for _, patch := range patches {
		wg.Go(func() { _, err := store.PatchCredential(ctx, credential.ID, patch); results <- err })
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCheckinCredentialConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent edits: successes=%d conflicts=%d", successes, conflicts)
	}
}
