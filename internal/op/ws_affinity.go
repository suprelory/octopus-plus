package op

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func WSResponseAffinityCleanup(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	result := db.GetDB().WithContext(ctx).
		Where("expires_at <= ?", now).
		Delete(&model.WSResponseAffinity{})
	return result.RowsAffected, result.Error
}
