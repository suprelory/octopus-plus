package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	checkinStateChecked   = "checked_in"
	checkinStateUnchecked = "not_checked_in"
	checkinStateUnknown   = "unknown"
)

// Preview evaluates user-provided JSON only; it never performs network requests.
func TestCheckinStatusRule(ctx context.Context, config *model.SiteCheckinStatusConfig, status int, body json.RawMessage, date string) (string, error) {
	if err := config.Validate(); err != nil {
		return "", err
	}
	if status < 100 || status > 599 || len(body) > siteCheckinResponseLimit || !json.Valid(body) {
		return "", fmt.Errorf("invalid HTTP status or JSON sample")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", fmt.Errorf("checkin date must use YYYY-MM-DD")
	}
	return evaluateCheckinStatus(ctx, config, &configuredCheckinResponse{StatusCode: status, Body: body}, date)
}

func evaluateCheckinStatus(ctx context.Context, config *model.SiteCheckinStatusConfig, response *configuredCheckinResponse, date string) (string, error) {
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return checkinStateUnknown, nil
	}
	if IsCloudflareProtectionResponse(response.StatusCode, response.Header, response.Body) {
		return checkinStateUnknown, nil
	}
	payload, ok := parseSiteJSONMap(response.Body)
	if !ok || payload == nil {
		return checkinStateUnknown, nil
	}
	if config.Code != "" {
		code, _ := json.Marshal("\"use strict\";\n" + config.Code)
		input, _ := json.Marshal(map[string]any{"body": payload, "context": map[string]any{"httpStatus": response.StatusCode, "checkinDate": date}})
		// Reuse the same isolated runtime, time/memory limits and output validation.
		result, err := runCheckinRewardScript(ctx, `const state = new Function("response", "context", `+string(code)+`)(response.body, response.context);
			if (state === "checked_in") return 1;
			if (state === "not_checked_in") return 0;
			if (state === "unknown") return 2;
			throw new Error("Invalid state");`, input)
		if err != nil {
			return checkinStateUnknown, fmt.Errorf("状态判定代码执行失败，请检查返回值和资源限制")
		}
		switch result {
		case "1":
			return checkinStateChecked, nil
		case "0":
			return checkinStateUnchecked, nil
		default:
			return checkinStateUnknown, nil
		}
	}
	if success, present := payload["success"]; present && success != nil && !jsonBool(success) {
		return checkinStateUnknown, nil
	}
	if config.DatePath != "" && statusField(payload, config.DatePath) != date {
		return checkinStateUnknown, nil
	}
	actual := statusField(payload, config.CheckedPath)
	if actual == nil {
		return checkinStateUnknown, nil
	}
	var checked, unchecked any
	_ = json.Unmarshal(config.CheckedValue, &checked)
	_ = json.Unmarshal(config.UncheckedValue, &unchecked)
	if reflect.DeepEqual(actual, checked) {
		return checkinStateChecked, nil
	}
	if reflect.DeepEqual(actual, unchecked) {
		return checkinStateUnchecked, nil
	}
	return checkinStateUnknown, nil
}

func statusField(value any, path string) any {
	for _, key := range strings.Split(path, ".") {
		switch current := value.(type) {
		case map[string]any:
			value = current[key]
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(current) {
				return nil
			}
			value = current[index]
		default:
			return nil
		}
	}
	return value
}

func extractStatusCheckinReward(ctx context.Context, site *model.Site, submit, status *configuredCheckinResponse) string {
	jsonBody := func(response *configuredCheckinResponse) any {
		if response == nil {
			return nil
		}
		payload, _ := parseSiteJSONMap(response.Body)
		return payload
	}
	submitBody, statusBody := jsonBody(submit), jsonBody(status)
	selected := submitBody
	if site.CheckinStatus.RewardSource == "status" {
		selected = statusBody
	}
	reward := checkinRewardString(statusField(selected, "data.reward"))
	if site.CheckinRewardExtractor != "" {
		value, err := ExtractCheckinRewardResponses(ctx, site.CheckinRewardExtractor, submitBody, statusBody)
		if err == nil && value != "" {
			reward = value
		}
	}
	return reward
}

func ExtractCheckinRewardResponses(ctx context.Context, code string, submit, status any) (string, error) {
	if err := model.ValidateCheckinRewardExtractor(code); err != nil {
		return "", err
	}
	body, _ := json.Marshal("\"use strict\";\n" + code)
	input, err := json.Marshal(map[string]any{"submit": submit, "status": status})
	if err != nil {
		return "", fmt.Errorf("invalid response samples")
	}
	// response retains its legacy meaning, even when the default source is status.
	return runCheckinRewardScript(ctx, `return new Function("response", "responses", `+string(body)+`)(response.submit, response);`, input)
}
