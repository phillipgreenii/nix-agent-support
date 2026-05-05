package ccusage

import "testing"

func TestPlanCapUSD(t *testing.T) {
	cases := map[string]float64{
		"pro":     18,
		"max_5x":  90,
		"max_20x": 360,
	}
	for tier, want := range cases {
		if got := PlanCapUSD(tier); got != want {
			t.Errorf("PlanCapUSD(%q) = %v, want %v", tier, got, want)
		}
	}
	if got := PlanCapUSD("unknown"); got != 0 {
		t.Errorf("unknown tier should return 0, got %v", got)
	}
}
