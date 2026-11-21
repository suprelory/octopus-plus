package backup

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type importer struct {
	tx            *gorm.DB
	document      *Document
	fingerprint   string
	result        *model.DBImportResult
	configuration *op.BackupConfigurationMappings
	sites         map[int]model.CheckinSite
	credentials   map[int]model.CheckinCredential
	accounts      map[int]model.CheckinAccount
}

func (s *Service) Import(ctx context.Context, source *Document) (*model.DBImportResult, error) {
	if source == nil || source.Version != 1 && source.Version != Version {
		return nil, ErrFormat
	}
	identity, err := fingerprint(ctx, source)
	if err != nil {
		return nil, err
	}
	// Work on a private copy. A failed or repeated import must not rewrite its
	// input IDs, credential data or workflow fingerprints.
	data, err := json.Marshal(source)
	if err != nil {
		return nil, ErrFormat
	}
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, ErrFormat
	}
	document.archive = source.archive
	d := &document
	var issues []model.BackupIssue
	if d.Version == 1 {
		d, issues, err = ConvertLegacy(d)
		if err != nil {
			return &model.DBImportResult{Issues: issues}, err
		}
	}
	if err := validateDocument(d); err != nil {
		return nil, err
	}
	prepare := func(*gorm.DB) error {
		return s.prepareSchema(ctx, d.IncludeStats && len(d.StatsModel) > 0)
	}
	result, err := op.RestoreBackupTransaction(ctx, s.conn, d.IncludeStats, prepare, func(tx *gorm.DB) (*model.DBImportResult, error) {
		// Take the writer lock before any snapshot reads, also on SQLite. Every
		// importer uses the same row, including imports of different backups.
		if err := tx.Model(&importGate{}).Where("id = 1").Update("id", gorm.Expr("id")).Error; err != nil {
			return nil, err
		}
		result := &model.DBImportResult{RowsAffected: map[string]int64{}, Issues: issues}
		var previous importRun
		if err := tx.First(&previous, "fingerprint = ?", identity).Error; err == nil {
			result.AlreadyImported = true
			return result, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err := tx.Model(&model.CheckinNotificationGate{}).Where("id = 1").Update("id", gorm.Expr("id")).Error; err != nil {
			return nil, err
		}
		state := &importer{tx: tx, document: d, fingerprint: identity, result: result,
			sites: map[int]model.CheckinSite{}, credentials: map[int]model.CheckinCredential{}, accounts: map[int]model.CheckinAccount{}}
		common := d.DBDump
		common.StatsSiteModelHourly = nil
		common.Settings = slices.DeleteFunc(slices.Clone(common.Settings), func(setting model.Setting) bool {
			return setting.Key == model.SettingKeyProjectedChannelAutoGroupEnabled
		})
		var err error
		state.configuration, err = op.ImportBackupConfiguration(tx, &common, result)
		if err != nil {
			return nil, err
		}
		for _, step := range []func() error{state.importCheckin, state.importHistory, state.importNotifications, state.importArchives, state.saveMappings} {
			if err := step(); err != nil {
				return nil, err
			}
		}
		if err := tx.Create(&importRun{Fingerprint: identity, ImportedAt: time.Now().UTC()}).Error; err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
			return nil, err
		}
		if errors.Is(err, op.ErrBackupConflict) {
			return nil, ErrConflict
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrStorage
	}
	return result, nil
}

func (s *importer) insert(table string, row any) error {
	if err := s.tx.Omit(clause.Associations).Create(row).Error; err != nil {
		return err
	}
	s.result.RowsAffected[table]++
	return nil
}

func (s *importer) saveMappings() error {
	maps := map[string]map[int]int{
		"channel": s.configuration.Channels, "proxy": s.configuration.Proxies,
		"channel_key": s.configuration.ChannelKeys,
		"group":       s.configuration.Groups, "api_key": s.configuration.APIKeys,
		"site": {}, "credential": {}, "account": {},
	}
	for id, site := range s.sites {
		maps["site"][id] = site.ID
	}
	for id, credential := range s.credentials {
		maps["credential"][id] = credential.ID
	}
	for id, account := range s.accounts {
		maps["account"][id] = account.ID
	}
	for entity, ids := range maps {
		for source, target := range ids {
			if err := s.tx.Create(&importMapping{Fingerprint: s.fingerprint, Entity: entity, SourceID: int64(source), TargetID: int64(target)}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *importer) unusedID(table any, id int64) error {
	if id <= 0 {
		return ErrInvalid
	}
	var count int64
	if err := s.tx.Model(table).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrConflict
	}
	return nil
}

func validateDocument(d *Document) error {
	if d.Version != Version || d.ExportedAt.IsZero() || len(d.Sites)+len(d.SiteAccounts)+len(d.SiteTokens)+len(d.SiteUserGroups)+len(d.SiteModels)+len(d.SiteChannelBindings) != 0 {
		return ErrInvalid
	}
	for _, rows := range []any{d.Channels, d.ChannelKeys, d.ProxyConfigurations, d.Groups, d.GroupItems, d.GroupPresets, d.APIKeys, d.CheckinSites, d.CheckinCredentials, d.CheckinAccounts, d.SiteCheckinLogs, d.SiteCheckinBatchJobs, d.CheckinDeliveries} {
		seen := map[int64]bool{}
		value := reflect.ValueOf(rows)
		for i := 0; i < value.Len(); i++ {
			id := value.Index(i).FieldByName("ID").Int()
			if id <= 0 || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
		}
	}
	proxies, channels, groups := map[int]bool{}, map[int]bool{}, map[int]bool{}
	for _, row := range d.ProxyConfigurations {
		proxies[row.ID] = true
	}
	for _, row := range d.Channels {
		channels[row.ID] = true
		if row.ProxyMode == model.ProxyUsageModePool && (row.ProxyConfigID == nil || !proxies[*row.ProxyConfigID]) {
			return ErrInvalid
		}
	}
	for _, row := range d.ChannelKeys {
		if !channels[row.ChannelID] {
			return ErrInvalid
		}
	}
	for _, row := range d.Groups {
		groups[row.ID] = true
	}
	for _, row := range d.GroupItems {
		if !groups[row.GroupID] || !channels[row.ChannelID] {
			return ErrInvalid
		}
	}
	for _, row := range d.GroupPresets {
		if !groups[row.GroupID] {
			return ErrInvalid
		}
		for _, item := range row.Items {
			if !channels[item.ChannelID] {
				return ErrInvalid
			}
		}
	}
	return nil
}
