package parity

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// oldWantKinds is, per scenario, the set of ledger kinds the OLD sync stage
// leaves as planned (not-yet-applied) rows. A write that only adopts an existing
// bead carries that bead's id and so is not a planned row: scenarios 09 to 14
// start from existing beads and plan nothing new. Scenario 15 lists only the
// open beads, as production does, so the old sync cannot see the two closed
// ones and plans a fresh cycle and review request.
var oldWantKinds = map[string][]string{
	"01-": {"anchor", "feedback-cycle", "review-request"},
	"02-": {"anchor", "review-request"},
	"03-": {"anchor", "feedback-cycle", "review-request"},
	"04-": {"anchor", "feedback-cycle"},
	"05-": {"anchor", "feedback-cycle", "review-request"},
	"06-": {"anchor", "review-request"},
	"07-": {"anchor", "review-request"},
	"08-": {"anchor", "review-request"},
	"09-": nil,
	"10-": nil,
	"11-": nil,
	"12-": nil,
	"13-": nil,
	"14-": nil,
	"15-": {"feedback-cycle", "review-request"},
	// The focus scenarios: the old sync has no focus bead, and a draft team PR
	// with no comments plans nothing else.
	"16-": nil,
	"17-": nil,
	"18-": nil,
}

func wantFor[T any](t *testing.T, table map[string]T, name string) T {
	t.Helper()
	for prefix, v := range table {
		if strings.HasPrefix(name, prefix) {
			return v
		}
	}
	t.Fatalf("scenario %s has no expectation in the table; add one", name)
	var zero T
	return zero
}

func TestRunOldPlansWhatSyncPlans(t *testing.T) {
	env := realEnv(t)
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scs {
		t.Run(sc.Name, func(t *testing.T) {
			want := wantFor(t, oldWantKinds, sc.Name)
			res, err := RunOld(context.Background(), env, sc)
			if err != nil {
				t.Fatalf("RunOld: %v", err)
			}
			for _, id := range sc.Entities {
				rows, ok := res.Rows[id]
				if !ok {
					t.Fatalf("no entry for %s: %v", id, res.Rows)
				}
				var kinds []string
				for _, r := range rows {
					if r.ContentHash == "" {
						t.Errorf("%s row %s has no content hash", id, r.Kind)
					}
					kinds = append(kinds, r.Kind)
				}
				sort.Strings(kinds)
				if !slices.Equal(want, kinds) {
					t.Errorf("%s planned kinds = %v, want %v", id, kinds, want)
				}
			}
		})
	}
}

func TestRunOldIsDeterministic(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "01-")
	a, err := RunOld(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunOld(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two runs of one scenario differ:\n%v\n%v", a, b)
	}
}

// The runs must touch nothing outside Env.TempDir and reach only the fake
// pg-connector, whatever the caller's own environment names.
func TestRunOldIsHermetic(t *testing.T) {
	env := realEnv(t)
	realState, realHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", realState)
	t.Setenv("HOME", realHome)
	t.Setenv("PG_DESK_CONFIG", filepath.Join(realHome, "no-such-config.yaml"))

	sc := scenarioByPrefix(t, "10-")
	if _, err := RunOld(context.Background(), env, sc); err != nil {
		t.Fatalf("RunOld: %v", err)
	}
	for _, dir := range []string{realState, realHome} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s was written to: %v", dir, entries)
		}
	}
	assertRunArtifacts(t, env.TempDir, "old-", []string{"pr show acme/api#110 --fresh", "issue list --query work-beads"})
}

// assertRunArtifacts finds the single run directory under temp with the given
// prefix and checks its store and the fake connector's call log.
func assertRunArtifacts(t *testing.T, temp, prefix string, wantCalls []string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(temp, prefix+"*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one %s* run dir under %s, got %v (%v)", prefix, temp, matches, err)
	}
	run := matches[0]
	if _, err := os.Stat(filepath.Join(run, "state", "pg-desk", "store.db")); err != nil {
		t.Errorf("the store is not under the run dir: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(run, "connector-calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(log), "\n")
	for _, w := range wantCalls {
		if !contains(lines, w) {
			t.Errorf("the fake pg-connector never saw %q; calls:\n%s", w, log)
		}
	}
	if strings.Contains(string(log), "unsupported") {
		t.Errorf("call log mentions an unsupported call:\n%s", log)
	}
}
