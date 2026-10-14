package controller

import (
	"math"
	"testing"
)

// [patch lou #8b] pattern matrix for clineRemainingCNY:
// monthly cap $50×7=¥350, weekly cap $25×7=¥175, balance=min(monthly, weekly),
// missing weekly falls back to monthly-only; five-hour tier is not a money
// cap but a fully-used five-hour tier forces balance to 0.
func TestClineRemainingCNY(t *testing.T) {
	pct := func(v float64) *float64 { return &v }
	cases := []struct {
		name     string
		monthly  *float64
		weekly   *float64
		fiveHour *float64
		want     float64
	}{
		{"monthly smaller (real ch21 data 10-14)", pct(59), pct(13), pct(9), 143.5}, // weekly 152.25 > monthly 143.5; five_hour 91% not exhausted
		{"weekly smaller", pct(20), pct(90), nil, 17.5},                             // monthly 280 > weekly 17.5
		{"weekly missing falls back to monthly", pct(59), nil, nil, 143.5},          // #8 legacy behavior
		{"monthly exhausted", pct(100), pct(50), nil, 0},
		{"monthly exhausted no weekly", pct(100), nil, nil, 0},
		{"both idle -> weekly cap governs", pct(0), pct(0), pct(0), 175},           // 350 vs 175
		{"five_hour exhausted forces zero", pct(30), pct(10), pct(100), 0},         // channel unusable now
		{"five_hour exhausted, monthly plenty", pct(0), nil, pct(100), 0},
		{"five_hour near-exhausted does not interfere", pct(59), pct(13), pct(99), 143.5},
	}
	for _, c := range cases {
		got := clineRemainingCNY(c.monthly, c.weekly, c.fiveHour)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
