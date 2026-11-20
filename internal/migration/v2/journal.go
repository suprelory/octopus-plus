package v2

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrMigrationInputChanged    = errors.New("migration input differs from the recorded snapshot")
	ErrMigrationStageOrder      = errors.New("migration stage is out of order")
	ErrMigrationMappingConflict = errors.New("migration source mapping conflicts with an existing destination")
	ErrMigrationStageFailed     = errors.New("migration stage failed; its writes were rolled back")
)

type Stage string

const (
	StageSites       Stage = "sites"
	StageCredentials Stage = "credentials"
	StageAccounts    Stage = "accounts"
	StageChannels    Stage = "channels"
	StageHistory     Stage = "history"
	StageVerify      Stage = "verify"
)

var migrationStages = [...]Stage{StageSites, StageCredentials, StageAccounts, StageChannels, StageHistory, StageVerify}
var sourceNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,128}$`)

type MigrationRun struct {
	ID          int64  `gorm:"primaryKey"`
	Source      string `gorm:"size:128;not null;uniqueIndex"`
	Fingerprint string `gorm:"size:64;not null"`
	Version     int    `gorm:"not null"`
	NextStage   int    `gorm:"not null"`
	Revision    int64  `gorm:"not null"`
}

func (MigrationRun) TableName() string { return "v2_migration_runs" }
func (r MigrationRun) Complete() bool  { return r.NextStage == len(migrationStages) }

type MigrationStage struct {
	RunID     int64  `gorm:"primaryKey;autoIncrement:false"`
	Name      Stage  `gorm:"primaryKey;size:32"`
	State     string `gorm:"size:16;not null"`
	Attempts  int    `gorm:"not null"`
	ErrorCode string `gorm:"size:64;not null"`
}

func (MigrationStage) TableName() string { return "v2_migration_stages" }

type MigrationMapping struct {
	RunID    int64  `gorm:"primaryKey;autoIncrement:false"`
	Entity   Entity `gorm:"primaryKey;size:32"`
	SourceID int    `gorm:"primaryKey;autoIncrement:false"`
	TargetID int    `gorm:"not null"`
}

func (MigrationMapping) TableName() string { return "v2_migration_mappings" }

// PrepareJournalSchema is explicit and DDL-only. Run it before transactions;
// MySQL DDL cannot be rolled back together with stage data. No old or new
// business tables are initialized, no production startup path calls this yet.
func PrepareJournalSchema(conn *gorm.DB) error {
	if conn == nil {
		return errors.New("migration journal requires a database")
	}
	for _, table := range []any{&MigrationRun{}, &MigrationStage{}, &MigrationMapping{}} {
		if !conn.Migrator().HasTable(table) {
			if err := conn.Migrator().CreateTable(table); err != nil {
				return errors.New("prepare migration journal failed")
			}
		}
	}
	return nil
}

type Journal struct{ conn *gorm.DB }

func NewJournal(conn *gorm.DB) *Journal { return &Journal{conn: conn} }

// Open pins a stable source identity to one frozen snapshot. Sources must be
// opaque database/backup identifiers, never URLs, credentials or filenames.
// Reusing an identity with changed data requires explicit conflict resolution.
// Identifiers are case-normalized to behave identically across DB collations.
func (j *Journal) Open(ctx context.Context, source, fingerprint string) (*MigrationRun, error) {
	source, fingerprint = strings.ToLower(source), strings.ToLower(fingerprint)
	decoded, err := hex.DecodeString(fingerprint)
	if j == nil || j.conn == nil || !sourceNamePattern.MatchString(source) || err != nil || len(decoded) != 32 || len(fingerprint) != 64 {
		return nil, errors.New("invalid migration journal identity")
	}
	run := MigrationRun{Source: source, Fingerprint: fingerprint, Version: 2}
	conn := j.conn.WithContext(ctx)
	if err := conn.Clauses(clause.OnConflict{DoNothing: true}).Create(&run).Error; err != nil {
		return nil, errors.New("open migration journal failed")
	}
	var saved MigrationRun
	if err := conn.Where("source = ?", source).First(&saved).Error; err != nil {
		return nil, errors.New("read migration journal failed")
	}
	if saved.Fingerprint != fingerprint || saved.Version != 2 {
		return nil, ErrMigrationInputChanged
	}
	return &saved, nil
}

func stageIndex(stage Stage) int {
	for i, name := range migrationStages {
		if stage == name {
			return i
		}
	}
	return -1
}

// Apply commits business writes, source mappings and stage completion together.
// A row write acquires the lock before reading, including on SQLite. Callbacks
// must use only this transaction: no DDL, network calls or external side effects.
// A completed stage is a no-op even if destination records were manually edited.
// A process crash rolls back the transaction; the same frozen input can retry.
func (j *Journal) Apply(ctx context.Context, run MigrationRun, stage Stage, mappings []Mapping, write func(*gorm.DB) error) (bool, error) {
	index := stageIndex(stage)
	if j == nil || j.conn == nil || index < 0 || run.ID <= 0 || write == nil {
		return false, ErrMigrationStageOrder
	}
	for _, mapping := range mappings {
		expected := map[Entity]Stage{EntitySite: StageSites, EntityCredential: StageCredentials, EntityAccount: StageAccounts, EntityChannel: StageChannels}
		if expected[mapping.Entity] != stage || mapping.SourceID <= 0 || mapping.TargetID <= 0 {
			return false, ErrMigrationMappingConflict
		}
	}
	applied, entered := false, false
	err := j.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockRun(tx, run)
		if err != nil {
			return err
		}
		if current.NextStage > index {
			// A retry must not silently accept a different mapping proposal.
			return checkSavedMappings(tx, run.ID, mappings)
		}
		if current.NextStage != index {
			return ErrMigrationStageOrder
		}
		entered = true
		for _, mapping := range mappings {
			row := MigrationMapping{RunID: run.ID, Entity: mapping.Entity, SourceID: mapping.SourceID, TargetID: mapping.TargetID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return ErrMigrationStageFailed
			}
			var saved MigrationMapping
			if err := tx.Where("run_id = ? AND entity = ? AND source_id = ?", run.ID, mapping.Entity, mapping.SourceID).First(&saved).Error; err != nil {
				return ErrMigrationStageFailed
			}
			if saved.TargetID != mapping.TargetID {
				return ErrMigrationMappingConflict
			}
		}
		if err := write(tx); err != nil {
			return ErrMigrationStageFailed
		}
		if err := recordStage(tx, run.ID, stage, "completed", ""); err != nil {
			return ErrMigrationStageFailed
		}
		if err := tx.Model(&MigrationRun{}).Where("id = ?", run.ID).Update("next_stage", index+1).Error; err != nil {
			return ErrMigrationStageFailed
		}
		applied = true
		return nil
	})
	if err != nil {
		if entered {
			// Failure diagnostics contain a stable code only. The original SQL/input
			// error may include secrets and must not enter the persistent journal.
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = j.conn.WithContext(failureCtx).Transaction(func(tx *gorm.DB) error {
				current, lockErr := lockRun(tx, run)
				if lockErr != nil {
					return lockErr
				}
				if current.NextStage != index {
					return nil
				} // A concurrent retry already completed it.
				return recordStage(tx, run.ID, stage, "failed", "stage_failed")
			})
		}
		if errors.Is(err, ErrMigrationInputChanged) || errors.Is(err, ErrMigrationStageOrder) || errors.Is(err, ErrMigrationMappingConflict) {
			return false, err
		}
		return false, ErrMigrationStageFailed
	}
	return applied, nil
}

func checkSavedMappings(tx *gorm.DB, runID int64, mappings []Mapping) error {
	for _, mapping := range mappings {
		var saved MigrationMapping
		err := tx.Where("run_id = ? AND entity = ? AND source_id = ?", runID, mapping.Entity, mapping.SourceID).First(&saved).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && saved.TargetID != mapping.TargetID {
			return ErrMigrationMappingConflict
		}
		if err != nil {
			return ErrMigrationStageFailed
		}
	}
	return nil
}

func lockRun(tx *gorm.DB, expected MigrationRun) (*MigrationRun, error) {
	// A write first avoids SQLite's deferred read-to-write lock upgrade race.
	result := tx.Model(&MigrationRun{}).Where("id = ? AND source = ? AND fingerprint = ? AND version = ?", expected.ID, expected.Source, expected.Fingerprint, 2).
		Update("revision", gorm.Expr("revision + 1"))
	if result.Error != nil {
		return nil, ErrMigrationStageFailed
	}
	if result.RowsAffected != 1 {
		return nil, ErrMigrationInputChanged
	}
	var run MigrationRun
	if err := tx.First(&run, expected.ID).Error; err != nil {
		return nil, ErrMigrationStageFailed
	}
	return &run, nil
}

func recordStage(tx *gorm.DB, runID int64, stage Stage, state, code string) error {
	row := MigrationStage{RunID: runID, Name: stage, State: state, ErrorCode: code, Attempts: 1}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "name"}},
		DoUpdates: clause.Assignments(map[string]any{"state": state, "error_code": code, "attempts": gorm.Expr("v2_migration_stages.attempts + 1")})}).Create(&row).Error
}

func (j *Journal) Mappings(ctx context.Context, run MigrationRun) ([]Mapping, error) {
	if j == nil || j.conn == nil || run.ID <= 0 {
		return nil, errors.New("invalid migration journal identity")
	}
	var saved MigrationRun
	err := j.conn.WithContext(ctx).Where("id = ? AND source = ? AND fingerprint = ? AND version = ?", run.ID, run.Source, run.Fingerprint, 2).First(&saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMigrationInputChanged
	}
	if err != nil {
		return nil, errors.New("read migration journal failed")
	}
	var rows []MigrationMapping
	if err := j.conn.WithContext(ctx).Where("run_id = ?", run.ID).Order("entity, source_id").Find(&rows).Error; err != nil {
		return nil, errors.New("read migration mappings failed")
	}
	result := make([]Mapping, 0, len(rows))
	for _, row := range rows {
		result = append(result, Mapping{row.Entity, row.SourceID, row.TargetID})
	}
	return result, nil
}
