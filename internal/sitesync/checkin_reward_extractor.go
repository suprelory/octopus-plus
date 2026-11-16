package sitesync

import (
	"context"
	"encoding/json"

	"github.com/suprelory/octopus-plus/internal/checkin/rules"
)

// Retained for legacy callers until runtime cutover; both paths use one sandbox.
func ExtractCheckinReward(ctx context.Context, code string, response json.RawMessage) (string, error) {
	return rules.ExtractCheckinReward(ctx, code, response)
}
