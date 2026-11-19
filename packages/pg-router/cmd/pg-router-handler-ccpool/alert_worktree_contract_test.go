package main

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// Every janitor-owned metric name an alert reads (the ccpool_janitor_*,
// ccpool_worktree_*, ccpool_orphan_* and ccpool_build_info families) must be a
// name this handler defines, so renaming or dropping a gauge cannot leave an
// alert reading a series nobody emits. Other ccpool_ names (pool capacity,
// session states, launch outcomes) are defined by the ccpool package and are
// out of scope here.

var janitorFamilyRE = regexp.MustCompile(`\bccpool_(?:janitor_[a-z0-9_]*[a-z0-9]|worktree_[a-z0-9_]*[a-z0-9]|orphan_[a-z0-9_]*[a-z0-9]|build_info)\b`)

// alertJanitorFamilyNames returns the distinct family names that appear in the
// alert rule text.
func alertJanitorFamilyNames(text string) []string {
	seen := map[string]bool{}
	for _, m := range janitorFamilyRE.FindAllString(text, -1) {
		seen[m] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// definedJanitorNames lists the names the handler defines as constants.
func definedJanitorNames(t *testing.T) map[string]bool {
	t.Helper()
	defined := map[string]bool{}
	for _, f := range []string{"janitor_metrics.go", "janitor_census.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`=\s*"(ccpool_[a-z0-9_]+)"`).FindAllStringSubmatch(string(b), -1) {
			defined[m[1]] = true
		}
	}
	return defined
}

func undefinedJanitorNames(alerts string, defined map[string]bool) []string {
	var missing []string
	for _, n := range alertJanitorFamilyNames(alerts) {
		if !defined[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

func TestAlertsReadOnlyDefinedJanitorMetrics(t *testing.T) {
	b, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	defined := definedJanitorNames(t)
	// Proof the scan is not vacuous: the worktree growth rule's series and a
	// long-standing janitor gauge are both read and both defined.
	names := alertJanitorFamilyNames(string(b))
	for _, must := range []string{"ccpool_worktree_count", "ccpool_janitor_last_success_timestamp_seconds"} {
		found := false
		for _, n := range names {
			found = found || n == must
		}
		if !found {
			t.Fatalf("alerts.yaml no longer reads %s: the scan is reading the wrong text", must)
		}
	}
	if missing := undefinedJanitorNames(string(b), defined); len(missing) > 0 {
		t.Errorf("alerts.yaml reads janitor-family metrics the handler does not define: %v", missing)
	}
}

// The check must fail when an alert reads a name the code does not define.
func TestUndefinedJanitorMetricIsDetected(t *testing.T) {
	defined := definedJanitorNames(t)
	got := undefinedJanitorNames("expr: sum(ccpool_worktree_countt) + ccpool_orphan_bogus + ccpool_worktree_count", defined)
	want := []string{"ccpool_orphan_bogus", "ccpool_worktree_countt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("undefined names = %v, want %v", got, want)
	}
}
