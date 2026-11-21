package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testService(t *testing.T) *Service {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "backup.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	prepareOrdinaryTables(t, conn)
	service := NewService(conn)
	if err := service.PrepareSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return service
}

func prepareOrdinaryTables(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, table := range []any{&model.Channel{}, &model.ChannelKey{}, &model.ProxyConfiguration{}, &model.Group{}, &model.GroupItem{}, &model.GroupPreset{}, &model.LLMInfo{}, &model.APIKey{}, &model.Setting{}, &model.StatsTotal{}, &model.StatsDaily{}, &model.StatsHourly{}, &model.StatsChannel{}, &model.StatsAPIKey{}, &model.RelayLog{}, &model.RelayRequestContent{}, &model.RelayAttemptContent{}} {
		if err := conn.Migrator().CreateTable(table); err != nil {
			t.Fatal(err)
		}
	}
}

func backupFixture() *Document {
	now := time.Now().UTC().Truncate(time.Second)
	proxyID := 7
	site := model.CheckinSite{ID: 11, Name: "check-in", Platform: model.SitePlatformNewAPI, BaseURL: "https://example.test", Enabled: true,
		CheckinNetwork:     model.CheckinNetwork{ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &proxyID},
		CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/checkin", CheckinMode: model.SiteCheckinModeEnabled,
		CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"}
	credential := model.CheckinCredential{ID: 21, Name: "shared-cookie", Type: model.CheckinCredentialCookie, Platform: model.SitePlatformNewAPI,
		AuthBaseURL: site.BaseURL, CheckinNetwork: site.CheckinNetwork, Cookie: "session=backup-fixture-secret", CredentialVersion: 5, IdentityRevision: 2}
	account := model.CheckinAccount{ID: 31, SiteID: site.ID, Name: "pending", CredentialID: credential.ID, AuthMode: model.CheckinAuthCookie, Enabled: true,
		CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModeInherit}, BalanceConfig: model.CheckinBalanceConfig{Mode: model.CheckinBalanceDisabled}, Balance: 12.5}
	account.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740993, BatchJobID: 9007199254740995, Submitted: true, Date: now.Format("2006-01-02"), Status: model.SiteExecutionStatusPending,
		StartedAt: now, Deadline: now.Add(time.Hour), Source: model.CheckinTriggerManual, Fingerprint: site.WorkflowFingerprint(account, credential)}
	second := account
	second.ID, second.Name, second.CheckinPending = 32, "finished", nil
	entry := model.CheckinLog{ID: 9007199254740994, SiteID: site.ID, AccountID: second.ID, SiteName: site.Name, AccountName: second.Name,
		Platform: site.Platform, Source: string(model.CheckinTriggerScheduled), Status: model.SiteExecutionStatusSuccess, Reason: "checked_in", Reward: "0", StartedAt: now, FinishedAt: now}
	completion := model.CheckinCompletion{ID: fmt.Sprintf("%d:success", entry.ID), LogID: entry.ID, Entry: entry, Status: entry.Status, Expanded: true, ReadyAt: now.UnixMilli()}
	destination := strings.Repeat("a", 64)
	key := cooldownKey(destination, second.ID, "site_checkin_success", entry.Reason)
	return &Document{DBDump: model.DBDump{Version: Version, ExportedAt: now, IncludeLogs: true, IncludeStats: true,
		ProxyConfigurations:  []model.ProxyConfiguration{{ID: proxyID, Name: "proxy", URL: "socks5h://127.0.0.1:1080", Enabled: false}},
		Channels:             []model.Channel{{ID: 41, Name: "disabled-channel", Type: outbound.OutboundTypeOpenAIEmbedding, Enabled: false, ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &proxyID}},
		ChannelKeys:          []model.ChannelKey{{ID: 51, ChannelID: 41, ChannelKey: "channel-fixture-secret", Enabled: false}},
		Groups:               []model.Group{{ID: 61, Name: "group", Mode: model.GroupModeRoundRobin}},
		GroupItems:           []model.GroupItem{{ID: 71, GroupID: 61, ChannelID: 41, ModelName: "embedding"}},
		StatsChannel:         []model.StatsChannel{{ChannelID: 41, StatsMetrics: model.StatsMetrics{RequestSuccess: 9}}},
		SiteCheckinLogs:      []model.CheckinLog{{ID: account.CheckinPending.LogID, SiteID: site.ID, AccountID: account.ID, Status: model.SiteExecutionStatusPending, BatchJobID: account.CheckinPending.BatchJobID, StartedAt: now, FinishedAt: now}, entry},
		SiteCheckinBatchJobs: []model.CheckinBatchJob{{ID: account.CheckinPending.BatchJobID, Status: model.SiteCheckinBatchJobStatusRunning, SiteIDs: []int{site.ID, 999}, StartedAt: now, UpdatedAt: now}},
		RelayLogs:            []model.RelayLog{{ID: 9901, ChannelId: 41, Attempts: []model.ChannelAttempt{{ChannelID: 41, ChannelKeyID: 51}}, Success: true}},
		RelayRequestContents: []model.RelayRequestContent{{LogID: 9901, RequestBody: []byte("request fixture")}}},
		CheckinSites:       []Site{{CheckinSite: site, VerificationFingerprint: site.ConfigFingerprint()}},
		CheckinCredentials: []model.CheckinCredentialBackup{model.ExportCheckinCredential(credential)},
		CheckinAccounts:    []Account{{CheckinAccount: account, WorkflowVersion: 8, BalanceVersion: 3, BalanceConfigVersion: 2}, {CheckinAccount: second}},
		CheckinCompletions: []model.CheckinCompletion{completion},
		CheckinDeliveries:  []Delivery{{CheckinNotificationDelivery: model.CheckinNotificationDelivery{ID: 81, CompletionID: completion.ID, Destination: destination, Event: "site_checkin_success", State: "sending", Attempts: 1, StartedAt: now.UnixMilli()}, CooldownKey: key, CooldownMS: 60000}},
		CheckinCooldowns:   []Cooldown{{Key: key, Until: now.Add(time.Hour).UnixMilli(), AccountID: second.ID, Destination: destination, Event: "site_checkin_success", Reason: entry.Reason}}}
}

func requireImport(t *testing.T, s *Service, d *Document) *model.DBImportResult {
	t.Helper()
	result, err := s.Import(context.Background(), d)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	return result
}

func TestBackupRoundtrip(t *testing.T) {
	for _, format := range []string{"json", "zip"} {
		for _, logs := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/logs=%v", format, logs), func(t *testing.T) {
				source := testService(t)
				requireImport(t, source, backupFixture())
				// Export must preserve an unresolved session while excluding lock ownership.
				if err := source.conn.Model(&model.CheckinCredential{}).Where("name = ?", "shared-cookie").Updates(map[string]any{"token_refresh_started": true, "token_refresh_lease": "private-lease", "token_refresh_lease_until": 123, "token_refresh_retry_at": 456}).Error; err != nil {
					t.Fatal(err)
				}
				var encoded bytes.Buffer
				if format == "zip" {
					if err := source.ExportZIP(context.Background(), &encoded, logs, true); err != nil {
						t.Fatal(err)
					}
				} else {
					d, err := source.Export(context.Background(), logs, true)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.NewEncoder(&encoded).Encode(d); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(encoded.String(), "private-lease") || strings.Contains(encoded.String(), "token_refresh_retry_at") {
						t.Fatal("runtime ownership exported")
					}
				}
				document, err := Read(context.Background(), &encoded)
				if err != nil {
					t.Fatal(err)
				}
				defer document.Close()
				target := testService(t)
				// Existing unrelated IDs force foreign keys and fingerprints to change.
				if err := target.conn.Create(&model.CheckinSite{ID: 100, Name: "unrelated"}).Error; err != nil {
					t.Fatal(err)
				}
				requireImport(t, target, document)
				assertRestored(t, target, logs)
				var credential model.CheckinCredential
				if err := target.conn.First(&credential).Error; err != nil {
					t.Fatal(err)
				}
				if !credential.TokenRefreshStarted || credential.TokenRefreshLease != "" || credential.TokenRefreshRetryAt != 0 || credential.TokenRefreshLeaseUntil != 0 {
					t.Fatal("session ownership was restored")
				}
				if err := target.conn.Model(&credential).Updates(map[string]any{"name": "manually edited", "cookie": "session=new", "credential_version": 6}).Error; err != nil {
					t.Fatal(err)
				}
				if result := requireImport(t, target, document); !result.AlreadyImported || len(result.RowsAffected) != 0 {
					t.Fatal("repeat import wrote data")
				}
				if err := target.conn.First(&credential, credential.ID).Error; err != nil || credential.Cookie != "session=new" || credential.Name != "manually edited" {
					t.Fatal("repeat import overwrote edits")
				}
				if document.temporary != nil {
					path := document.temporary.Name()
					if err := document.Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("archive spool remains")
					}
				}
			})
		}
	}
}

func assertRestored(t *testing.T, s *Service, includeLogs bool) {
	t.Helper()
	var accounts []model.CheckinAccount
	if err := s.conn.Order("id ASC").Find(&accounts).Error; err != nil || len(accounts) != 2 {
		t.Fatal("accounts lost")
	}
	if accounts[0].CredentialID != accounts[1].CredentialID || accounts[0].CheckinWorkflowVersion != 8 || accounts[0].BalanceVersion != 3 {
		t.Fatal("shared identity or versions changed")
	}
	var site model.CheckinSite
	var credential model.CheckinCredential
	s.conn.First(&site, accounts[0].SiteID)
	s.conn.First(&credential, accounts[0].CredentialID)
	if accounts[0].CheckinPending.LogID != 9007199254740993 || !accounts[0].CheckinPending.Submitted || accounts[0].CheckinPending.Fingerprint != site.WorkflowFingerprint(accounts[0], credential) {
		t.Fatal("workflow mapping lost")
	}
	if site.VerificationFingerprint != site.ConfigFingerprint() {
		t.Fatal("site evidence lost on proxy remapping")
	}
	store := op.NewCheckinStore(s.conn)
	execution, err := store.BeginWorkflow(context.Background(), accounts[0].ID, model.CheckinTriggerManual, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveWorkflowSubmission(context.Background(), execution, true); !errors.Is(err, op.ErrCheckinWorkflowConflict) {
		t.Fatalf("submitted workflow can resubmit: %v", err)
	}
	var delivery model.CheckinNotificationDelivery
	s.conn.First(&delivery)
	if delivery.State != "uncertain" || delivery.CooldownKey != cooldownKey(delivery.Destination, accounts[1].ID, delivery.Event, "checked_in") {
		t.Fatal("notification remapping/state lost")
	}
	if queued, err := store.PendingNotifications(context.Background()); err != nil || len(queued) != 0 {
		t.Fatal("uncertain notification requeued")
	}
	var cooldown model.CheckinNotificationCooldown
	if err := s.conn.Where(map[string]any{"key": delivery.CooldownKey}).First(&cooldown).Error; err != nil || cooldown.Until <= time.Now().UnixMilli() {
		t.Fatal("cooldown lost")
	}
	var channel model.Channel
	var key model.ChannelKey
	var proxy model.ProxyConfiguration
	s.conn.First(&channel)
	s.conn.First(&key)
	s.conn.First(&proxy)
	if channel.Enabled || key.Enabled || proxy.Enabled || key.ChannelID != channel.ID {
		t.Fatal("disabled configuration enabled or key mapping lost")
	}
	var job model.CheckinBatchJob
	s.conn.First(&job)
	if job.Status != model.SiteCheckinBatchJobStatusInterrupted || len(job.SiteIDs) != 2 || job.SiteIDs[0] != site.ID || job.SiteIDs[1] != 0 {
		t.Fatal("batch scope/state changed")
	}
	var entry model.CheckinLog
	if err := s.conn.First(&entry, 9007199254740994).Error; err != nil || entry.Reward != "0" {
		t.Fatal("known zero reward lost")
	}
	var relayCount int64
	s.conn.Model(&model.RelayLog{}).Count(&relayCount)
	if includeLogs && relayCount != 1 || !includeLogs && relayCount != 0 {
		t.Fatal("relay history option changed")
	}
	if includeLogs {
		var log model.RelayLog
		s.conn.First(&log)
		if log.ChannelId != channel.ID || log.Attempts[0].ChannelKeyID != key.ID {
			t.Fatal("relay references not remapped")
		}
	}
	for _, table := range []string{"sites", "site_accounts", "site_channel_bindings"} {
		if s.conn.Migrator().HasTable(table) {
			t.Fatalf("requires subscription table %s", table)
		}
	}
}

func TestBackupRollbackAndConcurrentRetry(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	d.CheckinDeliveries[0].State = "invalid"
	if _, err := s.Import(context.Background(), d); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid delivery accepted: %v", err)
	}
	for _, table := range []any{&model.Channel{}, &model.CheckinSite{}, &model.CheckinCredential{}, &model.CheckinAccount{}, &model.CheckinLog{}, &importRun{}, &importMapping{}} {
		var count int64
		if err := s.conn.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partial writes in %T", table)
		}
	}
	d.CheckinDeliveries[0].State = "sending"
	var group sync.WaitGroup
	results := make(chan *model.DBImportResult, 2)
	errors := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := s.Import(context.Background(), d)
			results <- result
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	repeats := 0
	for result := range results {
		if result.AlreadyImported {
			repeats++
		}
	}
	if repeats != 1 {
		t.Fatal("concurrent import repeated writes")
	}
}

func TestBackupInvalidFingerprintAndMissingHistory(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	d.CheckinAccounts[0].CheckinPending.Fingerprint = "old-invalid-evidence"
	d.SiteCheckinLogs = d.SiteCheckinLogs[1:]
	d.IncludeLogs = false
	result := requireImport(t, s, d)
	if len(result.Issues) == 0 {
		t.Fatal("rebuilt log not reported")
	}
	var account model.CheckinAccount
	s.conn.Where("name = ?", "pending").First(&account)
	store := op.NewCheckinStore(s.conn)
	execution, err := store.BeginWorkflow(context.Background(), account.ID, model.CheckinTriggerManual, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckWorkflow(context.Background(), execution); !errors.Is(err, op.ErrCheckinWorkflowChanged) {
		t.Fatal("invalid evidence reauthorized")
	}
}

func TestBackupRejectsAmbiguousConflicts(t *testing.T) {
	for _, conflict := range []string{"name", "history", "proxy"} {
		t.Run(conflict, func(t *testing.T) {
			s := testService(t)
			d := backupFixture()
			switch conflict {
			case "name":
				s.conn.Create(&model.CheckinSite{Name: d.CheckinSites[0].Name})
			case "history":
				s.conn.Create(&model.CheckinLog{ID: d.SiteCheckinLogs[0].ID})
			case "proxy":
				d.CheckinSites[0].ProxyConfigID = new(int)
				*d.CheckinSites[0].ProxyConfigID = 999
			}
			if _, err := s.Import(context.Background(), d); err == nil {
				t.Fatal("conflict accepted")
			}
			var channels int64
			s.conn.Model(&model.Channel{}).Count(&channels)
			if channels != 0 {
				t.Fatal("conflict left partial configuration")
			}
		})
	}
}

func TestBackupRejectsDamagedArchive(t *testing.T) {
	for _, names := range [][]string{{"../manifest.json"}, {"manifest.json", "manifest.json"}, {"manifest.json", "channels.json", "channels.ndjson"}} {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		for _, name := range names {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			data := "[]"
			if strings.Contains(name, "manifest") {
				data = `{"version":1,"format":"zip-v1"}`
			}
			fmt.Fprint(entry, data)
		}
		writer.Close()
		if d, err := Read(context.Background(), &buffer); err == nil {
			d.Close()
			t.Fatal("damaged archive accepted")
		}
	}
}

func TestBackupDuplicateCredentialNamesRemainIndependent(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	second := d.CheckinCredentials[0]
	second.ID, second.Cookie = 22, "session=another-identity"
	d.CheckinCredentials = append(d.CheckinCredentials, second)
	d.CheckinAccounts[1].CredentialID = second.ID
	requireImport(t, s, d)
	var accounts []model.CheckinAccount
	if err := s.conn.Order("id ASC").Find(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].CredentialID == accounts[1].CredentialID {
		t.Fatal("same-name credentials were merged")
	}
	var credentials []model.CheckinCredential
	if err := s.conn.Find(&credentials).Error; err != nil || len(credentials) != 2 || credentials[0].Name != credentials[1].Name {
		t.Fatal("duplicate display names lost")
	}
}

func TestBackupPreservesMissingCredentialAccount(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	d.CheckinAccounts[0].CredentialID = 0
	d.CheckinAccounts[0].AutoCheckin = true
	requireImport(t, s, d)
	var account model.CheckinAccount
	if err := s.conn.Where("name = ?", "pending").First(&account).Error; err != nil {
		t.Fatal(err)
	}
	if account.CredentialID != 0 || account.AutoCheckin || account.NextAutoCheckinAt != nil || account.CheckinPending == nil || !account.CheckinPending.Submitted {
		t.Fatal("orphan account lost submission or resumed execution")
	}
}

func TestBackupNotificationSurvivesPrunedHistory(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	d.SiteCheckinLogs = d.SiteCheckinLogs[:1]
	result := requireImport(t, s, d)
	if len(result.Issues) != 1 || result.Issues[0].Code != "notification_history_rebuilt" {
		t.Fatal("reconstructed history not reported")
	}
	var entry model.CheckinLog
	if err := s.conn.First(&entry, d.CheckinCompletions[0].LogID).Error; err != nil || entry.Reward != "0" {
		t.Fatal("completion snapshot lost its known reward")
	}
	var delivery model.CheckinNotificationDelivery
	if err := s.conn.First(&delivery).Error; err != nil || delivery.State != "uncertain" {
		t.Fatal("restored event can resend")
	}
}

func TestBackupProxyEnablementConflictRollsBack(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	existing := d.ProxyConfigurations[0]
	existing.ID, existing.Enabled = 0, true
	if err := s.conn.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(context.Background(), d); !errors.Is(err, ErrConflict) {
		t.Fatalf("different proxy state accepted: %v", err)
	}
	var accounts int64
	if err := s.conn.Model(&model.CheckinAccount{}).Count(&accounts).Error; err != nil || accounts != 0 {
		t.Fatal("proxy conflict partially imported accounts")
	}
}

func TestBackupArchiveStreamsRelayRows(t *testing.T) {
	s := testService(t)
	d := backupFixture()
	d.RelayRequestContents = nil
	d.RelayLogs = nil
	for i := 1; i <= 1005; i++ {
		d.RelayLogs = append(d.RelayLogs, model.RelayLog{ID: int64(i), ChannelId: 41, Success: true})
	}
	requireImport(t, s, d)
	var archive bytes.Buffer
	if err := s.ExportZIP(context.Background(), &archive, true, false); err != nil {
		t.Fatal(err)
	}
	read, err := Read(context.Background(), &archive)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if len(read.RelayLogs) != 0 || read.archive["relay_logs"] == nil {
		t.Fatal("ZIP reader materialized relay history")
	}
	target := testService(t)
	requireImport(t, target, read)
	var count int64
	if err := target.conn.Model(&model.RelayLog{}).Count(&count).Error; err != nil || count != 1005 {
		t.Fatal("streamed history lost rows")
	}
}

func TestBackupOrphanedChannelStatsDoNotAttachToExistingIDs(t *testing.T) {
	s := testService(t)
	unrelated := model.Channel{ID: 999, Name: "unrelated-target", Type: outbound.OutboundTypeOpenAIEmbedding}
	if err := s.conn.Create(&unrelated).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.conn.Create(&model.StatsChannel{ChannelID: unrelated.ID, StatsMetrics: model.StatsMetrics{RequestSuccess: 7}}).Error; err != nil {
		t.Fatal(err)
	}
	d := backupFixture()
	d.StatsChannel = append(d.StatsChannel, model.StatsChannel{ChannelID: unrelated.ID, StatsMetrics: model.StatsMetrics{RequestSuccess: 99}})
	d.StatsModel = []model.StatsModel{{ID: 1, Name: "retired-model", ChannelID: unrelated.ID, StatsMetrics: model.StatsMetrics{RequestSuccess: 99}}}
	result := requireImport(t, s, d)
	var saved model.StatsChannel
	if err := s.conn.First(&saved, "channel_id = ?", unrelated.ID).Error; err != nil || saved.RequestSuccess != 7 {
		t.Fatal("orphaned source statistics overwrote an unrelated target channel")
	}
	var modelCount int64
	if err := s.conn.Model(&model.StatsModel{}).Count(&modelCount).Error; err != nil || modelCount != 0 {
		t.Fatal("orphaned model statistics attached to an unrelated target channel")
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Code == "channel_statistics_owner_missing" && issue.Entity == "channel" && issue.ID == unrelated.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("missing statistics owner was not reported")
	}
}

func TestBackupCustomBalanceKeepsCredentialAndProxyMapping(t *testing.T) {
	for _, compatible := range []bool{true, false} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			s := testService(t)
			d := backupFixture()
			credential := model.CheckinCredential{ID: 22, Name: "balance", Platform: model.SitePlatformSub2API, Type: model.CheckinCredentialAPIKey,
				AuthBaseURL: "https://balance.example.test", APIKey: "balance-test-secret", CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModeDirect}}
			d.CheckinCredentials = append(d.CheckinCredentials, model.ExportCheckinCredential(credential))
			proxyID := 7
			d.CheckinAccounts[0].BalanceConfig = model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: credential.Platform, BaseURL: credential.AuthBaseURL, CredentialID: &credential.ID,
				CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &proxyID}}
			if !compatible {
				d.CheckinAccounts[0].BalanceConfig.Platform = model.SitePlatformNewAPI
			}
			_, err := s.Import(context.Background(), d)
			if !compatible {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("incompatible balance credential accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var account model.CheckinAccount
			if err := s.conn.Where("name = ?", "pending").First(&account).Error; err != nil {
				t.Fatal(err)
			}
			var saved model.CheckinCredential
			if err := s.conn.First(&saved, *account.BalanceConfig.CredentialID).Error; err != nil || saved.APIKey != credential.APIKey || saved.ID == account.CredentialID {
				t.Fatal("balance credential mapping lost")
			}
			var proxy model.ProxyConfiguration
			if err := s.conn.First(&proxy, *account.BalanceConfig.ProxyConfigID).Error; err != nil || proxy.URL != d.ProxyConfigurations[0].URL {
				t.Fatal("balance proxy mapping lost")
			}
		})
	}
}
