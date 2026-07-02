package budget

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/usage"
)

func mk(tok, cost Limit, tm time.Duration) Budget {
	return Budget{
		Tokens: tok, Cost: cost, Time: tm,
		Thresholds: Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
		Prices:     usage.PriceTable{"_default": {OutputPerMTok: 75}},
	}
}

func TestLimitUnlimited(t *testing.T) {
	if !Limit(0).Unlimited() || !Limit(-1).Unlimited() || Limit(1).Unlimited() {
		t.Error("Unlimited semantics wrong")
	}
}

func TestEvaluate_levels(t *testing.T) {
	b := mk(1000, 0, 0) // token-only cap of 1000
	cases := []struct {
		out  int
		want Level
	}{
		{700, None}, {725, Reminder}, {900, Cancel}, {1000, Hard}, {5000, Hard},
	}
	for _, c := range cases {
		_, lvl := b.Evaluate(usage.Snapshot{OutputTokens: c.out}, 0)
		if lvl != c.want {
			t.Errorf("tokens=%d -> %v, want %v", c.out, lvl, c.want)
		}
	}
}

func TestEvaluate_maxAcrossDimensions(t *testing.T) {
	b := mk(1000, 0, 10*time.Minute) // tokens 1000, time 10m
	// tokens 50% but time 95% -> Cancel (max)
	_, lvl := b.Evaluate(usage.Snapshot{OutputTokens: 500}, 95*time.Minute/10)
	if lvl != Cancel {
		t.Errorf("max%% should pick time (Cancel), got %v", lvl)
	}
}

func TestEvaluate_unlimitedContributesZero(t *testing.T) {
	b := mk(0, 0, 0) // everything unlimited
	pct, lvl := b.Evaluate(usage.Snapshot{OutputTokens: 1 << 30}, 1000*time.Hour)
	if lvl != None || pct != 0 {
		t.Errorf("fully unlimited -> None/0, got %v/%v", lvl, pct)
	}
}

func TestPromptLine_omitsUnlimited(t *testing.T) {
	if s := mk(0, 0, 0).PromptLine(); s != "" {
		t.Errorf("fully unlimited PromptLine should be empty, got %q", s)
	}
	s := mk(0, 0, 25*time.Minute).PromptLine()
	if s == "" {
		t.Error("time-limited budget should produce a prompt line")
	}
}
