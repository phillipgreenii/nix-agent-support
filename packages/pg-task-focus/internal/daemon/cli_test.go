package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/cli"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// cliResult is what one invocation of the command line did.
type cliResult struct {
	code        int
	out, errOut string
}

// cli runs the real command line against the daemon, in the host zone
// America/New_York and at the fake clock's time.
func (e *env) cli(args ...string) cliResult {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(), args, cli.App{
		Stdout: &out, Stderr: &errOut, Version: "test",
		Getenv: func(k string) string {
			switch k {
			case "PG_TASK_FOCUS_ADDR":
				return fmt.Sprintf("127.0.0.1:%d", e.port)
			case "TZ":
				return "America/New_York"
			case "PG_TASK_FOCUS_CONFIG":
				return e.cfg
			}
			return ""
		},
		Readlink: func(string) (string, error) { return "", os.ErrNotExist },
		Now:      e.clock.Now,
	})
	return cliResult{code: code, out: out.String(), errOut: errOut.String()}
}

var cliSchema = func() *schemacheck.Schema {
	s, err := schemacheck.Compile("cli.schema.json", schemas.CLI())
	if err != nil {
		panic(err)
	}
	return s
}()

// valid checks that a --json output validates against the checked-in schema.
func (r cliResult) valid(t *testing.T) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(r.out), "\n") {
		if err := cliSchema.Validate([]byte(line)); err != nil {
			t.Errorf("--json output does not validate against schemas/cli.schema.json: %v\n%s", err, line)
		}
	}
}

func (r cliResult) want(t *testing.T, code int, in ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.out, r.errOut)
	}
	all := r.out + r.errOut
	for _, s := range in {
		if !strings.Contains(all, s) {
			t.Errorf("output lacks %q:\nstdout: %s\nstderr: %s", s, r.out, r.errOut)
		}
	}
}

// A scripted day driven through the command line over a real daemon, a fake
// clock, a fake player and a temporary store.
func TestCLIScriptedDay(t *testing.T) {
	e := newEnv(t, options{})

	e.cli("status").want(t, cli.ExitOK, "Not set up yet")
	r := e.cli("period", "change", "--day", "2026-10-07", "--week", "2026-10-05..2026-10-11", "--sprint", "2026-10-05..2026-10-18", "--dry-run")
	r.want(t, cli.ExitOK, "Preview (nothing was changed)", "Tasks the new periods would materialize")
	e.cli("period", "change", "--day", "2026-10-07", "--week", "2026-10-05..2026-10-11", "--sprint", "2026-10-05..2026-10-18").
		want(t, cli.ExitOK, "Done:", "batch")
	st := e.cli("status")
	st.want(t, cli.ExitOK, "Profile: normal", "Day: 2026-10-07 (America/New_York)", "plan-day", "Next:", "overdue")
	if strings.HasPrefix(st.out, "READ-ONLY") {
		t.Error("a healthy store has no READ-ONLY line")
	}
	js := e.cli("status", "--json")
	js.want(t, cli.ExitOK)
	js.valid(t)

	e.clock.Set(local(9, 0))
	e.cli("task", "done", "plan-d", "--at", "8:55").want(t, cli.ExitOK, "Done: 1 event")
	e.cli("task", "done", "plan-day").want(t, cli.ExitRefused, "task_already_resolved")
	e.cli("task", "done", "zzz").want(t, cli.ExitUsage, "no task")
	e.cli("task", "skip", "post-plan").want(t, cli.ExitUsage, "--reason")
	e.cli("task", "skip", "post-plan", "--reason", "  ").want(t, cli.ExitUsage, "MUST NOT be blank")
	e.cli("task", "skip", "post-plan", "--reason", "not needed").want(t, cli.ExitOK)

	e.cli("cycle", "start", "nonesuch").want(t, cli.ExitRefused, "unknown_cycle_type", "the types are:")
	e.cli("cycle", "start", "deep-work").want(t, cli.ExitOK)
	e.clock.Set(local(9, 20))
	e.cli("cycle", "break", "deep", "--from", "9:05", "--to", "9:08").want(t, cli.ExitOK, "batch")
	e.cli("cycle", "start", "notifications", "--minutes", "5").want(t, cli.ExitOK)
	s := e.cli("status")
	s.want(t, cli.ExitOK, "Focus: Notification cycle", "Paused: Deep work cycle", "[switch to resume]")

	// A verb that does not say which of two paused cycles it means is refused, and the candidates are printed.
	e.clock.Set(local(9, 25))
	e.cli("cycle", "pause").want(t, cli.ExitOK)
	amb := e.cli("cycle", "resume")
	amb.want(t, cli.ExitRefused, "cycle_ambiguous", "Candidates:", "Deep work cycle", "Notification cycle")
	e.cli("cycle", "resume", "nonexistent").want(t, cli.ExitUsage, "no cycle that is not stopped")
	e.cli("cycle", "resume", "no").want(t, cli.ExitOK) // a unique title prefix
	e.cli("cycle", "resume", "no").want(t, cli.ExitOK, "No change:")
	e.clock.Set(local(9, 30))
	e.cli("cycle", "switch", "deep").want(t, cli.ExitOK, "batch")
	e.cli("cycle", "boost", "--minutes", "5").want(t, cli.ExitOK)
	e.cli("cycle", "boost").want(t, cli.ExitUsage, "--minutes")
	e.cli("cycle", "note", "deep", "--note", "wrote the thing", "--kv", "ticket=ABC-1", "--kv", "ticket=ABC-2", "--kv", "pr=7").want(t, cli.ExitOK)
	e.cli("cycle", "note", "deep", "--kv", "cycle_type=x").want(t, cli.ExitRefused, "reserved_key")

	// Mutations print the daemon's JSON with --json, validated against the schema.
	e.clock.Set(local(9, 35))
	j := e.cli("cycle", "pause", "--json")
	j.want(t, cli.ExitOK)
	j.valid(t)
	noop := e.cli("cycle", "pause", "deep", "--json")
	noop.want(t, cli.ExitOK, `"changed":false`)
	noop.valid(t)
	refused := e.cli("cycle", "boost", "deep", "--minutes", "5", "--at", "yesterday 9:00", "--json")
	refused.want(t, cli.ExitRefused, `"reason"`)
	refused.valid(t) // a refusal prints its problem document on stdout

	// The editor and undo.
	ev := e.cli("events", "list", "--type", "cycle.started")
	ev.want(t, cli.ExitOK, "cycle.started")
	evj := e.cli("events", "list", "--json")
	evj.want(t, cli.ExitOK)
	evj.valid(t)
	e.cli("undo").want(t, cli.ExitOK, "Done:")
	e.cli("undo", "--json").want(t, cli.ExitOK)
	e.cli("events", "retract", "01JABCDEFGHJKMNPQRSTVWXYZ0").want(t, cli.ExitRefused, "unknown_event")

	// A rollover is refused while a cycle is active, naming it; after stopping them it goes through.
	e.clock.Set(time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC))
	roll := e.cli("period", "roll")
	roll.want(t, cli.ExitRefused, "Blocking cycles", "cannot roll while a cycle is running or paused")
	e.cli("cycle", "stop", "deep").want(t, cli.ExitOK)
	e.cli("cycle", "stop", "no").want(t, cli.ExitOK)
	e.cli("period", "roll", "--dry-run").want(t, cli.ExitOK, "Preview", "Open tasks of the periods being left")
	e.cli("period", "roll", "--profile", "on-call").want(t, cli.ExitOK, "Done:")
	e.cli("status").want(t, cli.ExitOK, "Day: 2026-10-08", "Profile: on-call")
	e.cli("period", "roll").want(t, cli.ExitOK, "Nothing to roll")
	e.cli("profile", "change", "normal", "--dry-run").want(t, cli.ExitOK, "Preview")
}

func TestCLIStatusWatchStreamsOneStateObjectPerLine(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuf{}
	done := make(chan int, 1)
	go func() {
		done <- cli.Execute(ctx, []string{"status", "--watch", "--interval", "1h"}, cli.App{
			Stdout: out, Stderr: &lockedBuf{},
			Getenv: func(k string) string {
				if k == "PG_TASK_FOCUS_ADDR" {
					return fmt.Sprintf("127.0.0.1:%d", e.port)
				}
				return ""
			},
		})
	}()
	lines := func() []string {
		var ls []string
		for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if l != "" {
				ls = append(ls, l)
			}
		}
		return ls
	}
	eventually(t, "the first state line", func() bool { return len(lines()) >= 1 })
	eventually(t, "the stream to open", func() bool {
		v, _ := sampleValue(e.scrape()["pg_task_focus_sse_clients"], nil)
		return v == 1
	})
	e.clock.Set(local(9, 0))
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{})
	eventually(t, "a second state line after the mutation", func() bool { return len(lines()) >= 2 })
	for _, l := range lines() {
		if err := cliSchema.Validate([]byte(l)); err != nil {
			t.Errorf("a watch line does not validate: %v\n%s", err, l)
		}
		var v struct{ Tasks []any }
		if json.Unmarshal([]byte(l), &v) != nil || len(v.Tasks) == 0 {
			t.Errorf("a watch line is not a full state object: %s", l)
		}
	}
	cancel()
	if code := <-done; code != cli.ExitOK {
		t.Errorf("a cancelled watch exits %d, want 0", code)
	}
}

// Read-only mode in the CLI: status prints the sentence as its first line, and
// a mutation prints it and exits 5.
func TestCLIReadOnlyMode(t *testing.T) {
	fs := storefault.New(nil)
	e := newEnv(t, options{fs: fs})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Name: logFile})
	r := e.cli("task", "done", "plan-day")
	r.want(t, cli.ExitStore, "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover")
	again := e.cli("task", "done", "post-plan")
	again.want(t, cli.ExitStore)
	if !strings.HasPrefix(again.errOut, "READ-ONLY: the append fsync failed") {
		t.Errorf("stderr = %q", again.errOut)
	}
	st := e.cli("status")
	st.want(t, cli.ExitOK)
	if first := strings.SplitN(st.out, "\n", 2)[0]; first != "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover" {
		t.Errorf("the first line of status is %q", first)
	}
	// With --json the problem document is printed and the exit code is the same.
	j := e.cli("task", "done", "post-plan", "--json")
	j.want(t, cli.ExitStore, `"store_unavailable"`)
	j.valid(t)
}

// An unknown outcome (the append was rolled back) tells the operator to retry
// with the same id, and the retry works.
func TestCLIUnknownOutcomeIsRetryableWithTheSameID(t *testing.T) {
	fs := storefault.New(nil)
	e := newEnv(t, options{fs: fs})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logFile})
	r := e.cli("task", "done", "plan-day")
	r.want(t, cli.ExitStore, "the outcome is unknown", "--id ")
	idx := strings.Index(r.errOut, "--id ")
	id := strings.Fields(r.errOut[idx+len("--id "):])[0]
	e.cli("task", "done", "plan-day", "--id", id).want(t, cli.ExitOK, "Done: 1 event")
	e.cli("task", "done", "plan-day", "--id", id).want(t, cli.ExitOK, "earlier request with the same id")
}

func TestCLIExitCodes(t *testing.T) {
	e := newEnv(t, options{})
	e.d.Stop()
	e.cli("status").want(t, cli.ExitUnreachable, "not reachable")
	e.cli("cycle", "pause").want(t, cli.ExitUnreachable)
	e.cli("nonsense").want(t, cli.ExitUsage)
	e.cli("status", "--bogus").want(t, cli.ExitUsage)
	e.cli("task").want(t, cli.ExitOK)                         // a group prints its help
	e.cli("completion", "zsh").want(t, cli.ExitOK, "compdef") // shell completion is provided
	e.cli("--version").want(t, cli.ExitOK, "pg-task-focus version test")
	e.cli("cycle", "boost", "x", "y", "--minutes", "1").want(t, cli.ExitUsage)
}

// check and config check run with no daemon.
func TestCLICheckAndConfigCheck(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.d.Stop()
	ok := e.cli("check", e.dir, "--json")
	ok.want(t, cli.ExitOK, `"ok":true`)
	ok.valid(t)
	e.cli("check", e.dir).want(t, cli.ExitOK, "OK: a daemon would start on this log.")

	// A corrupt log is exit 8 with its line.
	path := filepath.Join(e.dir, "events.jsonl")
	b, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(b), "\n")
	lines[1] = "NOT JSON\n"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := e.cli("check", path)
	bad.want(t, cli.ExitCheckFailed, "PROBLEM at line 2")
	badj := e.cli("check", path, "--json")
	badj.want(t, cli.ExitCheckFailed, `"line":2`)
	badj.valid(t)

	good := e.cli("config", "check")
	good.want(t, cli.ExitOK, "OK: the configuration is valid")
	gj := e.cli("config", "check", "--json")
	gj.want(t, cli.ExitOK)
	gj.valid(t)
	if err := os.WriteFile(e.cfg, []byte(`{"listen_port": 70000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.cli("config", "check").want(t, cli.ExitCheckFailed, "/listen_port")
	bj := e.cli("config", "check", "--json")
	bj.want(t, cli.ExitCheckFailed, `"valid":false`)
	bj.valid(t)
}
