package backup

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const Version = 2

var (
	ErrFormat   = errors.New("invalid or unsupported database backup")
	ErrConflict = errors.New("backup conflicts with existing data")
	ErrInvalid  = errors.New("backup contains invalid references or workflow state")
	ErrStorage  = errors.New("database backup operation failed")
)

// Document keeps the version 1 fields as a decoding boundary. Version 2 exports
// populate ordinary configuration and the independent check-in fields only.
type Document struct {
	model.LegacyDBDump
	CheckinSites       []Site                          `json:"checkin_sites,omitempty"`
	CheckinCredentials []model.CheckinCredentialBackup `json:"checkin_credentials,omitempty"`
	CheckinAccounts    []Account                       `json:"checkin_accounts,omitempty"`
	CheckinCompletions []model.CheckinCompletion       `json:"checkin_completions,omitempty"`
	CheckinDeliveries  []Delivery                      `json:"checkin_deliveries,omitempty"`
	CheckinCooldowns   []Cooldown                      `json:"checkin_cooldowns,omitempty"`
	ArchivedSiteStats  []ArchivedSiteStat              `json:"archived_site_stats,omitempty"`
	archive            map[string]*zip.File
	temporary          *os.File
}

type Site struct {
	model.CheckinSite
	VerificationFingerprint string `json:"verification_fingerprint"`
}

type Account struct {
	model.CheckinAccount
	WorkflowVersion      int64 `json:"workflow_version"`
	BalanceVersion       int64 `json:"balance_version"`
	BalanceConfigVersion int64 `json:"balance_config_version"`
}

type Delivery struct {
	model.CheckinNotificationDelivery
	CooldownKey string `json:"cooldown_key"`
	CooldownMS  int64  `json:"cooldown_ms"`
}

// A cooldown's opaque key includes the account ID. Carry the inputs so a
// restore can recompute it after remapping that ID, including expired deliveries.
type Cooldown struct {
	Key         string `json:"key"`
	Until       int64  `json:"until"`
	AccountID   int    `json:"account_id"`
	Destination string `json:"destination"`
	Event       string `json:"event"`
	Reason      string `json:"reason"`
}

// Subscription statistics remain an archive with an explicit source namespace.
// They never become check-in account statistics or ordinary channel totals.
type ArchivedSiteStat struct {
	Source string                           `json:"source" gorm:"primaryKey;size:64"`
	Key    string                           `json:"-" gorm:"primaryKey;size:64"`
	Stats  model.LegacyStatsSiteModelHourly `json:"stats" gorm:"type:text;serializer:json"`
}

func (ArchivedSiteStat) TableName() string { return "v2_backup_site_stats" }

type importRun struct {
	Fingerprint string `gorm:"primaryKey;size:64"`
	ImportedAt  time.Time
}

func (importRun) TableName() string { return "v2_backup_imports" }

type importMapping struct {
	Fingerprint string `gorm:"primaryKey;size:64"`
	Entity      string `gorm:"primaryKey;size:32"`
	SourceID    int64  `gorm:"primaryKey;autoIncrement:false"`
	TargetID    int64
}

func (importMapping) TableName() string { return "v2_backup_mappings" }

type Service struct{ conn *gorm.DB }

func NewService(conn *gorm.DB) *Service {
	return &Service{conn: conn.Session(&gorm.Session{Logger: logger.Discard})}
}

// Close releases the private archive spool created by Read. In-memory documents
// and documents returned by Export have nothing to release.
func (d *Document) Close() error {
	if d == nil || d.temporary == nil {
		return nil
	}
	file := d.temporary
	d.temporary = nil
	closeErr := file.Close()
	return errors.Join(closeErr, os.Remove(file.Name()))
}

type contextReader struct {
	ctx    context.Context
	reader interface{ Read([]byte) (int, error) }
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
