// Package finance contains platform-independent balance and reward arithmetic.
package finance

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const QuotaPerUSD = 500000.0

type Balance struct {
	Value       float64
	Used        float64
	Income      float64
	UsedKnown   bool
	IncomeKnown bool
}

func Number(value any) (float64, bool) {
	var number float64
	var err error
	switch value := value.(type) {
	case float64:
		number = value
	case json.Number:
		number, err = value.Float64()
	case string:
		number, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
	default:
		return 0, false
	}
	return number, err == nil && Finite(number)
}

func Finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func Quota(data map[string]any, scale float64, remaining bool) (Balance, bool) {
	quota, known := Number(data["quota"])
	used, usedKnown := Number(data["used_quota"])
	if !known || !remaining && !usedKnown || !Finite(scale) || scale <= 0 {
		return Balance{}, false
	}
	if !remaining {
		quota = math.Max(0, quota-used)
	}
	income, incomeKnown := Number(data["today_income"])
	result := Balance{Value: quota / scale, Used: used / scale, Income: income / scale, UsedKnown: usedKnown, IncomeKnown: incomeKnown}
	result.UsedKnown = result.UsedKnown && Finite(result.Used)
	result.IncomeKnown = result.IncomeKnown && Finite(result.Income)
	return result, Finite(result.Value)
}

// Reward accounts for consumption between two fresh observations. A reset of
// the cumulative usage counter cannot establish consumption for that interval.
func Reward(before, after Balance) string {
	delta := after.Value - before.Value
	if before.UsedKnown && after.UsedKnown && Finite(before.Used) && Finite(after.Used) && before.Used >= 0 && after.Used >= before.Used {
		delta += after.Used - before.Used
	}
	if !Finite(delta) || delta < 0 {
		return ""
	}
	value := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(delta, 'f', 6, 64), "0"), ".")
	if value == "-0" {
		value = "0"
	}
	return value
}

var incomeNumber = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)

func Income(content string) float64 {
	value, ok := Number(incomeNumber.FindString(strings.ReplaceAll(content, ",", "")))
	if !ok || value <= 0 {
		return 0
	}
	return value
}
