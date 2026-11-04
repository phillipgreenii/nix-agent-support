package serve

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// alertsFile is the Grafana alert rules file, relative to this package.
const alertsFile = "../../grafana/alerting/alerts.yaml"

// loopStalledExpr returns the PromQL of the loop-stalled rule's query (refId A): the
// first expr line between its uid line and the next rule's.
func loopStalledExpr(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(alertsFile)
	if err != nil {
		t.Fatal(err)
	}
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "- uid: ") {
			in = trim == "- uid: pg-ccrec-loop-stalled"
			continue
		}
		if in && strings.HasPrefix(trim, "expr: ") {
			return strings.Trim(strings.TrimPrefix(trim, "expr: "), `'"`)
		}
	}
	t.Fatal("no loop-stalled rule with an expr in " + alertsFile)
	return ""
}

// The stall bounds status uses (internal/obs/stall.go, defined once) equal the literals of the
// loop-stalled alert rule: every loop with an age bound appears in the rule with that bound in
// seconds, no loop without one does, and the in-flight literal is InFlightStallBound. The
// watcher_sweep literal assumes the default sweep.interval.
func TestStallBoundsEqualLoopStalledAlertLiterals(t *testing.T) {
	expr := loopStalledExpr(t)
	sweep := config.Defaults(func(string) string { return "" }).SweepInterval
	if sweep != 15*time.Minute {
		t.Fatalf("the default sweep.interval is %v; the alert's watcher_sweep literal assumes 15m", sweep)
	}

	// time() - (loop_last_timestamp_seconds{loop="x"} > 0)) > N, or loop=~"x|y".
	age := regexp.MustCompile(`loop_last_timestamp_seconds\{loop(=~?)"([^"]+)"\} > 0\)\) > (\d+)`)
	alertBound := map[string]int{}
	for _, m := range age.FindAllStringSubmatch(expr, -1) {
		secs, err := strconv.Atoi(m[3])
		if err != nil {
			t.Fatal(err)
		}
		names := []string{m[2]}
		if m[1] == "=~" {
			names = strings.Split(m[2], "|")
		}
		for _, n := range names {
			if _, dup := alertBound[n]; dup {
				t.Fatalf("loop %s has two age bounds in the alert", n)
			}
			alertBound[n] = secs
		}
	}
	if len(alertBound) == 0 {
		t.Fatalf("no age bound found in the alert expression: %s", expr)
	}
	for _, l := range obs.Loops {
		want, ok := obs.StallAgeBound(l, sweep)
		got, inAlert := alertBound[l]
		switch {
		case ok && !inAlert:
			t.Errorf("loop %s has the code bound %v but no age bound in the alert", l, want)
		case !ok && inAlert:
			t.Errorf("loop %s has no code bound but the alert bounds it at %d s", l, got)
		case ok && time.Duration(got)*time.Second != want:
			t.Errorf("loop %s: the alert bound is %d s, the code bound %v", l, got, want)
		}
	}
	for l := range alertBound {
		if !slices.Contains(obs.Loops, l) {
			t.Errorf("the alert bounds %q, which is not a loop", l)
		}
	}

	inFlight := regexp.MustCompile(`pg_claude_code_recorder_loop_in_flight_seconds > (\d+)`).FindStringSubmatch(expr)
	if inFlight == nil {
		t.Fatalf("no loop_in_flight_seconds bound (without a loop selector, so it covers every loop) in: %s", expr)
	}
	secs, _ := strconv.Atoi(inFlight[1])
	if time.Duration(secs)*time.Second != obs.InFlightStallBound {
		t.Errorf("the alert in-flight bound is %d s, the code bound %v", secs, obs.InFlightStallBound)
	}
}
