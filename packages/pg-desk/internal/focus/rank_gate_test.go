package focus

// The standing rank gate (bead pg2-2j5ac.44.13, the daily-focus design's "Rank regression").
//
// The synthetic goldens of rank_golden_test.go pin the keys; this gate pins
// the rank against what a backend really prints. testdata/snapshots/<backend>/
// holds scrubbed copies of the stored facts of each backend (a Jira issue, a
// bd issue, a GitHub PR), loaded into a store and run through Load,
// Candidates and Rank, asserting the exact rank order AND the five
// rank_inputs counts. A drift in a real input (a changed due-date format, a
// timestamp layout the age key cannot read, a missing status category) moves
// a count or the order and fails here, where a hand-written synthetic fixture
// would still pass.
//
// The check `pg-desk-focus-rank-gate` in flake.nix runs ONLY TestFocusRankGate
// and fails unless the log shows `--- PASS: TestFocusRankGate`, so a renamed,
// filtered or skipped test cannot pass vacuously. Keep the name.
//
// Telemetry: the gate emits no OpenTelemetry or Prometheus signal and logs
// nothing beyond go test's own output.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const rankSnapshotDir = "testdata/snapshots"

// gateBackend is one backend whose recorded snapshots the gate runs, and the
// shape every ranked row of its snapshots must have.
type gateBackend struct {
	dir     string
	rowKind func(key string) bool
}

var (
	gateBeadID   = regexp.MustCompile(`^issue:bd-`)
	gateBackends = []gateBackend{
		{"jira", func(k string) bool { return strings.HasPrefix(k, "issue:") && !gateBeadID.MatchString(k) }},
		{"bd", func(k string) bool { return gateBeadID.MatchString(k) }},
		{"github-pr", func(k string) bool { return strings.HasPrefix(k, "pr:") }},
	}
)

// gateMinRows is the least a snapshot set must rank: with fewer the order
// assertion is too weak to catch a swap.
const gateMinRows = 4

func TestFocusRankGate(t *testing.T) {
	// A snapshot directory the gate does not know would be silently
	// unchecked; every directory must be a declared backend.
	entries, err := os.ReadDir(rankSnapshotDir)
	if err != nil {
		t.Fatalf("read %s: %v", rankSnapshotDir, err)
	}
	declared := map[string]bool{}
	for _, b := range gateBackends {
		declared[b.dir] = true
	}
	var onDisk []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		onDisk = append(onDisk, e.Name())
		if !declared[e.Name()] {
			t.Errorf("%s/%s is not a gate backend; declare it in gateBackends or remove it", rankSnapshotDir, e.Name())
		}
	}
	sort.Strings(onDisk)
	for _, b := range gateBackends {
		found := false
		for _, d := range onDisk {
			found = found || d == b.dir
		}
		if !found {
			t.Errorf("backend %s has no snapshot directory under %s", b.dir, rankSnapshotDir)
		}
	}

	for _, b := range gateBackends {
		t.Run(b.dir, func(t *testing.T) {
			files := rankFixtureFiles(t, filepath.Join(rankSnapshotDir, b.dir))
			if len(files) == 0 {
				t.Fatalf("no recorded snapshot under %s/%s", rankSnapshotDir, b.dir)
			}
			for _, f := range files {
				t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) {
					c := readRankCase(t, f)
					got := checkRankCase(t, c)
					if len(got.Order) < gateMinRows {
						t.Errorf("%s ranks only %d rows, want at least %d", f, len(got.Order), gateMinRows)
					}
					for _, k := range got.Order {
						if !b.rowKind(k) {
							t.Errorf("%s: ranked row %s is not a %s entity", f, k, b.dir)
						}
					}
				})
			}
		})
	}
}

// TestFocusRankGateCatchesInputDrift is the gate's own negative control: each
// real-input drift the gate exists for, applied to a recorded snapshot, moves
// the order or a rank_inputs count, so the unchanged expectation fails. It
// runs in the full module suite, not in the check, which runs only the gate.
func TestFocusRankGateCatchesInputDrift(t *testing.T) {
	jira := filepath.Join(rankSnapshotDir, "jira", "assigned_issues.json")
	bd := filepath.Join(rankSnapshotDir, "bd", "open_beads.json")

	cases := []struct {
		name    string
		file    string
		replace [2]string // old, new in the fixture text
	}{
		{"a changed due-date format", jira, [2]string{`"due_date": "2026-10-08"`, `"due_date": "08/10/2026"`}},
		{"a due value the parser cannot read in a bd snapshot", bd, [2]string{`"due_date": "2026-10-09T00:00:00Z"`, `"due_date": "tomorrow-ish"`}},
		{"the status category dropped from a started Jira issue", jira, [2]string{`"status_category": "indeterminate"`, `"status_category": ""`}},
		{"a priority spelling no map knows", jira, [2]string{`"priority": "High"`, `"priority": "Urgent"`}},
		{"issue dependencies no longer hydrated", bd, [2]string{`"issue_deps": {`, `"issue_deps_gone": {`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			text := string(raw)
			if !strings.Contains(text, tc.replace[0]) {
				t.Fatalf("%s no longer contains %q: update the drift case", tc.file, tc.replace[0])
			}
			drifted := filepath.Join(t.TempDir(), filepath.Base(tc.file))
			if err := os.WriteFile(drifted, []byte(strings.Replace(text, tc.replace[0], tc.replace[1], -1)), 0o644); err != nil {
				t.Fatal(err)
			}
			c := readRankCase(t, drifted)
			if diffs := diffRankCase(c, runRankCase(t, c)); len(diffs) == 0 {
				t.Errorf("the drift %q was not detected: the recorded snapshot's expectation still holds", tc.name)
			}
		})
	}
}
