//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook entry with a retry_guard: the first run is unguarded; after a
// timeout stop, --continue runs the guard and keeps the entry back (same
// timeout stop) while the guard exits non-zero; once the guard passes the
// entry runs and the land completes. No check is skipped.
func TestRetryGuardHoldsBackARetryAfterATimeoutStop(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape: ShapeAhead, Layout: srcOnBr,
		Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\n" +
			"pre-merge-verify = [{ run = [\"{hooks}/gate\"], timeout = \"1s\", retry_guard = [\"{hooks}/guard\"] }]\n",
		Hooks: map[string]string{
			"gate": `echo run >> "$PGL_MARKERS/gate-runs"
if [ "$(wc -l < "$PGL_MARKERS/gate-runs")" -le 1 ]; then exec sleep 30; fi`,
			"guard": `echo run >> "$PGL_MARKERS/guard-runs"
[ -e "$PGL_MARKERS/quiet" ]`,
		},
	})
	count := func(name string) int {
		b, _ := os.ReadFile(filepath.Join(h.marks, name))
		return strings.Count(string(b), "run")
	}

	expect(t, h.run("land", "feature/x"), 21, "stopped", "timeout")
	if count("gate-runs") != 1 || count("guard-runs") != 0 {
		t.Fatalf("first run: gate %d guard %d", count("gate-runs"), count("guard-runs"))
	}

	expect(t, h.run("land", "feature/x", "--continue"), 21, "stopped", "timeout")
	if count("gate-runs") != 1 || count("guard-runs") != 1 {
		t.Fatalf("held back: gate %d guard %d", count("gate-runs"), count("guard-runs"))
	}

	writeFile(t, filepath.Join(h.marks, "quiet"), "")
	done(t, h, h.run("land", "feature/x", "--continue"), landedPastGate)
	if count("gate-runs") != 2 || count("guard-runs") != 2 {
		t.Fatalf("retry: gate %d guard %d", count("gate-runs"), count("guard-runs"))
	}
}
