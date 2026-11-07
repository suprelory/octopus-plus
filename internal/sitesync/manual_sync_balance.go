package sitesync

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func parseManualAccountBalance(platform model.SitePlatform, value any) (*float64, *float64, *float64) {
	for _, payload := range manualAccountPayloadCandidates(value) {
		balance, balanceUsed, todayIncome := parseManualAccountBalanceMap(platform, payload)
		if balance != nil || balanceUsed != nil || todayIncome != nil {
			return balance, balanceUsed, todayIncome
		}
	}
	return nil, nil, nil
}

func manualAccountPayloadCandidates(value any) []map[string]any {
	payload, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, 5)
	for _, candidate := range []any{
		nestedValue(payload, "data", "user"),
		nestedValue(payload, "data", "account"),
		payload["data"],
		payload["user"],
		payload["account"],
		payload,
	} {
		if item, ok := candidate.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func parseManualAccountBalanceMap(platform model.SitePlatform, payload map[string]any) (*float64, *float64, *float64) {
	var balance, balanceUsed, todayIncome *float64
	if value, ok := manualFloatFromMap(payload, "balance", "remaining_balance", "remainingBalance"); ok {
		balance = floatPointer(value)
	}
	if value, ok := manualFloatFromMap(payload, "balance_used", "balanceUsed", "used_balance", "usedBalance", "total_spent", "totalSpent"); ok {
		balanceUsed = floatPointer(value)
	}
	if value, ok := manualFloatFromMap(payload, "today_income", "todayIncome"); ok {
		todayIncome = floatPointer(value)
	}

	quota, hasQuota := manualFloatFromMap(payload, "quota")
	usedQuota, hasUsedQuota := manualFloatFromMap(payload, "used_quota", "usedQuota")
	if hasQuota {
		quotaIsRemaining := platform == model.SitePlatformNewAPI || platform == model.SitePlatformAnyRouter || platform == model.SitePlatformDoneHub
		value := quota
		if !quotaIsRemaining && hasUsedQuota {
			value = math.Max(quota-usedQuota, 0)
		}
		balance = floatPointer(value / siteBalanceQuotaPerUSD)
	}
	if hasUsedQuota {
		balanceUsed = floatPointer(usedQuota / siteBalanceQuotaPerUSD)
	}
	if raw, ok := payload["today_income"]; ok && hasQuota {
		if value, valid := manualFloat(raw); valid {
			todayIncome = floatPointer(value / siteBalanceQuotaPerUSD)
		}
	}
	return balance, balanceUsed, todayIncome
}

func manualFloatFromMap(payload map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if raw, ok := payload[key]; ok {
			if value, valid := manualFloat(raw); valid {
				return value, true
			}
		}
	}
	return 0, false
}

func manualFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		result := float64(typed)
		return result, !math.IsNaN(result) && !math.IsInf(result, 0)
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil && !math.IsNaN(result) && !math.IsInf(result, 0)
	case string:
		result, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return result, err == nil && !math.IsNaN(result) && !math.IsInf(result, 0)
	default:
		return 0, false
	}
}

func validateManualBalanceValues(values ...*float64) error {
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return manualSyncInvalid("余额字段必须是有限数字")
		}
	}
	return nil
}

func floatPointer(value float64) *float64 {
	copy := value
	return &copy
}
