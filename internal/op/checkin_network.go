package op

import (
	"context"
	"errors"

	"github.com/suprelory/octopus-plus/internal/model"
)

// CheckinProxyURL reads availability from the injected database on every new
// operation. A cached HTTP transport must not bypass a disabled pool entry.
func (s *CheckinStore) CheckinProxyURL(ctx context.Context, id int) (string, error) {
	var proxy model.ProxyConfiguration
	if err := s.conn.WithContext(ctx).Select("id", "url", "enabled").First(&proxy, id).Error; err != nil {
		return "", errors.New("checkin proxy is unavailable")
	}
	if !proxy.Enabled {
		return "", errors.New("checkin proxy is disabled")
	}
	return proxy.URL, nil
}
