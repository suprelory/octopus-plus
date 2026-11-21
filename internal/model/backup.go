package model

// StatsModel is retained only for legacy backup compatibility. Current relay
// statistics do not use it, and new databases do not create its table unless
// a backup containing these historical records is restored.
type StatsModel struct {
	ID        int    `json:"id" gorm:"primaryKey"`
	Name      string `json:"name" gorm:"not null"`
	ChannelID int    `json:"channel_id" gorm:"not null"`
	StatsMetrics
}

// DBDump is the v1 incremental importer's input.
type DBDump = LegacyDBDump

type DBImportResult struct {
	// RowsAffected contains the rows affected for each table operation (insert/upsert depending on table).
	RowsAffected    map[string]int64 `json:"rows_affected"`
	AlreadyImported bool             `json:"already_imported,omitempty"`
	Issues          []BackupIssue    `json:"issues,omitempty"`
}

type BackupIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Entity   string `json:"entity,omitempty"`
	ID       int    `json:"id,omitempty"`
}
