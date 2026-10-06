package main

// activity_git_e2e_test.go: the phase's automated stand-in for the operator
// checkpoint. It builds the REAL pg-connector-activity-git binary, registers
// it only under the top-level activity.sources key, and drives the umbrella's
// "activity list" fan-out in-process against fixture git repositories whose
// author dates span one month. Nothing here fakes the backend or the wire:
// the umbrella execs the real binary over the real scriptout protocol.
//
// The fixture is generated with plain git commands under a hermetic
// environment built from an allowlist (never inheriting GIT_DIR or
// GIT_INDEX_FILE, which the commit-time test hook exports).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	gitE2EBinary      = "pg-connector-activity-git"
	gitE2EAuthorEmail = "operator@example.test"
	gitE2EOtherEmail  = "someone-else@example.test"
	// gitE2EBuildDeadline is a hang guard for a cold go build under load.
	gitE2EBuildDeadline = 8 * time.Minute
)

// gitE2EEnv returns a hermetic environment for the fixture's own git
// commands: PATH and TMPDIR from the ambient environment, a pinned HOME, no
// global or system config, and a pinned identity and dates. GIT_DIR and
// GIT_INDEX_FILE are never copied.
func gitE2EEnv(home, name, email, date string) []string {
	env := []string{
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date,
	}
	for _, k := range []string{"PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func gitE2ERun(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitE2ECommit makes one commit (a one-line file change) authored by email at
// the given RFC3339 date.
func gitE2ECommit(t *testing.T, home, repo, name, email string, when time.Time, msg string) {
	t.Helper()
	env := gitE2EEnv(home, name, email, when.Format(time.RFC3339))
	f, err := os.OpenFile(filepath.Join(repo, "log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open fixture file: %v", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", msg); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture file: %v", err)
	}
	gitE2ERun(t, env, repo, "add", "log.txt")
	gitE2ERun(t, env, repo, "commit", "-q", "-m", msg)
}

func gitE2EInit(t *testing.T, home, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repo, err)
	}
	gitE2ERun(t, gitE2EEnv(home, "init", "init@example.test", "2026-08-31T00:00:00Z"), repo, "init", "-q", "-b", "main")
}

// buildActivityGitBinary go-builds the real backend into a fresh temp dir and
// returns that directory. The build runs with the module root as its working
// directory and without any inherited GIT_* variable.
func buildActivityGitBinary(t *testing.T) string {
	t.Helper()
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), gitE2EBuildDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", filepath.Join(dir, gitE2EBinary), "./cmd/"+gitE2EBinary)
	cmd.Dir = moduleRoot
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/%s: %v\n%s", gitE2EBinary, err, out)
	}
	return dir
}

// gitE2EItem is one decoded activity item, as the umbrella emits it.
type gitE2EItem struct {
	Source string         `json:"source"`
	Item   map[string]any `json:"item"`
}

func gitE2EList(t *testing.T, since, before string) (activityOut, []gitE2EItem) {
	t.Helper()
	stdout, stderr, code := executePr(t, []string{"activity", "list", "--since", since, "--before", before, "--output", "json"})
	if code != 0 {
		t.Fatalf("activity list [%s, %s): exit = %d\nstdout=%s\nstderr=%s", since, before, code, stdout, stderr)
	}
	o := decodeActivityOut(t, stdout)
	items := make([]gitE2EItem, 0, len(o.Items))
	for _, it := range o.Items {
		items = append(items, gitE2EItem{Source: it.Source, Item: it.Item})
	}
	return o, items
}

// byID indexes items by id, failing on a duplicate.
func gitE2EByID(t *testing.T, items []gitE2EItem) map[string]map[string]any {
	t.Helper()
	m := make(map[string]map[string]any, len(items))
	for _, it := range items {
		id, _ := it.Item["id"].(string)
		if id == "" {
			t.Fatalf("item without id: %v", it.Item)
		}
		if _, dup := m[id]; dup {
			t.Fatalf("duplicate item id %q in one result", id)
		}
		m[id] = it.Item
	}
	return m
}

// withoutAsOf returns a copy of item minus as_of, the pull-time stamp that
// legitimately differs between two pulls of the same range.
func withoutAsOf(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for k, v := range item {
		if k != "as_of" {
			out[k] = v
		}
	}
	return out
}

func TestActivityGitFanOutE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found on PATH; skipping the activity-git end-to-end fan-out test")
	}

	binDir := buildActivityGitBinary(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Sandbox this process's ambient environment too: the umbrella execs the
	// backend, which execs git under a PATH+HOME-only environment.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	mainRepo := filepath.Join(root, "primary-clone")
	searchRoot := filepath.Join(root, "workspace")
	foundRepo := filepath.Join(searchRoot, "discovered-clone")
	gitE2EInit(t, home, mainRepo)
	gitE2EInit(t, home, foundRepo)
	// A non-repo directory under the search path and a missing configured
	// path: both are skipped (logged to the backend's stderr), never fatal.
	if err := os.MkdirAll(filepath.Join(searchRoot, "plain-directory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(root, "no-such-clone")

	// September 2026, noon UTC. The operator commits on most days (one or two
	// a day); another author commits on every third day and must never appear.
	day := func(d int) time.Time { return time.Date(2026, time.September, d, 12, 0, 0, 0, time.UTC) }
	expectedPerDay := map[string]int{}
	for d := 1; d <= 30; d++ {
		if d%7 == 0 {
			continue // quiet days
		}
		n := 1 + d%2
		for i := 0; i < n; i++ {
			when := day(d).Add(time.Duration(i) * time.Hour)
			gitE2ECommit(t, home, mainRepo, "Operator", gitE2EAuthorEmail, when, fmt.Sprintf("operator change %d-%d", d, i))
		}
		expectedPerDay[day(d).Format("2006-01-02")] += n
		if d%3 == 0 {
			gitE2ECommit(t, home, mainRepo, "Someone Else", gitE2EOtherEmail, day(d).Add(5*time.Hour), fmt.Sprintf("other change %d", d))
		}
	}
	// The discovered clone holds two operator commits and one foreign commit.
	gitE2ECommit(t, home, foundRepo, "Operator", gitE2EAuthorEmail, day(10), "discovered operator one")
	gitE2ECommit(t, home, foundRepo, "Operator", gitE2EAuthorEmail, day(20), "discovered operator two")
	gitE2ECommit(t, home, foundRepo, "Someone Else", gitE2EOtherEmail, day(15), "discovered other")
	expectedPerDay[day(10).Format("2006-01-02")]++
	expectedPerDay[day(20).Format("2006-01-02")]++
	wantTotal := 0
	for _, n := range expectedPerDay {
		wantTotal += n
	}

	writeConfig := func(t *testing.T, yaml string) {
		t.Helper()
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv("PG_PR_CONFIG", cfg)
		t.Setenv("XDG_STATE_HOME", dir)
	}
	registryYAML := fmt.Sprintf(`activity:
  sources:
    - %s
backends:
  %s:
    author_emails:
      - %s
    repo_paths:
      - %s
      - %s
    repo_search_paths:
      - %s
`, gitE2EBinary, gitE2EBinary, gitE2EAuthorEmail, mainRepo, missing, searchRoot)

	const (
		monthStart = "2026-09-01T00:00:00Z"
		monthEnd   = "2026-10-01T00:00:00Z"
	)

	t.Run("one-month backfill through the umbrella", func(t *testing.T) {
		writeConfig(t, registryYAML)
		o, items := gitE2EList(t, monthStart, monthEnd)

		if len(o.Sources) != 1 {
			t.Fatalf("sources = %v, want exactly the one registered backend", o.Sources)
		}
		row := o.Sources[0]
		if row["source"] != gitE2EBinary || row["status"] != "succeeded" || row["truncated"] != false || row["count"] != float64(wantTotal) {
			t.Fatalf("sources[0] = %v, want %s succeeded count=%d truncated=false", row, gitE2EBinary, wantTotal)
		}
		if len(items) != wantTotal {
			t.Fatalf("items = %d, want %d", len(items), wantTotal)
		}
		byID := gitE2EByID(t, items) // fails on any duplicate id

		perDay := map[string]int{}
		for _, it := range items {
			if it.Source != gitE2EBinary {
				t.Fatalf("item source = %q, want %q", it.Source, gitE2EBinary)
			}
			if it.Item["kind"] != "commit" {
				t.Fatalf("item kind = %v, want commit: %v", it.Item["kind"], it.Item)
			}
			var fields struct {
				AuthorEmail string `json:"author_email"`
			}
			raw, _ := json.Marshal(it.Item["fields"])
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("decode fields: %v", err)
			}
			if fields.AuthorEmail != gitE2EAuthorEmail {
				t.Fatalf("a commit by %q appeared: %v", fields.AuthorEmail, it.Item)
			}
			summary, _ := it.Item["summary"].(string)
			if strings.Contains(summary, "other") && !strings.Contains(summary, "operator") {
				t.Fatalf("another author's commit appeared: %v", it.Item)
			}
			when, _ := it.Item["occurred_at"].(string)
			perDay[when[:10]]++
		}
		if !reflect.DeepEqual(perDay, expectedPerDay) {
			t.Fatalf("per-day commit counts = %v, want %v", perDay, expectedPerDay)
		}
		if len(byID) != wantTotal {
			t.Fatalf("distinct ids = %d, want %d", len(byID), wantTotal)
		}
	})

	t.Run("overlapping pulls agree on every shared id", func(t *testing.T) {
		writeConfig(t, registryYAML)
		_, first := gitE2EList(t, monthStart, "2026-09-21T00:00:00Z")
		_, second := gitE2EList(t, "2026-09-10T00:00:00Z", monthEnd)
		a, b := gitE2EByID(t, first), gitE2EByID(t, second)

		var shared []string
		for id := range a {
			if _, ok := b[id]; ok {
				shared = append(shared, id)
			}
		}
		sort.Strings(shared)
		// The overlap window [09-10, 09-21) holds operator commits, so the
		// comparison below is never vacuous.
		if len(shared) == 0 {
			t.Fatalf("overlapping pulls shared no ids (first=%d second=%d)", len(a), len(b))
		}
		for _, id := range shared {
			if !reflect.DeepEqual(withoutAsOf(a[id]), withoutAsOf(b[id])) {
				t.Fatalf("id %q differs between pulls (as_of aside):\nfirst:  %v\nsecond: %v", id, a[id], b[id])
			}
		}
	})

	t.Run("registering under a connector type fails to load", func(t *testing.T) {
		writeConfig(t, "connector:\n  pr:\n    - "+gitE2EBinary+"\nactivity:\n  sources:\n    - "+gitE2EBinary+"\n")
		// The rejection is the registry load's own error; newRootCmd silences
		// error printing, so read it from Execute's return value.
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"activity", "list", "--since", monthStart, "--before", monthEnd})
		execErr := root.Execute()
		if execErr == nil {
			t.Fatalf("activity list succeeded with the backend under connector.pr; stdout=%s", out.String())
		}
		combined := execErr.Error() + out.String()
		for _, want := range []string{"connector.pr", gitE2EBinary} {
			if !strings.Contains(combined, want) {
				t.Fatalf("output %q does not name %q", combined, want)
			}
		}
	})
}
