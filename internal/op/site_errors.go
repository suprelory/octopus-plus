package op

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

const (
	CodeSiteNameExists         = "site.name_exists"
	CodeSiteArchivedNameExists = "site.archived_name_exists"
	CodeSiteCredentialConflict = "site.account.credential_conflict"
)

func siteCredentialConflictError() *apperror.Error {
	return apperror.New(CodeSiteCredentialConflict, "账号凭据已更新或缺少凭据版本，请刷新页面并重新打开编辑窗口后再保存").WithStatus(http.StatusConflict)
}

// Resolve name conflicts after the write has rolled back, including concurrent
// creates. Other unique constraints and database errors keep their original error.
func siteNameConflictError(err error, name string, kind model.SiteKind, excludeID int, ctx context.Context) error {
	if err == nil {
		return nil
	}
	gormDB := db.GetDB()
	translated := err
	if translator, ok := gormDB.Dialector.(gorm.ErrorTranslator); ok {
		translated = translator.Translate(err)
	}
	if !errors.Is(translated, gorm.ErrDuplicatedKey) {
		return err
	}

	var existing model.Site
	query := gormDB.WithContext(ctx).Select("id", "name", "archived").Where("name = ? AND kind = ?", name, kind)
	if excludeID > 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if lookupErr := query.Take(&existing).Error; lookupErr != nil {
		return err
	}

	code := CodeSiteNameExists
	message := fmt.Sprintf("A site named %q already exists. Choose a different name.", existing.Name)
	if existing.Archived {
		code = CodeSiteArchivedNameExists
		message = fmt.Sprintf("An archived site named %q already exists. Restore it from View options > Global actions > Archived sites in the top right, or choose a different name.", existing.Name)
	}
	return apperror.Wrap(code, message, err).
		WithStatus(http.StatusConflict).
		WithParams(map[string]any{"name": existing.Name, "site_id": existing.ID})
}
