package op

import (
	"context"
	"database/sql"

	"github.com/suprelory/octopus-plus/internal/db"
	"gorm.io/gorm"
)

func withBackupSnapshot(ctx context.Context, export func(*gorm.DB) error) error {
	conn := db.GetDB().WithContext(ctx)
	// PostgreSQL defaults to READ COMMITTED, which would give each table a new
	// snapshot. MySQL also needs an explicit isolation level independent of its
	// server configuration. SQLite read transactions already pin a WAL snapshot.
	if conn.Dialector.Name() == "sqlite" {
		return conn.Transaction(export)
	}
	return conn.Transaction(export, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}
