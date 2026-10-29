package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// cli runs the command in-process with a private configuration.
type cli struct {
	t   *testing.T
	dir string
	cfg string
}

func newCLI(t *testing.T, cfg map[string]any) *cli {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PG_DECIDER_CONFIG", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv(backfill.ConfigEnv, "")
	cfg["set_file"] = filepath.Join(dir, "set.json")
	cfg["run_record"] = filepath.Join(dir, "runs.jsonl")
	cfg["rate_log"] = filepath.Join(dir, "events.jsonl")
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := &cli{t: t, dir: dir, cfg: filepath.Join(dir, "config.json")}
	if err := os.WriteFile(c.cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *cli) run(args ...string) (int, string, string) {
	c.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{args[0], "--config", c.cfg}, args[1:]...), &out, &errb)
	if code == 1 || code == 9 {
		c.t.Fatalf("exit %d, a code the driver never uses", code)
	}
	return code, out.String(), errb.String()
}

func TestHelpAndUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "enumerate") {
		t.Fatalf("help: %d %q", code, out.String())
	}
	if code := run(context.Background(), nil, &out, &errb); code != backfill.ExitUsage {
		t.Fatalf("no command: %d", code)
	}
	if code := run(context.Background(), []string{"sync"}, &out, &errb); code != backfill.ExitUsage {
		t.Fatalf("unknown command: %d", code)
	}
	if code := run(context.Background(), []string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("version: %d", code)
	}
}

func TestEnumerateFailsClosedWithoutAQueryNameFile(t *testing.T) {
	c := newCLI(t, map[string]any{"queries_file": filepath.Join(t.TempDir(), "absent")})
	code, _, errb := c.run("enumerate")
	if code != exitEnumerateFailed || !strings.Contains(errb, "fails closed") {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "set.json")); err == nil {
		t.Error("a set was written")
	}
}

func TestRunNeedsASetAndAnApplyPassNeedsItsCreateLimits(t *testing.T) {
	c := newCLI(t, map[string]any{})
	if code, _, errb := c.run("run", "--mode", "plan"); code != backfill.ExitUsage || !strings.Contains(errb, "run enumerate first") {
		t.Fatalf("no set: %d %s", code, errb)
	}
	set := &backfill.Set{Contract: backfill.SetContract, Entries: []backfill.Entry{{PR: "o/r#1"}}}
	if err := set.Save(filepath.Join(c.dir, "set.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "events.jsonl"), []byte(`{"time":"2026-10-24T10:00:00Z","graphql_remaining":9000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := c.run("run", "--mode", "apply"); code != backfill.ExitUsage || !strings.Contains(errb, "create limits") {
		t.Fatalf("apply without limits: %d %s", code, errb)
	}
	if code, _, errb := c.run("run", "--mode", "apply", "--max-creates", "anchor=x"); code != backfill.ExitUsage || !strings.Contains(errb, "kind=") {
		t.Fatalf("bad limits: %d %s", code, errb)
	}
	if code, _, errb := c.run("run", "--mode", "sync"); code != backfill.ExitUsage {
		t.Fatalf("bad mode: %d %s", code, errb)
	}
	// A command that cannot be started stops the run with the stopped code and a last line.
	code, out, _ := c.run("run", "--mode", "plan", "--change-cmd", filepath.Join(c.dir, "absent-command"), "--pace", "0s")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != backfill.ExitStopped || !strings.HasPrefix(lines[len(lines)-1], "FAILED ") {
		t.Fatalf("exit %d last line %q", code, lines[len(lines)-1])
	}
}

func TestSummaryFlipMarkerAndWritesOverAnEmptyRecord(t *testing.T) {
	c := newCLI(t, map[string]any{})
	set := &backfill.Set{Contract: backfill.SetContract, Entries: []backfill.Entry{{PR: "o/r#1"}}}
	if err := set.Save(filepath.Join(c.dir, "set.json")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := c.run("summary")
	if code != backfill.ExitSummaryFailed || !strings.Contains(out, "enumerated: 1") || !strings.Contains(out, "o/r#1: no plan-mode record") || !strings.Contains(out, "checks: FAIL") {
		t.Fatalf("summary: %d\n%s", code, out)
	}
	if code, _, _ := c.run("writes"); code != exitNothingToList {
		t.Fatalf("writes with nothing: %d", code)
	}
	if code, _, errb := c.run("flip-marker", "--time", "yesterday"); code != backfill.ExitUsage {
		t.Fatalf("bad time: %d %s", code, errb)
	}
	code, out, errb := c.run("flip-marker", "--time", "2026-10-24T09:00:00Z")
	if code != 0 || !strings.Contains(out, "time=2026-10-24T09:00:00Z") {
		t.Fatalf("flip-marker: %d %s %s", code, out, errb)
	}
	code, out, _ = c.run("writes")
	if code != 0 || !strings.Contains(out, "writes since: 2026-10-24T09:00:00Z") || !strings.Contains(out, "writes total=0") {
		t.Fatalf("writes: %d\n%s", code, out)
	}
	// The marker is a row of its own kind: the summary still sees no plan record.
	if code, out, _ := c.run("summary", "--mode", "apply"); code != backfill.ExitSummaryFailed || !strings.Contains(out, "no apply-mode record") {
		t.Fatalf("apply summary: %d\n%s", code, out)
	}
}

func TestParseLimits(t *testing.T) {
	f := filepath.Join(t.TempDir(), "limits.json")
	if err := os.WriteFile(f, []byte(`{"anchor":3,"fix-ci":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseLimits("review-pr=2,anchor=4", f)
	if err != nil || got["anchor"] != 4 || got["fix-ci"] != 1 || got["review-pr"] != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := parseLimits("", ""); got != nil || err != nil {
		t.Fatalf("no limits: %v %v", got, err)
	}
	for _, bad := range []string{"anchor", "anchor=", "=3", "anchor=-1", "anchor=1.5"} {
		if _, err := parseLimits(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := os.WriteFile(f, []byte(`[1]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseLimits("", f); err == nil {
		t.Error("a limits file that is no object accepted")
	}
}

func TestRunWarnsWhenTheDeciderConfigurationIsNotSet(t *testing.T) {
	c := newCLI(t, map[string]any{})
	set := &backfill.Set{Contract: backfill.SetContract, Entries: []backfill.Entry{{PR: "o/r#1"}}}
	if err := set.Save(filepath.Join(c.dir, "set.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "events.jsonl"), []byte(`{"time":"2026-10-24T10:00:00Z","graphql_remaining":9000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--mode", "plan", "--change-cmd", filepath.Join(c.dir, "absent-command"), "--pace", "0s"}
	_, _, errb := c.run(args...)
	if !strings.Contains(errb, "PG_DECIDER_CONFIG is not set") || !strings.Contains(errb, "work_items_query") {
		t.Fatalf("no warning with PG_DECIDER_CONFIG unset: %q", errb)
	}
	t.Setenv("PG_DECIDER_CONFIG", filepath.Join(c.dir, "decider.json"))
	if _, _, errb := c.run(args...); strings.Contains(errb, "is not set") {
		t.Fatalf("warned although PG_DECIDER_CONFIG is set: %q", errb)
	}
}
