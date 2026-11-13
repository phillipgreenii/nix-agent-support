package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
	"github.com/phillipgreenii/x/exitcode"
)

// brokerEnv builds a hermetic run whose provider "fake" is a shell script that
// records its argv in argv.txt and prints one row. queries_max is 2.
func brokerEnv(t *testing.T) (runID string, evDir string, l *ledger.Ledger) {
	t.Helper()
	root := t.TempDir()
	cfgHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv(RunEnv, "")
	evDir = filepath.Join(root, "evidence")
	if err := os.MkdirAll(evDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fake.sh")
	body := `#!/bin/sh
printf '%s\n' "$@" > "` + evDir + `/argv.txt"
case "$*" in *boom*) echo "failure at /Users/someone/x" >&2; exit 3;; esac
echo '[{"kind":"k","subject_key":"s","metrics":{"calls":40,"errors":10},"note":"see /Users/someone/x/file"}]'
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `schema_version = 1
[profile.health-check]
queries_max = 2
[[scope]]
name = "demo-scope"
prefixes = ["/work/demo"]
[[provider]]
name = "fake"
command = ["sh", "` + script + `"]
timeout_seconds = 10
[provider.queries]
agent-workflow = ["by-tool", "boom"]
`
	if err := os.MkdirAll(filepath.Join(cfgHome, "pg-review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "pg-review", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var id int64
	err = l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(context.Background(), ledger.Run{
			Lens: "agent-workflow", Scope: "demo-scope", Profile: "health-check", PID: 1, StartedAt: time.Now(),
			WindowFrom: "2026-01-01T05:00:00+05:00", WindowTo: "2026-01-08T00:00:00Z", EvidenceDir: evDir,
		})
		id = r.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(id, 10), evDir, l
}

func errorOf(t *testing.T, out string) string {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal([]byte(out), &e); err != nil || e["error"] == "" {
		t.Fatalf("not a JSON error: %q (%v)", out, err)
	}
	return e["error"]
}

func TestAgentQRunsListedQueryAndLogs(t *testing.T) {
	rid, ev, l := brokerEnv(t)
	t.Setenv(RunEnv, rid)
	code, out, stderr := run(t, "agent", "q", "fake", "by-tool", "--limit", "5")
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%q %v", out, err)
	}
	if rows[0]["query_id"] != "Q-001" {
		t.Errorf("query_id %v", rows[0]["query_id"])
	}
	if strings.Contains(out, "/Users/") {
		t.Errorf("rows are not redacted: %s", out)
	}
	code, out, _ = run(t, "agent", "q", "fake", "by-tool")
	if code != exitcode.OK || !strings.Contains(out, `"Q-002"`) {
		t.Fatalf("second call: %d %s", code, out)
	}

	// The scope and window come from the run, the window as UTC with a Z.
	argv, _ := os.ReadFile(filepath.Join(ev, "argv.txt"))
	got := string(argv)
	for _, want := range []string{"--query\nby-tool\n", "--cwd-prefix\n/work/demo\n", "--since\n2026-01-01T00:00:00Z\n", "--until\n2026-01-08T00:00:00Z\n", "--scope\ndemo-scope\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("provider argv lacks %q:\n%s", want, got)
		}
	}
	// The first call's extra flags pass through unchanged.
	if run1, _ := os.ReadFile(filepath.Join(ev, "query-results", "Q-001.json")); !strings.Contains(string(run1), `"query_id":"Q-001"`) {
		t.Errorf("persisted result: %s", run1)
	}

	// The log validates against the pinned schema.
	f, err := os.Open(filepath.Join(ev, "query-log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		if err := schema.Validate("query-log-entry", sc.Bytes()); err != nil {
			t.Errorf("log line %d: %v", n, err)
		}
	}
	if n != 2 {
		t.Errorf("%d log lines, want 2", n)
	}
	var first struct {
		Args  []string `json:"args"`
		Rows  int      `json:"rows"`
		Scope string   `json:"scope"`
	}
	b, _ := os.ReadFile(filepath.Join(ev, "query-log.jsonl"))
	_ = json.Unmarshal([]byte(strings.SplitN(string(b), "\n", 2)[0]), &first)
	if strings.Join(first.Args, " ") != "by-tool --limit 5" || first.Rows != 1 || first.Scope != "demo-scope" {
		t.Errorf("first entry %+v", first)
	}
	if r, _ := l.GetRun(context.Background(), mustInt(rid)); r.QueryCount != 2 {
		t.Errorf("query_count %d", r.QueryCount)
	}
}

func mustInt(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func TestAgentQBudgetAndRunFlag(t *testing.T) {
	rid, _, l := brokerEnv(t)
	for i := 0; i < 2; i++ {
		if code, out, _ := run(t, "agent", "q", "fake", "by-tool", "--run", rid); code != exitcode.OK {
			t.Fatalf("call %d: %d %s", i, code, out)
		}
	}
	code, out, _ := run(t, "agent", "q", "fake", "by-tool", "--run="+rid)
	if code != appcode.Validation || !strings.Contains(errorOf(t, out), "budget") {
		t.Fatalf("over budget: %d %s", code, out)
	}
	if r, _ := l.GetRun(context.Background(), mustInt(rid)); r.QueryCount != 2 {
		t.Errorf("a refused call counted: %d", r.QueryCount)
	}
}

func TestAgentQRefusals(t *testing.T) {
	rid, ev, l := brokerEnv(t)
	t.Setenv(RunEnv, rid)
	cases := map[string][]string{
		"unlisted query":    {"fake", "other-query"},
		"unlisted provider": {"nope", "by-tool"},
		"scope":             {"fake", "by-tool", "--scope", "x"},
		"scope eq":          {"fake", "by-tool", "--scope=x"},
		"from":              {"fake", "by-tool", "--from", "2026-01-01T00:00:00Z"},
		"to":                {"fake", "by-tool", "--to", "2026-01-01T00:00:00Z"},
		"since":             {"fake", "by-tool", "--since", "2026-01-01T00:00:00Z"},
		"cwd prefix":        {"fake", "by-tool", "--cwd-prefix", "/"},
		"metachar":          {"fake", "by-tool", "--limit", "5; rm -rf x"},
		"subst":             {"fake", "by-tool", "--limit", "$(id)"},
		"flag as query":     {"fake", "--limit", "5"},
	}
	for name, a := range cases {
		code, out, _ := run(t, append([]string{"agent", "q"}, a...)...)
		if code != appcode.Validation {
			t.Errorf("%s: exit %d, want 4: %s", name, code, out)
			continue
		}
		errorOf(t, out)
	}
	if _, err := os.Stat(filepath.Join(ev, "argv.txt")); err == nil {
		t.Error("a refused call ran the provider")
	}
	if r, _ := l.GetRun(context.Background(), mustInt(rid)); r.QueryCount != 0 {
		t.Errorf("refused calls counted: %d", r.QueryCount)
	}
}

func TestAgentQProviderFailureCountsAndExits7(t *testing.T) {
	rid, ev, l := brokerEnv(t)
	t.Setenv(RunEnv, rid)
	code, out, _ := run(t, "agent", "q", "fake", "boom")
	if code != appcode.Provider {
		t.Fatalf("exit %d: %s", code, out)
	}
	if msg := errorOf(t, out); strings.Contains(msg, "/Users/") {
		t.Errorf("error not redacted: %s", msg)
	}
	if r, _ := l.GetRun(context.Background(), mustInt(rid)); r.QueryCount != 1 {
		t.Errorf("query_count %d, want 1", r.QueryCount)
	}
	if b, _ := os.ReadFile(filepath.Join(ev, "query-log.jsonl")); !strings.Contains(string(b), `"Q-001"`) {
		t.Errorf("failure not logged: %s", b)
	}
}

func TestAgentQNeedsRunAndLiveRun(t *testing.T) {
	rid, _, l := brokerEnv(t)
	if code, _, _ := run(t, "agent", "q", "fake", "by-tool"); code != exitcode.Usage {
		t.Errorf("no run: exit %d, want 2", code)
	}
	if code, out, _ := run(t, "agent", "q", "fake", "by-tool", "--run", "9999"); code != appcode.Validation {
		t.Errorf("unknown run: %d %s", code, out)
	}
	err := l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		r, _ := tx.GetRun(context.Background(), mustInt(rid))
		r.State = ledger.RunFiled
		return tx.UpdateRun(context.Background(), r)
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, "agent", "q", "fake", "by-tool", "--run", rid); code != appcode.Validation {
		t.Errorf("filed run: %d %s", code, out)
	}
}

func TestAgentQAddendResolvesAgainstPersistedResult(t *testing.T) {
	rid, ev, _ := brokerEnv(t)
	t.Setenv(RunEnv, rid)
	if code, out, _ := run(t, "agent", "q", "fake", "by-tool"); code != exitcode.OK {
		t.Fatalf("%d %s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(ev, "query-results", "Q-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil || rows[0]["calls"] != float64(40) || rows[0]["errors"] != float64(10) {
		t.Errorf("Q-001#0.calls does not resolve: %s", b)
	}
}
