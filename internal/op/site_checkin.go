package op

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func SiteCheckinLogList(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinLogPage, error) {
	return checkinLogList(db.GetDB(), ctx, filter)
}

// SiteCheckinStats aggregates check-in outcomes and rewards from the durable
// execution log. The query is paged by snowflake ID so large histories do not
// require one unbounded result set.
func SiteCheckinStats(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinStats, error) {
	return siteCheckinStatsAt(ctx, filter, time.Now())
}

func siteCheckinStatsAt(ctx context.Context, filter SiteCheckinLogFilter, now time.Time) (*model.SiteCheckinStats, error) {
	return checkinStatsAt(db.GetDB(), &model.Site{}, ctx, filter, now)
}
