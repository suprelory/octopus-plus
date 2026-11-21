package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"slices"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/migration/v1"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrMigrationBlocked       = errors.New("database migration is blocked by conversion or target conflicts")
	ErrMigrationSourceChanged = errors.New("legacy database changed after the frozen snapshot was captured")
	ErrMigrationVerification  = errors.New("database migration verification failed")
)

type MigrationResult struct {
	Source    string           `json:"source"`
	Completed bool             `json:"completed"`
	NextStage Stage            `json:"next_stage,omitempty"`
	Report    ConversionReport `json:"report"`
}

// MigrateDatabase applies one frozen conversion. Legacy writers must be stopped
// and a backup made before calling it.
func MigrateDatabase(ctx context.Context, conn *gorm.DB, source string, input *LegacyInput) (*MigrationResult, error) {
	return runDatabaseMigration(ctx, conn, source, input, true)
}

// InspectDatabase performs the same source and target checks as apply without DDL or writes.
func InspectDatabase(ctx context.Context, conn *gorm.DB, source string, input *LegacyInput) (*MigrationResult, error) {
	return runDatabaseMigration(ctx, conn, source, input, false)
}

const DatabaseSource = "legacy-database"

func runDatabaseMigration(ctx context.Context, conn *gorm.DB, source string, input *LegacyInput, apply bool) (*MigrationResult, error) {
	if conn == nil || source != DatabaseSource || input == nil || !input.HistoryComplete {
		return nil, errors.New("migration requires a complete database checkpoint")
	}
	c, err := Convert(input)
	if err != nil {
		return nil, err
	}
	result := &MigrationResult{Source: source, Report: c.Report}
	if !c.Ready() {
		return result, ErrMigrationBlocked
	}
	conn = conn.WithContext(ctx)
	var next int
	err = readSnapshotTransaction(conn, func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&MigrationRun{}) {
			run, found, err := NewJournal(tx).Find(ctx, source)
			if err != nil {
				return err
			}
			if found {
				if run.Version != 2 || run.Fingerprint != c.Fingerprint {
					return ErrMigrationInputChanged
				}
				next = run.NextStage
			}
		}
		if next < 0 || next > len(migrationStages) {
			return ErrMigrationStageOrder
		}
		if next == len(migrationStages) {
			result.Completed = true
			return nil
		}
		result.NextStage = migrationStages[next]
		if err := normalizeRebuiltLogTimestamps(tx, input, c); err != nil {
			return err
		}
		if err := verifyLegacyState(tx, input, c, next); err != nil {
			return err
		}
		return checkMigrationTargets(tx, next, c)
	})
	c.finishReport()
	result.Report = c.Report
	if err != nil || !apply || result.Completed {
		return result, err
	}
	if err := prepareMigrationSchema(conn); err != nil {
		return result, err
	}
	journal := NewJournal(conn)
	run, err := journal.Open(ctx, source, c.Fingerprint)
	if err != nil {
		return result, err
	}
	for _, stage := range migrationStages[next:] {
		result.NextStage = stage
		if stage == StageVerify && conn.Dialector.Name() == "mysql" {
			if err := advanceMySQLSequences(conn, sequenceFloors(input, c)); err != nil {
				return result, errors.New("advance MySQL identity counters failed")
			}
		}
		_, err := journal.Apply(ctx, *run, stage, mappingsForStage(c.Report.Mappings, stage), func(tx *gorm.DB) error {
			if stage != StageVerify {
				if err := verifyLegacyState(tx, input, c, stageIndex(stage)); err != nil {
					return err
				}
			}
			switch stage {
			case StageSites:
				return createRows(tx, c.Sites)
			case StageCredentials:
				return createRows(tx, c.Credentials)
			case StageAccounts:
				return createRows(tx, c.Accounts)
			case StageChannels:
				return convertChannels(tx, input, c)
			case StageHistory:
				return convertHistory(tx, input, c)
			case StageVerify:
				return verifyConversion(tx, input, c)
			}
			return ErrMigrationStageOrder
		})
		if err != nil {
			return result, err
		}
	}
	result.Completed, result.NextStage = true, ""
	return result, nil
}

func prepareMigrationSchema(conn *gorm.DB) error {
	prepare := func(db *gorm.DB) error {
		if err := dbpkg.PrepareCheckinSchema(db); err != nil {
			return errors.New("prepare independent check-in schema failed")
		}
		if err := v1.PrepareCheckinHistorySchema(db); err != nil {
			return errors.New("prepare legacy history columns failed")
		}
		return PrepareJournalSchema(db)
	}
	lockErr := errors.New("cannot acquire migration schema lock")
	if conn.Dialector.Name() == "mysql" {
		// DDL implicitly commits on MySQL. A named lock survives those commits
		// and is held only on the connection that prepares this database.
		return conn.Connection(func(db *gorm.DB) (err error) {
			const name = "CONCAT('octopus-v2-schema-', MD5(DATABASE()))"
			pinned := db.Statement.ConnPool.(*sql.Conn)
			var locked int
			if err := pinned.QueryRowContext(db.Statement.Context, "SELECT GET_LOCK("+name+", 30)").Scan(&locked); err != nil || locked != 1 {
				return lockErr
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(db.Statement.Context), 5*time.Second)
				defer cancel()
				var released int
				if releaseErr := pinned.QueryRowContext(ctx, "SELECT RELEASE_LOCK("+name+")").Scan(&released); releaseErr != nil || released != 1 {
					_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
					if err == nil {
						err = errors.New("cannot release migration schema lock")
					}
				}
			}()
			return prepare(db.Session(&gorm.Session{NewDB: true}))
		})
	}
	return conn.Transaction(func(tx *gorm.DB) error {
		switch tx.Dialector.Name() {
		case "sqlite":
			// Take the SQLite writer lock before inspecting any schema objects.
			if err := tx.Exec("UPDATE sites SET id = id WHERE 0").Error; err != nil {
				return lockErr
			}
		case "postgres":
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database() || '-octopus-v2-schema'))").Error; err != nil {
				return lockErr
			}
		default:
			return lockErr
		}
		return prepare(tx)
	})
}

// The checkpoint never changes. Project only the committed mutations to shared
// v1 tables before comparing it with the current database on restart.
func verifyLegacyState(tx *gorm.DB, input *LegacyInput, c *Conversion, next int) error {
	actual, err := readDatabaseInput(tx, input.CapturedAt)
	if err != nil {
		return err
	}
	expected := *input
	if next > stageIndex(StageChannels) {
		expected.Configuration.Channels = c.Channels
		expected.Configuration.SiteChannelBindings = nil
		expected.Configuration.Settings = slices.DeleteFunc(slices.Clone(input.Configuration.Settings), func(s model.Setting) bool {
			return s.Key == model.SettingKeyProjectedChannelAutoGroupEnabled
		})
		if len(expected.Configuration.Settings) == 0 {
			expected.Configuration.Settings = nil
		}
	}
	if next > stageIndex(StageHistory) {
		expected.Logs = append([]model.CheckinLog(nil), c.Logs...)
		expected.Batches = append([]model.CheckinBatchJob(nil), c.Batches...)
	}
	left, err := inputFingerprint(actual)
	if err != nil {
		return err
	}
	right, err := inputFingerprint(&expected)
	if err != nil {
		return err
	}
	if left != right {
		return ErrMigrationSourceChanged
	}
	return nil
}

func mappingsForStage(mappings []Mapping, stage Stage) []Mapping {
	entities := map[Stage]Entity{StageSites: EntitySite, StageCredentials: EntityCredential, StageAccounts: EntityAccount, StageChannels: EntityChannel}
	entity, ok := entities[stage]
	if !ok {
		return nil
	}
	result := make([]Mapping, 0)
	for _, mapping := range mappings {
		if mapping.Entity == entity {
			result = append(result, mapping)
		}
	}
	return result
}

type migrationTargetRow struct {
	id, siteID int
	name       string
}

// MySQL may fold accents and case in a unique index. Use the target column's
// collation, or the database default if the table has yet to be created.
func checkMySQLNames(tx *gorm.DB, c *Conversion, table string, entity Entity, rows []migrationTargetRow) error {
	var charset, collation string
	if err := tx.Raw(`SELECT character_set_name, collation_name
		FROM information_schema.collations WHERE collation_name = COALESCE(
		(SELECT collation_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'name'),
		@@collation_database)`, table).Row().Scan(&charset, &collation); err != nil {
		return errors.New("cannot inspect migration target collation")
	}
	type scopedName struct {
		siteID int
		weight string
	}
	seen := map[scopedName]int{}
	for _, row := range rows {
		var weight string
		err := tx.Raw("SELECT HEX(WEIGHT_STRING(CONVERT(? USING ?) COLLATE ?))", row.name,
			clause.Column{Name: charset}, clause.Column{Name: collation}).Row().Scan(&weight)
		if err != nil {
			return errors.New("cannot compare migration target names")
		}
		key := scopedName{row.siteID, weight}
		if other, found := seen[key]; found {
			c.issue("blocker", "target_name_conflict", string(entity), row.id, other)
		} else {
			seen[key] = row.id
		}
	}
	return nil
}

func checkMigrationTargets(tx *gorm.DB, next int, c *Conversion) error {
	// Shared logs and batch scopes also refer to deleted owners. A previously
	// created v2 record with such an ID would silently inherit their history.
	reserved := map[Entity]map[int]bool{EntitySite: {}, EntityAccount: {}}
	for _, log := range c.Logs {
		reserved[EntitySite][log.SiteID] = true
		reserved[EntityAccount][log.AccountID] = true
	}
	for _, job := range c.Batches {
		reserved[EntitySite][job.CurrentSiteID] = true
		reserved[EntityAccount][job.CurrentAccountID] = true
		for _, id := range job.SiteIDs {
			reserved[EntitySite][id] = true
		}
	}
	var sites, accounts, credentials []migrationTargetRow
	for _, site := range c.Sites {
		sites = append(sites, migrationTargetRow{id: site.ID, name: site.Name})
		delete(reserved[EntitySite], site.ID)
	}
	for _, account := range c.Accounts {
		accounts = append(accounts, migrationTargetRow{id: account.ID, name: account.Name, siteID: account.SiteID})
		delete(reserved[EntityAccount], account.ID)
	}
	for _, credential := range c.Credentials {
		credentials = append(credentials, migrationTargetRow{id: credential.ID})
	}
	check := func(table any, entity Entity, stage Stage, row migrationTargetRow) error {
		id, siteID, name := row.id, row.siteID, row.name
		query := tx.Model(table)
		var found []int
		err := query.Where("id = ?", id).Pluck("id", &found).Error
		if err != nil {
			return errors.New("read migration target failed")
		}
		if next > stageIndex(stage) {
			if len(found) == 0 {
				c.issue("blocker", "completed_target_row_missing", string(entity), id, 0)
			}
			return nil
		}
		if len(found) != 0 {
			c.issue("blocker", "target_id_conflict", string(entity), id, 0)
		}
		if name == "" {
			return nil
		}
		query = tx.Model(table).Where("name = ?", name)
		if siteID != 0 {
			query = query.Where("site_id = ?", siteID)
		}
		if err := query.Pluck("id", &found).Error; err != nil {
			return errors.New("read migration target names failed")
		}
		if len(found) != 0 {
			c.issue("blocker", "target_name_conflict", string(entity), id, found[0])
		}
		return nil
	}
	before := len(c.Report.Issues)
	for _, target := range []struct {
		model  any
		stage  Stage
		entity Entity
		rows   []migrationTargetRow
		table  string
	}{
		{&model.CheckinSite{}, StageSites, EntitySite, sites, "checkin_sites"},
		{&model.CheckinCredential{}, StageCredentials, EntityCredential, credentials, "checkin_credentials"},
		{&model.CheckinAccount{}, StageAccounts, EntityAccount, accounts, "checkin_accounts"},
	} {
		if tx.Dialector.Name() == "mysql" && target.entity != EntityCredential && len(target.rows) > 1 && next <= stageIndex(target.stage) {
			if err := checkMySQLNames(tx, c, target.table, target.entity, target.rows); err != nil {
				return err
			}
		}
		if !tx.Migrator().HasTable(target.model) {
			if next > stageIndex(target.stage) {
				return ErrMigrationVerification
			}
			continue
		}
		if ids := reserved[target.entity]; len(ids) != 0 {
			reservedIDs := make([]int, 0, len(ids))
			for id := range ids {
				if id > 0 {
					reservedIDs = append(reservedIDs, id)
				}
			}
			slices.Sort(reservedIDs)
			for batch := range slices.Chunk(reservedIDs, 500) {
				var existing []int
				if err := tx.Model(target.model).Where("id IN ?", batch).Pluck("id", &existing).Error; err != nil {
					return errors.New("cannot read existing migration targets")
				}
				for _, id := range existing {
					c.issue("blocker", "historical_owner_id_conflict", string(target.entity), id, 0)
				}
			}
		}
		for _, row := range target.rows {
			if err := check(target.model, target.entity, target.stage, row); err != nil {
				return err
			}
		}
	}
	if len(c.Report.Issues) != before {
		return ErrMigrationBlocked
	}
	return nil
}

func createRows[T any](tx *gorm.DB, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.CreateInBatches(&rows, 100).Error
}

func convertChannels(tx *gorm.DB, input *LegacyInput, c *Conversion) error {
	byID := make(map[int]model.AutoGroupType, len(c.Channels))
	for _, row := range c.Channels {
		byID[row.ID] = row.AutoGroup
	}
	for _, binding := range input.Configuration.SiteChannelBindings {
		if err := tx.Model(&model.Channel{}).Where("id = ?", binding.ChannelID).UpdateColumn("auto_group", byID[binding.ChannelID]).Error; err != nil {
			return err
		}
		if deleted := tx.Delete(&model.LegacySiteChannelBinding{}, binding.ID); deleted.Error != nil || deleted.RowsAffected != 1 {
			return ErrMigrationSourceChanged
		}
	}
	return tx.Where(&model.Setting{Key: model.SettingKeyProjectedChannelAutoGroupEnabled}).Delete(&model.Setting{}).Error
}

func convertHistory(tx *gorm.DB, input *LegacyInput, c *Conversion) error {
	original := make(map[int64]model.CheckinLog, len(input.Logs))
	for _, row := range input.Logs {
		original[row.ID] = row
	}
	for _, row := range c.Logs {
		old, existed := original[row.ID]
		if !existed {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if old.SiteID != row.SiteID || old.AccountID != row.AccountID {
			updated := tx.Model(&row).Where("site_id = ? AND account_id = ?", old.SiteID, old.AccountID).
				Select("site_id", "account_id").UpdateColumns(&row)
			if updated.Error != nil || updated.RowsAffected != 1 {
				return ErrMigrationSourceChanged
			}
		}
	}
	for i, job := range c.Batches {
		// Both slices are ordered by the unchanged batch ID.
		if old := input.Batches[i]; old.ID != job.ID {
			return ErrMigrationSourceChanged
		} else if reflect.DeepEqual(old, job) {
			continue
		}
		if err := tx.Model(&job).Select("site_ids", "current_site_id", "current_account_id", "status", "error_message", "finished_at", "updated_at").UpdateColumns(&job).Error; err != nil {
			return err
		}
	}
	return nil
}
