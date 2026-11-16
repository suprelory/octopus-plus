package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	Checked   = "checked_in"
	Unchecked = "not_checked_in"
	Unknown   = "unknown"
)

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Preview evaluates user-provided JSON only; it never performs network requests.
func TestCheckinStatusRule(ctx context.Context, config *model.SiteCheckinStatusConfig, status int, body json.RawMessage, date string) (string, error) {
	if err := config.Validate(); err != nil {
		return "", err
	}
	if status < 100 || status > 599 || len(body) > ResponseLimit || !json.Valid(body) {
		return "", fmt.Errorf("invalid HTTP status or JSON sample")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", fmt.Errorf("checkin date must use YYYY-MM-DD")
	}
	return Evaluate(ctx, config, &Response{StatusCode: status, Body: body}, date)
}

func Evaluate(ctx context.Context, config *model.SiteCheckinStatusConfig, response *Response, date string) (string, error) {
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return Unknown, nil
	}
	if strings.EqualFold(response.Header.Get("Cf-Mitigated"), "challenge") {
		return Unknown, nil
	}
	payload, ok := parseSiteJSONMap(response.Body)
	if !ok || payload == nil {
		return Unknown, nil
	}
	if config.Code != "" {
		code, _ := json.Marshal("\"use strict\";\n" + config.Code)
		input, _ := json.Marshal(map[string]any{"body": payload, "context": map[string]any{"httpStatus": response.StatusCode, "checkinDate": date}})
		// Reuse the same isolated runtime, time/memory limits and output validation.
		result, err := Run(ctx, `const state = new Function("response", "context", `+string(code)+`)(response.body, response.context);
			if (state === "checked_in") return 1;
			if (state === "not_checked_in") return 0;
			if (state === "unknown") return 2;
			throw new Error("Invalid state");`, input)
		if err != nil {
			return Unknown, fmt.Errorf("状态判定代码执行失败，请检查返回值和资源限制")
		}
		switch result {
		case "1":
			return Checked, nil
		case "0":
			return Unchecked, nil
		default:
			return Unknown, nil
		}
	}
	if success, present := payload["success"]; present && success != nil && success != true {
		return Unknown, nil
	}
	if config.DatePath != "" && statusField(payload, config.DatePath) != date {
		return Unknown, nil
	}
	actual := statusField(payload, config.CheckedPath)
	if actual == nil {
		return Unknown, nil
	}
	var checked, unchecked any
	_ = json.Unmarshal(config.CheckedValue, &checked)
	_ = json.Unmarshal(config.UncheckedValue, &unchecked)
	if reflect.DeepEqual(actual, checked) {
		return Checked, nil
	}
	if reflect.DeepEqual(actual, unchecked) {
		return Unchecked, nil
	}
	return Unknown, nil
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

func Reward(ctx context.Context, code, source string, submit, status *Response) string {
	jsonBody := func(response *Response) any {
		if response == nil {
			return nil
		}
		payload, _ := parseSiteJSONMap(response.Body)
		return payload
	}
	submitBody, statusBody := jsonBody(submit), jsonBody(status)
	selected := submitBody
	if source == "status" {
		selected = statusBody
	}
	reward := rewardString(statusField(selected, "data.reward"))
	if code != "" {
		value, err := ExtractCheckinRewardResponses(ctx, code, submitBody, statusBody)
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
	return Run(ctx, `return new Function("response", "responses", `+string(body)+`)(response.submit, response);`, input)
}

func parseSiteJSONMap(body []byte) (map[string]any, bool) {
	if len(body) == 0 {
		return map[string]any{}, true
	}
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	return payload, err == nil && payload != nil
}

func rewardString(value any) string {
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
