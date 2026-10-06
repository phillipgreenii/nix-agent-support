package usagelimit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestEvaluate(t *testing.T) {
	future := now.Add(2 * time.Hour).Format(time.RFC3339)
	later := now.Add(48 * time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Minute).Format(time.RFC3339)
	for _, tc := range []struct {
		name      string
		doc       string
		threshold float64
		want      *Limit
	}{
		{"no rate_limits key", `{"sessions":[]}`, 100, nil},
		{"empty object", `{}`, 100, nil},
		{
			"five hour hit", `{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + future + `"}}}`, 100,
			&Limit{FiveHour, 100, now.Add(2 * time.Hour)},
		},
		{
			"seven day hit", `{"rate_limits":{"seven_day":{"used_pct":100,"resets_at":"` + later + `"}}}`, 100,
			&Limit{SevenDay, 100, now.Add(48 * time.Hour)},
		},
		{"just under threshold", `{"rate_limits":{"five_hour":{"used_pct":99.9,"resets_at":"` + future + `"}}}`, 100, nil},
		{
			"threshold is inclusive", `{"rate_limits":{"five_hour":{"used_pct":95,"resets_at":"` + future + `"}}}`, 95,
			&Limit{FiveHour, 95, now.Add(2 * time.Hour)},
		},
		{"hit but already reset", `{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + past + `"}}}`, 100, nil},
		{"hit and reset exactly now is clear", `{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + now.Format(time.RFC3339) + `"}}}`, 100, nil},
		{"hit without reset cannot be said to clear", `{"rate_limits":{"five_hour":{"used_pct":100}}}`, 100, nil},
		{"unparseable reset", `{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"soon"}}}`, 100, nil},
		{"reset without percentage is unknown not zero", `{"rate_limits":{"five_hour":{"resets_at":"` + future + `"}}}`, 100, nil},
		{
			"both hit: the later reset binds",
			`{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + future + `"},"seven_day":{"used_pct":100,"resets_at":"` + later + `"}}}`, 100,
			&Limit{SevenDay, 100, now.Add(48 * time.Hour)},
		},
		{
			"both hit, five hour clears later",
			`{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + later + `"},"seven_day":{"used_pct":100,"resets_at":"` + future + `"}}}`, 100,
			&Limit{FiveHour, 100, now.Add(48 * time.Hour)},
		},
		{
			"only the hit window counts",
			`{"rate_limits":{"five_hour":{"used_pct":10,"resets_at":"` + future + `"},"seven_day":{"used_pct":100,"resets_at":"` + later + `"}}}`, 100,
			&Limit{SevenDay, 100, now.Add(48 * time.Hour)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Evaluate([]byte(tc.doc), tc.threshold, now)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %+v, want not blocked", got)
			case tc.want != nil && got == nil:
				t.Fatalf("got not blocked, want %+v", tc.want)
			case tc.want != nil && (got.Window != tc.want.Window || got.UsedPct != tc.want.UsedPct || !got.ResetsAt.Equal(tc.want.ResetsAt)):
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEvaluate_malformedJSONIsAnError(t *testing.T) {
	if _, err := Evaluate([]byte("not json"), 100, now); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestGate_check(t *testing.T) {
	future := now.Add(time.Hour).Format(time.RFC3339)
	var gotName string
	var gotArgs []string
	g := Gate{
		Command: "pa-monitor", ThresholdPct: 100, Timeout: time.Second,
		Now: func() time.Time { return now },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return []byte(`{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"` + future + `"}}}`), nil
		},
	}
	l, err := g.Check(context.Background())
	if err != nil || l == nil || l.Window != FiveHour {
		t.Fatalf("Check = %+v, %v; want a five_hour limit", l, err)
	}
	if gotName != "pa-monitor" || strings.Join(gotArgs, " ") != "status --json" {
		t.Errorf("ran %q %v, want pa-monitor status --json", gotName, gotArgs)
	}
}

func TestGate_runnerFailureIsUnknownNotBlocked(t *testing.T) {
	g := Gate{Command: "pa-monitor", ThresholdPct: 100, Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("daemon unreachable")
	}}
	l, err := g.Check(context.Background())
	if l != nil {
		t.Fatalf("a failed query must not block, got %+v", l)
	}
	if err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Fatalf("err = %v, want the runner failure surfaced", err)
	}
}

func TestGate_badOutputIsUnknownNotBlocked(t *testing.T) {
	g := Gate{Command: "pa-monitor", ThresholdPct: 100, Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("daemon: unavailable"), nil
	}}
	if l, err := g.Check(context.Background()); l != nil || err == nil {
		t.Fatalf("Check = %+v, %v; want nil limit and an error", l, err)
	}
}

func TestGate_timeoutBoundsTheQuery(t *testing.T) {
	g := Gate{
		Command: "pa-monitor", ThresholdPct: 100, Timeout: 20 * time.Millisecond,
		Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	start := time.Now()
	if l, err := g.Check(context.Background()); l != nil || err == nil {
		t.Fatalf("Check = %+v, %v; want nil limit and a timeout error", l, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Timeout did not bound the query")
	}
}

func TestOff_neverBlocks(t *testing.T) {
	if l, err := (Off{}).Check(context.Background()); l != nil || err != nil {
		t.Fatalf("Off.Check = %+v, %v; want nil, nil", l, err)
	}
}

func TestExecRunner_missingBinaryIsAnError(t *testing.T) {
	if _, err := ExecRunner(context.Background(), "ccpool-no-such-binary-xyz", "status", "--json"); err == nil {
		t.Fatal("want an error for a missing binary")
	}
}

func TestLimitString(t *testing.T) {
	s := Limit{FiveHour, 100, now}.String()
	for _, want := range []string{"five_hour", "100.0%", "resets at"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}
