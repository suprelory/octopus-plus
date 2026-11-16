package sitesync

import (
	"context"
	"encoding/json"

	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	checkinStateChecked   = rules.Checked
	checkinStateUnchecked = rules.Unchecked
	checkinStateUnknown   = rules.Unknown
)

func TestCheckinStatusRule(ctx context.Context, config *model.SiteCheckinStatusConfig, status int, body json.RawMessage, date string) (string, error) {
	return rules.TestCheckinStatusRule(ctx, config, status, body, date)
}

func statusRuleResponse(response *configuredCheckinResponse) *rules.Response {
	if response == nil {
		return nil
	}
	return &rules.Response{StatusCode: response.StatusCode, Header: response.Header, Body: response.Body}
}

func evaluateCheckinStatus(ctx context.Context, config *model.SiteCheckinStatusConfig, response *configuredCheckinResponse, date string) (string, error) {
	if response != nil && IsCloudflareProtectionResponse(response.StatusCode, response.Header, response.Body) {
		return checkinStateUnknown, nil
	}
	return rules.Evaluate(ctx, config, statusRuleResponse(response), date)
}

func extractStatusCheckinReward(ctx context.Context, site *model.Site, submit, status *configuredCheckinResponse) string {
	return rules.Reward(ctx, site.CheckinRewardExtractor, site.CheckinStatus.RewardSource, statusRuleResponse(submit), statusRuleResponse(status))
}

func ExtractCheckinRewardResponses(ctx context.Context, code string, submit, status any) (string, error) {
	return rules.ExtractCheckinRewardResponses(ctx, code, submit, status)
}
