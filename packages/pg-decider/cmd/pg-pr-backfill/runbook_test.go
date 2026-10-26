package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// The first-apply runbook (docs/runbooks/pg-pr-first-apply.md) is a numbered copy of
// the procedure with the commands filled in. These tests keep it honest: every
// `pg-pr-backfill` command it shows is run once, in a private configuration whose
// tracker and connector do not exist, and must be understood by the built command
// (a command or flag it does not know is the failure; what the command then does
// with its missing inputs is not the question), and the plan-age bound it states
// is the summary's default.

var runbookRel = filepath.Join("..", "..", "..", "..", "docs", "runbooks", "pg-pr-first-apply.md")

func readRunbook(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(runbookRel)
	if err != nil {
		t.Skipf("the runbook is not reachable (the nix build sandbox holds only this module): %v", err)
	}
	return string(b)
}

// runbookCommands are the `pg-pr-backfill ...` lines of the runbook's code blocks,
// comments and placeholders resolved.
func runbookCommands(t *testing.T, doc string) [][]string {
	t.Helper()
	var out [][]string
	in := false
	sc := bufio.NewScanner(strings.NewReader(doc))
	placeholder := regexp.MustCompile(`<[^>]+>`)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "```") {
			in = !in
			continue
		}
		if !in || !strings.HasPrefix(l, "pg-pr-backfill ") {
			continue
		}
		if i := strings.Index(l, " #"); i >= 0 {
			l = l[:i]
		}
		l = strings.ReplaceAll(l, `"$(date -u +%Y-%m-%dT%H:%M:%SZ)"`, "2026-10-25T12:00:00Z")
		l = strings.ReplaceAll(l, "<kind>=<n>,<kind>=<n>", "anchor=1,review-pr=1")
		l = placeholder.ReplaceAllString(l, "x")
		out = append(out, strings.Fields(l)[1:])
	}
	return out
}

func TestEveryDriverCommandOfTheRunbookExistsInTheBuiltCommand(t *testing.T) {
	doc := readRunbook(t)
	cmds := runbookCommands(t, doc)
	seen := map[string]bool{}
	for _, c := range cmds {
		seen[c[0]] = true
	}
	// Non-vacuity: the runbook shows every command of the procedure.
	for _, want := range []string{"enumerate", "run", "summary", "snapshot", "flip-marker", "writes", "rollback"} {
		if !seen[want] {
			t.Errorf("the runbook shows no `pg-pr-backfill %s` command", want)
		}
	}
	for _, args := range cmds {
		c := newCLI(t, map[string]any{
			"bd": "/nonexistent/bd", "connector": "/nonexistent/pg-connector", "change_cmd": "/nonexistent/pg-pr-change",
			"beads_dir": t.TempDir(), "backend": "beads",
		})
		_, out, errb := c.run(args...)
		for _, bad := range []string{"flag provided but not defined", "unknown command", "unexpected argument"} {
			if strings.Contains(out+errb, bad) {
				t.Errorf("pg-pr-backfill %s: %s:\n%s%s", strings.Join(args, " "), bad, out, errb)
			}
		}
	}
	// A negative case: the check does catch a flag the command lacks.
	c := newCLI(t, map[string]any{"beads_dir": t.TempDir(), "backend": "beads"})
	if _, _, errb := c.run("rollback", "--no-such-flag"); !strings.Contains(errb, "flag provided but not defined") {
		t.Fatalf("an unknown flag was not reported as such: %q", errb)
	}
}

func TestTheRunbooksPlanAgeBoundIsTheSummarysDefault(t *testing.T) {
	doc := readRunbook(t)
	m := regexp.MustCompile(`(?m)Plan age bound: (\d+) hours`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("the runbook states no `Plan age bound: N hours`")
	}
	want := backfill.DefaultMaxPlanAge.Hours()
	var got float64
	for _, r := range m[1] {
		got = got*10 + float64(r-'0')
	}
	if got != want {
		t.Fatalf("the runbook says %v hours, the summary's default is %v", got, want)
	}
	if !strings.Contains(doc, "`--max-plan-age`") || !strings.Contains(doc, "`max_plan_age`") {
		t.Error("the runbook does not name the summary parameter and the configuration key of the bound")
	}
}

func TestRollbackAndSnapshotRefuseWithoutABeforeImageOrAMarker(t *testing.T) {
	c := newCLI(t, map[string]any{"bd": "/nonexistent/bd", "beads_dir": t.TempDir(), "backend": "beads"})
	// No flip marker: nothing to bound the listing.
	if code, _, errb := c.run("rollback"); code != exitRollbackRefused || !strings.Contains(errb, "no flip marker") {
		t.Fatalf("rollback with no marker: %d %q", code, errb)
	}
	if code, _, _ := c.run("flip-marker", "--time", "2026-10-25T12:00:00Z"); code != 0 {
		t.Fatalf("flip-marker: %d", code)
	}
	// A marker but no snapshot.
	if code, _, errb := c.run("rollback"); code != exitRollbackRefused || !strings.Contains(errb, "not a before-image") {
		t.Fatalf("rollback with no snapshot: %d %q", code, errb)
	}
	// A snapshot after the marker is refused before the tracker is read.
	if code, _, errb := c.run("snapshot"); code != exitRollbackRefused || !strings.Contains(errb, "no before-image") {
		t.Fatalf("snapshot after the marker: %d %q", code, errb)
	}
}
