package v2

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

// Exercise converted records through the real store, using only independent
// schema and the journal transaction. No runtime network request is made.
func TestConversionWorkflowSurvivesDatabaseRestore(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		valid, finished, log bool
	}{
		{"submitted", true, false, true},
		{"completed", true, true, true},
		{"invalid fingerprint", false, false, true},
		{"omitted history", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := conversionFixture()
			input.Configuration.Channels, input.Configuration.ChannelKeys, input.Configuration.SiteChannelBindings = nil, nil, nil
			input.Configuration.Groups, input.Configuration.GroupItems = nil, nil
			site, account := &input.Configuration.Sites[2], &input.Configuration.SiteAccounts[2]
			account.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740993, Date: "2026-11-20", Submitted: true,
				Finished: tc.finished, Status: model.SiteExecutionStatusPending, StartedAt: input.CapturedAt, Deadline: input.CapturedAt.Add(time.Hour),
				Fingerprint: site.CheckinWorkflowFingerprint(account)}
			if tc.finished {
				account.CheckinPending.Status = model.SiteExecutionStatusSuccess
			}
			if !tc.valid {
				account.CheckinPending.Fingerprint = "obsolete"
			}
			if tc.log {
				input.Logs = []model.CheckinLog{{ID: account.CheckinPending.LogID, SiteID: site.ID, AccountID: account.ID,
					SiteName: "Historical name", Status: account.CheckinPending.Status, Source: "manual", StartedAt: input.CapturedAt, FinishedAt: input.CapturedAt}}
				if tc.finished {
					input.Logs[0].Reward = "0"
				}
			} else {
				input.HistoryComplete = false
			}
			c := requireConversion(t, input)
			j, conn := journalDatabase(t)
			if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			run, err := j.Open(ctx, "workflow", c.Fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range migrationStages {
				var mappings []Mapping
				for _, mapping := range c.Report.Mappings {
					if string(mapping.Entity)+"s" == string(stage) {
						mappings = append(mappings, mapping)
					}
				}
				applied, err := j.Apply(ctx, *run, stage, mappings, func(tx *gorm.DB) error {
					switch stage {
					case StageSites:
						return tx.Create(&c.Sites).Error
					case StageCredentials:
						return tx.Create(&c.Credentials).Error
					case StageAccounts:
						return tx.Create(&c.Accounts).Error
					case StageHistory:
						return tx.Create(&c.Logs).Error
					default:
						return nil
					}
				})
				if err != nil || !applied {
					t.Fatalf("restore stage %s: %v", stage, err)
				}
			}
			store := op.NewCheckinStore(conn)
			execution, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, true)
			if err != nil || execution.Workflow().LogID != account.CheckinPending.LogID || !execution.Workflow().Submitted {
				t.Fatalf("restored submission lost: %v", err)
			}
			_, err = store.CheckWorkflow(ctx, execution)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, op.ErrCheckinWorkflowChanged) {
				t.Fatalf("old identity evidence changed: %v", err)
			}
			_, err = store.ReserveWorkflowSubmission(ctx, execution, true)
			expectedError := op.ErrCheckinWorkflowConflict
			if !tc.valid {
				expectedError = op.ErrCheckinWorkflowChanged
			}
			if !errors.Is(err, expectedError) {
				t.Fatalf("restored workflow can resubmit: %v", err)
			}
			outcome := model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "0"}
			_, err = store.FinishWorkflow(ctx, execution, outcome)
			if tc.valid && !tc.finished {
				if err != nil {
					t.Fatalf("confirmation failed: %v", err)
				}
				if _, err := store.FinishWorkflow(ctx, execution, outcome); !errors.Is(err, op.ErrCheckinWorkflowConflict) {
					t.Fatalf("confirmation counted twice: %v", err)
				}
			} else if !errors.Is(err, expectedError) {
				t.Fatalf("completed or invalid workflow changed: %v", err)
			}
			var entries []model.CheckinLog
			if err := conn.Find(&entries).Error; err != nil || len(entries) != 1 || entries[0].ID != account.CheckinPending.LogID {
				t.Fatalf("history duplicated: %v", err)
			}
			if tc.valid && entries[0].Reward != "0" || !tc.valid && entries[0].Reward != "" {
				t.Fatal("reward lost or invented")
			}
			var completions int64
			if err := conn.Model(&model.CheckinCompletion{}).Count(&completions).Error; err != nil {
				t.Fatal(err)
			}
			wantCompletions := int64(0)
			if tc.valid && !tc.finished {
				wantCompletions = 1
			}
			if completions != wantCompletions {
				t.Fatal("historical completion replayed or confirmation duplicated")
			}
			for _, table := range []string{"sites", "site_accounts", "site_channel_bindings"} {
				if conn.Migrator().HasTable(table) {
					t.Fatalf("restore required legacy table %s", table)
				}
			}
		})
	}
}

func TestConversionOrphanCanBeRepairedWithoutReauthorizingSubmission(t *testing.T) {
	input := conversionFixture()
	account := &input.Configuration.SiteAccounts[1]
	account.LinkedAccountID = id(999)
	account.CheckinPending = &model.SiteCheckinPending{LogID: 91, Submitted: true, Status: model.SiteExecutionStatusPending,
		Fingerprint: input.Configuration.Sites[1].CheckinWorkflowFingerprint(account), StartedAt: input.CapturedAt, Deadline: input.CapturedAt.Add(time.Hour)}
	c := requireConversion(t, input)
	_, conn := journalDatabase(t)
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, rows := range []any{&c.Sites, &c.Credentials, &c.Accounts, &c.Logs} {
		if err := conn.Create(rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	store, ctx := op.NewCheckinStore(conn), context.Background()
	if _, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("orphan used an unrelated credential: %v", err)
	}
	credential, err := store.CreateCredential(ctx, model.CheckinCredential{Name: "Replacement", Type: model.CheckinCredentialAccessToken,
		Platform: model.SitePlatformSub2API, AuthBaseURL: "https://login.example", AccessToken: "replacement-token"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"credential_id": credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	var patch model.CheckinAccountPatch
	if err := json.Unmarshal(data, &patch); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.PatchAccount(ctx, account.ID, patch)
	if err != nil || repaired.AutoCheckin || repaired.CheckinPending == nil || !repaired.CheckinPending.Submitted {
		t.Fatalf("repair reset workflow or schedule: %v", err)
	}
	execution, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, execution, true); !errors.Is(err, op.ErrCheckinWorkflowChanged) {
		t.Fatalf("repair reauthorized the old submission: %v", err)
	}
}
