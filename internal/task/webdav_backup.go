package task

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/webdav"
)

func WebDAVBackupTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := webdav.RunBackup(ctx); err != nil {
		log.Warnw("webdav.backup.failed", "trigger", "scheduled", "error", log.SafeError(err))
	}
}
