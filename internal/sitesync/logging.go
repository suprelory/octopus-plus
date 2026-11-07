package sitesync

import (
	"context"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

func SyncAccountWithTrigger(ctx context.Context, accountID int, trigger string) (*model.SiteSyncResult, error) {
	started := time.Now()
	diagnostic := &syncLogContext{}
	result, err := runAccountSync(ctx, accountID, diagnostic)
	err = diagnostic.safeError(err)
	logSyncResult(accountID, trigger, result, err, started, diagnostic)
	return result, err
}

type syncLogContext struct {
	site     *model.Site
	account  *model.SiteAccount
	snapshot *syncSnapshot
}

// Preserve the public error and its classification while giving request and
// background loggers the same account-aware diagnostic as the sync summary.
func (d *syncLogContext) safeError(err error) error {
	if err == nil {
		return nil
	}
	return apperror.Wrap(apperror.Code(err), apperror.Message(err), err).
		WithStatus(apperror.Status(err)).WithParams(apperror.Params(err)).
		WithLogMessage(d.safeText(log.SafeError(err)))
}

func (d *syncLogContext) safeText(value string) string {
	if d.account != nil {
		for _, token := range d.account.Tokens {
			if token.Token != "" {
				value = strings.ReplaceAll(value, token.Token, "[REDACTED]")
			}
		}
		if d.snapshot != nil {
			for _, token := range d.snapshot.tokens {
				if token.Token != "" {
					value = strings.ReplaceAll(value, token.Token, "[REDACTED]")
				}
			}
		}
		value = sanitizeCheckinText(value, d.account, snapshotAccessToken(d.snapshot), d.site)
	}
	return log.SafeText(sanitizeSiteStatusText(value))
}

func logSyncResult(accountID int, trigger string, result *model.SiteSyncResult, err error, started time.Time, diagnostic *syncLogContext) {
	status := model.SiteExecutionStatusFailed
	fields := []any{"account_id", accountID, "trigger", trigger, "duration_ms", time.Since(started).Milliseconds()}
	if diagnostic.site != nil {
		fields = append(fields, "site_id", diagnostic.site.ID, "platform", string(diagnostic.site.Platform))
	}
	if result != nil {
		status = result.Status
		fields = append(fields, "channels", result.ChannelCount,
			"groups", result.GroupCount, "tokens", result.TokenCount, "models", result.ModelCount,
			"message", diagnostic.safeText(result.Message))
	}
	fields = append(fields, "status", string(status))
	if err != nil {
		fields = append(fields, "reason", string(siteBatchReason(err)), "error", diagnostic.safeText(log.SafeError(err)))
	}
	if err != nil || status == model.SiteExecutionStatusFailed || status == model.SiteExecutionStatusPartial {
		log.Warnw("sitesync.sync.complete", fields...)
	} else {
		log.Infow("sitesync.sync.complete", fields...)
	}
}

func logSiteDataWarning(site *model.Site, account *model.SiteAccount, accessToken, step, reason string, err error) {
	fields := []any{"step", step, "reason", reason}
	if site != nil {
		fields = append(fields, "site_id", site.ID, "platform", string(site.Platform))
	}
	if account != nil {
		fields = append(fields, "account_id", account.ID)
	}
	if err != nil {
		if account == nil {
			account = &model.SiteAccount{}
		}
		fields = append(fields, "error", log.SafeText(sanitizeCheckinText(err.Error(), account, accessToken, site)))
	}
	log.Warnw("sitesync.data_warning", fields...)
}
