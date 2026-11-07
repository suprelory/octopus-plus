package op

import (
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
)

// Defer this directly, with the named error result, when manually managing a
// transaction. A recovered panic must never become a successful operation.
func rollbackOnPanic(tx *gorm.DB, operation string, resourceID int, resultErr *error) {
	if recovered := recover(); recovered != nil {
		fields := []any{
			"operation", operation,
			"resource_id", resourceID,
			"panic", log.SafeText(fmt.Sprint(recovered)),
			"stack", string(debug.Stack()),
		}
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, gorm.ErrInvalidTransaction) {
			fields = append(fields, "rollback_error", log.SafeError(err))
		}
		log.Errorw("transaction.panic", fields...)
		*resultErr = fmt.Errorf("%s interrupted by an internal panic", operation)
	}
}
