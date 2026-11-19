package finance

import (
	"math"
	"testing"
)

func TestRewardUsageAndUnits(t *testing.T) {
	for _, test := range []struct {
		before, after Balance
		want          string
	}{
		{Balance{Value: 1}, Balance{Value: 1}, "0"},
		{Balance{Value: 1, Used: 2, UsedKnown: true}, Balance{Value: .9, Used: 2.2, UsedKnown: true}, "0.1"},
		{Balance{Value: 1, Used: 2, UsedKnown: true}, Balance{Value: 1.2, Used: 0, UsedKnown: true}, "0.2"},
		{Balance{Value: 1}, Balance{Value: .9}, ""},
		{Balance{Value: math.Inf(1)}, Balance{Value: 1}, ""},
	} {
		if got := Reward(test.before, test.after); got != test.want {
			t.Fatalf("reward=%s want=%s", got, test.want)
		}
	}
	for _, value := range []any{nil, "NaN", "Inf", math.NaN(), true} {
		if _, ok := Number(value); ok {
			t.Fatalf("accepted invalid number %v", value)
		}
	}
}
