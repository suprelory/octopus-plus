package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestBackupRetiredSettings(t *testing.T) {
	active := model.Setting{Key: model.SettingKeySyncLLMInterval, Value: "6"}
	retired := []model.Setting{
		{Key: model.SettingKeySiteSyncInterval, Value: "12"},
		{Key: model.SettingKeyProjectedChannelAutoGroupEnabled, Value: "2"},
	}
	for _, version := range []int{1, Version} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s := testService(t)
			d := &Document{LegacyDBDump: model.LegacyDBDump{Version: version, ExportedAt: time.Now().UTC(),
				Settings: []model.Setting{retired[0], active, retired[1]}}}
			if got := requireImport(t, s, d).RowsAffected["settings"]; got != 1 {
				t.Fatalf("restored %d settings, want 1", got)
			}
			var stored []model.Setting
			if err := s.conn.Find(&stored).Error; err != nil || len(stored) != 1 || stored[0] != active {
				t.Fatalf("ordinary setting was not restored on its own: %v", err)
			}
			if !requireImport(t, s, d).AlreadyImported {
				t.Fatal("restoring the same input twice changed its identity")
			}
			// Migrated databases may still hold retired rows on disk.
			if err := s.conn.Create(&retired).Error; err != nil {
				t.Fatal(err)
			}
			exported, err := s.Export(t.Context(), false, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(exported.Settings) != 1 || exported.Settings[0] != active {
				t.Fatal("export did not preserve just the active setting")
			}
		})
	}
}

func legacyFixture() *Document {
	now := time.Now().UTC().Truncate(time.Second)
	site := model.LegacySite{ID: 3, Name: "legacy cookie site", Kind: model.LegacySiteKindCheckin, Platform: model.SitePlatformNewAPI, BaseURL: "https://legacy.example.test", Enabled: true,
		ProxyMode: model.ProxyUsageModeDirect, CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/daily", CheckinMode: model.SiteCheckinModeEnabled,
		CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"}
	account := model.LegacySiteAccount{ID: 7, SiteID: site.ID, Name: "legacy account", Cookie: "session=legacy-fixture-secret", Enabled: true, ProxyMode: model.ProxyUsageModeInherit}
	account.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740993, Submitted: true, Date: now.Format("2006-01-02"), Status: model.SiteExecutionStatusPending,
		Source: model.CheckinTriggerManual, StartedAt: now, Deadline: now.Add(time.Hour), Fingerprint: site.CheckinWorkflowFingerprint(&account)}
	return &Document{LegacyDBDump: model.LegacyDBDump{Version: 1, ExportedAt: now, IncludeLogs: true, IncludeStats: true,
		Sites: []model.LegacySite{site}, SiteAccounts: []model.LegacySiteAccount{account},
		SiteCheckinLogs:      []model.CheckinLog{{ID: account.CheckinPending.LogID, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name, Status: model.SiteExecutionStatusPending, Source: "manual", StartedAt: now, FinishedAt: now}},
		RelayLogs:            []model.RelayLog{{ID: 42, ChannelId: 999, ChannelName: "deleted channel"}},
		RelayRequestContents: []model.RelayRequestContent{{LogID: 42, RequestBody: []byte("legacy request")}},
		StatsSiteModelHourly: []model.LegacyStatsSiteModelHourly{{Hour: 4, SiteAccountID: 200, GroupKey: "archived", ModelName: "old-model", Date: "20261121", StatsMetrics: model.StatsMetrics{RequestSuccess: 17}}}}}
}

// Emit the version 1 archive layout, whose manifest has no table inventory and
// whose historical ZIP exporter omitted site_checkin_batch_jobs altogether.
func legacyZIP(t *testing.T, d *Document, brokenRelay bool) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	entry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(entry).Encode(manifest{Version: 1, ExportedAt: d.ExportedAt, IncludeLogs: d.IncludeLogs, IncludeStats: d.IncludeStats, Format: "zip-v1"}); err != nil {
		t.Fatal(err)
	}
	for name, rows := range tables(d) {
		if name == "site_checkin_batch_jobs" {
			continue
		}
		filename := name + ".json"
		if name == "relay_logs" {
			filename = name + ".ndjson"
		}
		entry, err := writer.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		if name == "relay_logs" {
			for _, row := range d.RelayLogs {
				if err := json.NewEncoder(entry).Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			if brokenRelay {
				fmt.Fprint(entry, "{broken")
			}
		} else if err := json.NewEncoder(entry).Encode(rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestBackupLegacyJSONAndZIP(t *testing.T) {
	for _, format := range []string{"json", "zip", "envelope"} {
		t.Run(format, func(t *testing.T) {
			d := legacyFixture()
			var encoded []byte
			var err error
			switch format {
			case "zip":
				encoded = legacyZIP(t, d, false)
			case "envelope":
				encoded, err = json.Marshal(map[string]any{"code": 200, "data": d})
			default:
				encoded, err = json.Marshal(d)
			}
			if err != nil {
				t.Fatal(err)
			}
			document, err := Read(context.Background(), bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			defer document.Close()
			s := testService(t)
			requireImport(t, s, document)
			var account model.CheckinAccount
			if err := s.conn.First(&account).Error; err != nil || account.CheckinPending == nil || !account.CheckinPending.Submitted {
				t.Fatal("legacy submission lost")
			}
			store := op.NewCheckinStore(s.conn)
			execution, err := store.BeginWorkflow(context.Background(), account.ID, model.CheckinTriggerManual, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CheckWorkflow(context.Background(), execution); err != nil {
				t.Fatalf("legacy workflow evidence changed: %v", err)
			}
			var archived ArchivedSiteStat
			if err := s.conn.First(&archived).Error; err != nil || archived.Stats.RequestSuccess != 17 || archived.Stats.SiteAccountID != 200 || len(archived.Source) != 64 {
				t.Fatal("legacy statistics archive lost")
			}
			var content model.RelayRequestContent
			if err := s.conn.First(&content).Error; err != nil || string(content.RequestBody) != "legacy request" {
				t.Fatal("legacy relay history lost")
			}
			if result := requireImport(t, s, document); !result.AlreadyImported {
				t.Fatal("legacy backup not deduplicated")
			}
			for _, table := range []string{"sites", "site_accounts", "site_channel_bindings"} {
				if s.conn.Migrator().HasTable(table) {
					t.Fatal("legacy entities recreated")
				}
			}
		})
	}
}

func TestBackupDamagedDeferredHistoryDoesNotWrite(t *testing.T) {
	d, err := Read(context.Background(), bytes.NewReader(legacyZIP(t, legacyFixture(), true)))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := testService(t)
	if _, err := s.Import(context.Background(), d); err == nil {
		t.Fatal("damaged history accepted")
	}
	var count int64
	if err := s.conn.Model(&model.CheckinSite{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("configuration committed before history validation")
	}
}

func TestLegacyBackupImportFingerprint(t *testing.T) {
	// Include ordinary, subscription, archive and history fields. Their wire
	// order participates in the identity stored by earlier importers.
	const input = `{"version":1,"exported_at":"2026-11-20T06:00:00Z","include_logs":true,"include_stats":true,"channels":[{"id":9}],"sites":[{"id":7}],"groups":[{"id":5}],"stats_site_model_hourly":[{"site_account_id":3}],"site_checkin_logs":[{"id":9007199254740993}]}`
	d, err := Read(context.Background(), bytes.NewBufferString(input))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := fingerprint(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if want := "3d1c442855e8bab83e3456ca85a3c7c4dd6868837e0a2dac121cdab8ee0f5a11"; got != want {
		t.Fatalf("backup import fingerprint = %s, want %s", got, want)
	}
}
