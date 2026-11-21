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

func legacyFixture() *Document {
	now := time.Now().UTC().Truncate(time.Second)
	site := model.Site{ID: 3, Name: "legacy cookie site", Kind: model.SiteKindCheckin, Platform: model.SitePlatformNewAPI, BaseURL: "https://legacy.example.test", Enabled: true,
		ProxyMode: model.ProxyUsageModeDirect, CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/daily", CheckinMode: model.SiteCheckinModeEnabled,
		CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"}
	account := model.SiteAccount{ID: 7, SiteID: site.ID, Name: "legacy account", Cookie: "session=legacy-fixture-secret", Enabled: true, ProxyMode: model.ProxyUsageModeInherit}
	account.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740993, Submitted: true, Date: now.Format("2006-01-02"), Status: model.SiteExecutionStatusPending,
		Source: model.CheckinTriggerManual, StartedAt: now, Deadline: now.Add(time.Hour), Fingerprint: site.CheckinWorkflowFingerprint(&account)}
	return &Document{DBDump: model.DBDump{Version: 1, ExportedAt: now, IncludeLogs: true, IncludeStats: true,
		Sites: []model.Site{site}, SiteAccounts: []model.SiteAccount{account},
		SiteCheckinLogs:      []model.CheckinLog{{ID: account.CheckinPending.LogID, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name, Status: model.SiteExecutionStatusPending, Source: "manual", StartedAt: now, FinishedAt: now}},
		RelayLogs:            []model.RelayLog{{ID: 42, ChannelId: 999, ChannelName: "deleted channel"}},
		RelayRequestContents: []model.RelayRequestContent{{LogID: 42, RequestBody: []byte("legacy request")}},
		StatsSiteModelHourly: []model.StatsSiteModelHourly{{Hour: 4, SiteAccountID: 200, GroupKey: "archived", ModelName: "old-model", Date: "20261121", StatsMetrics: model.StatsMetrics{RequestSuccess: 17}}}}}
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
