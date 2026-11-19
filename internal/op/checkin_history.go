package op

import (
	"context"
	"github.com/suprelory/octopus-plus/internal/model"
	"time"
)

func (s *CheckinStore) ListHistory(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinLogPage, error) {
	return checkinLogList(s.conn, ctx, filter)
}

func (s *CheckinStore) RewardStats(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinStats, error) {
	return checkinStatsAt(s.conn, &model.CheckinSite{}, ctx, filter, time.Now())
}
