package v2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func sqliteMigrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return conn
}

// Seed real v1 tables, including one log still owned by a pre-split account
// and one submitted workflow with an omitted log.
func seedMigrationDatabase(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, table := range []any{&model.LegacySite{}, &model.LegacySiteAccount{}, &model.Channel{}, &model.ChannelKey{}, &model.ProxyConfiguration{},
		&model.LegacySiteUserGroup{}, &model.LegacySiteChannelBinding{}, &model.Group{}, &model.GroupItem{}, &model.GroupPreset{}, &model.Setting{},
		&model.StatsChannel{}, &model.LegacyStatsSiteModelHourly{}, &model.CheckinLog{}, &model.CheckinBatchJob{}} {
		if err := conn.Migrator().CreateTable(table); err != nil {
			t.Fatalf("create %T: %v", table, err)
		}
	}
	f := conversionFixture()
	f.Configuration.Channels[0].Name, f.Configuration.Channels[1].Name = "projected", "ordinary"
	f.Configuration.Groups[0].Name = "chat"
	f.Configuration.SiteAccounts[1].CheckinSourceAccountID = id(10)
	f.Configuration.SiteAccounts[0].TokenRefreshLease = "test-refresh-owner"
	f.Configuration.SiteAccounts[0].TokenRefreshLeaseUntil = 1
	f.Configuration.SiteAccounts[0].TokenRefreshedAt = 1234
	f.Configuration.Groups[0].ActivePresetID = id(600)
	f.GroupPresets = []model.GroupPreset{{ID: 600, GroupID: 400, Name: "routes", Items: []model.GroupPresetItem{{ChannelID: 100, Weight: 7}}}}
	f.Configuration.GroupItems[0].Weight = 7
	f.ChannelStats = []model.StatsChannel{{ChannelID: 100, StatsMetrics: model.StatsMetrics{RequestSuccess: 5}}}
	f.ArchivedSiteStats = []model.LegacyStatsSiteModelHourly{{SiteAccountID: 10, GroupKey: "old", ModelName: "m", Hour: 3, Date: "20261120", StatsMetrics: model.StatsMetrics{RequestSuccess: 8}}}
	f.Logs = []model.CheckinLog{{ID: 9007199254740993, SiteID: 1, AccountID: 10, SiteName: "historical", AccountName: "old", Source: "manual", Status: model.SiteExecutionStatusSuccess, Reward: "0", StartedAt: f.CapturedAt, FinishedAt: f.CapturedAt},
		{ID: 9007199254740994, SiteID: 99, AccountID: 999, SiteName: "deleted", Status: model.SiteExecutionStatusSuccess, Reward: "2", StartedAt: f.CapturedAt, FinishedAt: f.CapturedAt}}
	f.Batches = []model.CheckinBatchJob{{ID: 31, SiteIDs: []int{1}, CurrentSiteID: 1, CurrentAccountID: 10, Status: model.SiteCheckinBatchJobStatusRunning, Attempted: 1, StartedAt: f.CapturedAt, UpdatedAt: f.CapturedAt}}
	a, s := &f.Configuration.SiteAccounts[2], &f.Configuration.Sites[2]
	// Use the persisted legacy defaults when computing the old evidence.
	s.CheckinMode = model.SiteCheckinModeAuto
	workflowStarted := f.CapturedAt.Add(123789123 * time.Nanosecond)
	a.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740999, Submitted: true, Date: "2026-11-20", Status: model.SiteExecutionStatusPending, StartedAt: workflowStarted, Deadline: workflowStarted.Add(time.Hour), Fingerprint: s.CheckinWorkflowFingerprint(a)}
	for _, rows := range []any{&f.Configuration.Sites, &f.Configuration.SiteAccounts, &f.Configuration.Channels, &f.Configuration.ChannelKeys, &f.Configuration.ProxyConfigurations, &f.Configuration.SiteChannelBindings, &f.Configuration.Groups, &f.Configuration.GroupItems, &f.GroupPresets, &f.Configuration.Settings, &f.ChannelStats, &f.ArchivedSiteStats, &f.Logs, &f.Batches} {
		if err := conn.Omit(clause.Associations).Create(rows).Error; err != nil {
			t.Fatalf("seed %T: %v", rows, err)
		}
	}
	for _, v := range []struct {
		row any
		id  int
		col string
	}{{&model.Channel{}, 100, "enabled"}, {&model.ChannelKey{}, 200, "enabled"}, {&model.LegacySiteAccount{}, 10, "auto_checkin"}} {
		if err := conn.Model(v.row).Where("id = ?", v.id).UpdateColumn(v.col, false).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDatabaseMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var conn *gorm.DB
			if dialect == "sqlite" {
				conn = sqliteMigrationDatabase(t)
			} else {
				env := "CHECKIN_TEST_" + strings.ToUpper(dialect) + "_DSN"
				if os.Getenv(env) == "" {
					t.Skip("set " + env + " for real database migration tests")
				}
				conn = openExternalDatabase(t, dialect, os.Getenv(env))
			}
			seedMigrationDatabase(t, conn)
			ctx := context.Background()
			input, err := CaptureDatabaseInput(ctx, conn)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "checkpoint.json")
			if err := WriteSnapshotFile(path, DatabaseSource, input); err != nil {
				t.Fatal(err)
			}
			if err := WriteSnapshotFile(path, DatabaseSource, input); err == nil {
				t.Fatal("overwrote checkpoint")
			}
			source, frozen, err := ReadSnapshotFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if source != DatabaseSource || frozen.Configuration.SiteAccounts[0].TokenRefreshLease != "test-refresh-owner" || frozen.Configuration.SiteAccounts[0].TokenRefreshedAt != 1234 {
				t.Fatal("checkpoint lost refresh state")
			}
			wantWorkflowStarted := frozen.Configuration.SiteAccounts[2].CheckinPending.StartedAt
			report, err := InspectDatabase(ctx, conn, source, frozen)
			if err != nil {
				t.Fatalf("preview: %v, %+v", err, report)
			}
			data, _ := json.Marshal(report)
			for _, secret := range []string{"test-refresh-owner", "source-refresh", "channel-secret", "proxy-secret"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("report exposed a secret")
				}
			}
			if conn.Migrator().HasTable(&MigrationRun{}) || conn.Migrator().HasTable(&model.CheckinAccount{}) {
				t.Fatal("preview created tables")
			}
			// Emulate a write failure in the history stage after its first update.
			fail := true
			if err := conn.Callback().Create().Before("gorm:create").Register("test:fail_restored_log", func(tx *gorm.DB) {
				if fail && tx.Statement.Table == "site_checkin_logs" {
					tx.AddError(errors.New("temporary failure with private values"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			report, err = MigrateDatabase(ctx, conn, source, frozen)
			if !errors.Is(err, ErrMigrationStageFailed) || report.NextStage != StageHistory {
				t.Fatalf("expected history rollback, got %v: %+v", err, report)
			}
			var historical model.CheckinLog
			if err := conn.First(&historical, int64(9007199254740993)).Error; err != nil || historical.AccountID != 10 {
				t.Fatal("partial history update committed")
			}
			fail = false
			if err := conn.Model(&model.CheckinCredential{}).Where("id = 10").UpdateColumn("name", "edited during interruption").Error; err != nil {
				t.Fatal(err)
			}
			// Reject a source-side change, even though the channel stage already committed.
			if err := conn.Model(&model.LegacySiteAccount{}).Where("id = 10").UpdateColumn("token_refresh_lease", "changed").Error; err != nil {
				t.Fatal(err)
			}
			if _, err := MigrateDatabase(ctx, conn, source, frozen); !errors.Is(err, ErrMigrationSourceChanged) {
				t.Fatalf("source drift accepted: %v", err)
			}
			if err := conn.Model(&model.LegacySiteAccount{}).Where("id = 10").UpdateColumn("token_refresh_lease", "test-refresh-owner").Error; err != nil {
				t.Fatal(err)
			}
			_, frozen, err = ReadSnapshotFile(path)
			if err != nil {
				t.Fatal(err)
			}
			report, err = MigrateDatabase(ctx, conn, source, frozen)
			if err != nil || !report.Completed {
				t.Fatalf("resume: %v, %+v", err, report)
			}
			report, err = MigrateDatabase(ctx, conn, source, frozen)
			if err != nil || !report.Completed {
				t.Fatalf("repeat: %v", err)
			}
			var credential model.CheckinCredential
			if err := conn.First(&credential, 10).Error; err != nil || credential.Name != "edited during interruption" || !credential.TokenRefreshStarted || credential.TokenRefreshLease != "" {
				t.Fatal("independent credential overwritten or old lease restored")
			}
			if err := conn.First(&historical, int64(9007199254740993)).Error; err != nil || historical.SiteID != 2 || historical.AccountID != 20 || historical.Reward != "0" || historical.SiteName != "historical" {
				t.Fatal("log ownership or reward lost")
			}
			var rebuilt model.CheckinLog
			expectedStarted := wantWorkflowStarted
			switch dialect {
			case "mysql":
				expectedStarted = expectedStarted.Round(time.Millisecond)
			case "postgres":
				expectedStarted = expectedStarted.Round(time.Microsecond)
			}
			if err := conn.First(&rebuilt, int64(9007199254740999)).Error; err != nil || !rebuilt.StartedAt.Equal(expectedStarted) {
				t.Fatalf("rebuilt workflow timestamp changed: %s, %v", rebuilt.StartedAt, err)
			}
			var channel model.Channel
			if err := conn.Preload("Keys").First(&channel, 100).Error; err != nil || channel.Enabled || channel.AutoSync || channel.AutoGroup != model.AutoGroupTypeExact || len(channel.Keys) != 1 || channel.Keys[0].Enabled || channel.Keys[0].ID != 200 {
				t.Fatal("channel identity or enablement changed")
			}
			var batch model.CheckinBatchJob
			if err := conn.First(&batch, 31).Error; err != nil || batch.Status != model.SiteCheckinBatchJobStatusInterrupted || batch.CurrentAccountID != 20 || len(batch.SiteIDs) != 1 || batch.SiteIDs[0] != 2 || batch.Attempted != 1 {
				t.Fatal("batch history changed")
			}
			store := op.NewCheckinStore(conn)
			execution, err := store.BeginWorkflow(ctx, 30, model.CheckinTriggerManual, true)
			if err != nil || !execution.Workflow().Submitted || execution.Workflow().LogID != 9007199254740999 {
				t.Fatalf("lost submission: %v", err)
			}
			if _, err := store.ReserveWorkflowSubmission(ctx, execution, true); !errors.Is(err, op.ErrCheckinWorkflowConflict) {
				t.Fatalf("unexpected reservation result: %v", err)
			}
			freshSite := model.CheckinSite{Name: "fresh"}
			if err := conn.Create(&freshSite).Error; err != nil || freshSite.ID <= 99 {
				t.Fatalf("reused site identity %d: %v", freshSite.ID, err)
			}
			freshAccount := model.CheckinAccount{SiteID: freshSite.ID, Name: "fresh", CredentialID: 10}
			if err := conn.Create(&freshAccount).Error; err != nil || freshAccount.ID <= 999 {
				t.Fatalf("reused account identity %d: %v", freshAccount.ID, err)
			}
		})
	}
}

func TestDatabaseMigrationBlocksTargetConflictsBeforeWriting(t *testing.T) {
	conn := sqliteMigrationDatabase(t)
	seedMigrationDatabase(t, conn)
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.CheckinSite{ID: 777, Name: "Token rewards"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.CheckinCredential{ID: 10, Name: "existing"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.CheckinSite{ID: 99, Name: "unrelated"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&model.CheckinAccount{ID: 999, SiteID: 99, Name: "unrelated", CredentialID: 10}).Error; err != nil {
		t.Fatal(err)
	}
	input, err := CaptureDatabaseInput(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	report, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input)
	if !errors.Is(err, ErrMigrationBlocked) || report.Report.Blockers != 4 || conn.Migrator().HasTable(&MigrationRun{}) {
		t.Fatalf("did not block target conflicts: %v, %+v", err, report)
	}
}

func TestDatabaseMigrationConcurrentApply(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var conn *gorm.DB
			if dialect == "sqlite" {
				conn = sqliteMigrationDatabase(t)
			} else {
				dsn := os.Getenv("CHECKIN_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("external database not configured")
				}
				conn = openExternalDatabase(t, dialect, dsn)
			}
			seedMigrationDatabase(t, conn)
			input, err := CaptureDatabaseInput(t.Context(), conn)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			finished := make(chan error, 2)
			for range 2 {
				go func() {
					<-start
					result, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input)
					if err == nil && !result.Completed {
						err = errors.New("migration incomplete")
					}
					finished <- err
				}()
			}
			close(start)
			for range 2 {
				if err := <-finished; err != nil {
					t.Error(err)
				}
			}
			var stages []MigrationStage
			if err := conn.Find(&stages).Error; err != nil || len(stages) != len(migrationStages) {
				t.Fatalf("unexpected migration journal: %d stages, %v", len(stages), err)
			}
			for _, stage := range stages {
				if stage.Attempts != 1 || stage.State != "completed" {
					t.Fatalf("stage %s ran %d times (%s)", stage.Name, stage.Attempts, stage.State)
				}
			}
		})
	}
}

func TestDatabaseMigrationMySQLNameCollation(t *testing.T) {
	dsn := os.Getenv("CHECKIN_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("external MySQL not configured")
	}
	conn := openExternalDatabase(t, "mysql", dsn)
	if err := conn.Exec("ALTER DATABASE CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		t.Fatal(err)
	}
	seedMigrationDatabase(t, conn)
	var account model.LegacySiteAccount
	if err := conn.First(&account, 30).Error; err != nil {
		t.Fatal(err)
	}
	account.ID, account.Name, account.CheckinPending = 31, strings.ToUpper(account.Name), nil
	if err := conn.Omit(clause.Associations).Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	input, err := CaptureDatabaseInput(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	result, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input)
	if !errors.Is(err, ErrMigrationBlocked) || result.Report.Blockers != 1 || conn.Migrator().HasTable(&MigrationRun{}) {
		t.Fatalf("case-insensitive collision did not block before writes: %v, %+v", err, result)
	}
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("ALTER TABLE checkin_accounts MODIFY name VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	if result, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input); err != nil || !result.Completed {
		t.Fatalf("case-sensitive names were not preserved: %v, %+v", err, result)
	}
}

func TestCaptureDatabaseInputSupportsOlderV1Schemas(t *testing.T) {
	conn := sqliteMigrationDatabase(t)
	seedMigrationDatabase(t, conn)
	for _, column := range []string{"TokenRefreshLease", "TokenRefreshLeaseUntil", "TokenRefreshedAt"} {
		if err := conn.Migrator().DropColumn(&model.LegacySiteAccount{}, column); err != nil {
			t.Fatalf("drop legacy-added account column %s: %v", column, err)
		}
	}
	for _, table := range []any{&model.GroupPreset{}, &model.CheckinBatchJob{}} {
		if err := conn.Migrator().DropTable(table); err != nil {
			t.Fatalf("drop optional legacy table %T: %v", table, err)
		}
	}
	if _, err := CaptureDatabaseInput(t.Context(), conn); err == nil {
		t.Fatal("accepted missing tables in a schema that uses them")
	}
	for _, query := range []string{
		"ALTER TABLE groups DROP COLUMN active_preset_id",
		"DROP INDEX idx_site_checkin_batch_job_id",
		"ALTER TABLE site_checkin_logs DROP COLUMN batch_job_id",
	} {
		if err := conn.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	input, err := CaptureDatabaseInput(t.Context(), conn)
	if err != nil {
		t.Fatalf("capture older schema: %v", err)
	}
	if input.Configuration.SiteAccounts[0].TokenRefreshLease != "" || input.Configuration.SiteAccounts[0].TokenRefreshedAt != 0 || len(input.GroupPresets) != 0 || len(input.Batches) != 0 {
		t.Fatal("missing legacy additions were not treated as their documented defaults")
	}
	if result, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input); err != nil || !result.Completed {
		t.Fatalf("migrate older schema: %v, %+v", err, result)
	}
	if err := conn.Exec("ALTER TABLE site_accounts DROP COLUMN password").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureDatabaseInput(t.Context(), conn); err == nil {
		t.Fatal("accepted missing identity data")
	}

	missing := sqliteMigrationDatabase(t)
	if _, err := CaptureDatabaseInput(t.Context(), missing); err == nil {
		t.Fatal("accepted a database missing required legacy tables")
	}
}
