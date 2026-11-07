package op

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCheckinHistoryBackupRoundTripRemapsAccounts(t *testing.T) {
	ctx := setupBackupTestDB(t)
	site, account := createSiteOpTestSiteAccount(t, ctx, "History site", "History account")
	rows := []model.SiteCheckinLog{
		{ID: 100, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name, Source: "manual", Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "12.5", FinishedAt: time.Now().UTC()},
		{ID: 101, SiteID: 999, AccountID: 999, SiteName: "Deleted site", AccountName: "Deleted account", Source: "scheduled", Status: model.SiteExecutionStatusFailed, FinishedAt: time.Now().UTC()},
	}
	if err := db.GetDB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	without, err := DBExportAll(ctx, false, false)
	if err != nil || len(without.SiteCheckinLogs) != 0 {
		t.Fatalf("logs leaked into log-free backup: %v", err)
	}
	dump, err := DBExportAll(ctx, true, false)
	if err != nil || len(dump.SiteCheckinLogs) != 2 {
		t.Fatalf("logs missing from backup: %v", err)
	}
	encoded, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	var decoded model.DBDump
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := DBExportZip(ctx, &archive, true, false); err != nil {
		t.Fatal(err)
	}
	zr, err := zipReaderFromBytes(archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if linesCount(readZipFile(t, zr, "site_checkin_logs.ndjson")) != 2 {
		t.Fatal("ZIP backup lost history")
	}

	ctx = setupBackupTestDB(t)
	otherSite := &model.Site{Name: "Unrelated site", BaseURL: "https://unrelated.example", Platform: model.SitePlatformOneAPI}
	if err := SiteCreate(otherSite, ctx); err != nil {
		t.Fatal(err)
	}
	otherAccount := &model.SiteAccount{SiteID: otherSite.ID, Name: "Unrelated account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "other"}
	if err := SiteAccountCreate(otherAccount, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := DBImportIncremental(ctx, &decoded); err != nil {
		t.Fatal(err)
	}
	page, err := SiteCheckinLogList(ctx, SiteCheckinLogFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("history not restored: %+v, %v", page, err)
	}
	deleted, restored := page.Items[0], page.Items[1]
	if deleted.SiteID != 0 || deleted.AccountID != 0 || deleted.SiteName != "Deleted site" {
		t.Fatalf("deleted account acquired an unrelated ID: %+v", deleted)
	}
	if restored.SiteID == otherSite.ID || restored.AccountID == otherAccount.ID || restored.Reward != "12.5" {
		t.Fatalf("history was not remapped: %+v", restored)
	}
	if saved, err := SiteAccountGet(restored.AccountID, ctx); err != nil || saved.Name != account.Name || saved.SiteID != restored.SiteID {
		t.Fatalf("history points to wrong account: %+v, %v", saved, err)
	}
	result, err := DBImportIncremental(ctx, &decoded)
	if err != nil || result.RowsAffected["site_checkin_logs"] != 0 {
		t.Fatalf("reimport duplicated history: %+v, %v", result, err)
	}
}
