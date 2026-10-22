package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/query"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// Tests for the dispatch_start record (INV-CCH-35, INV-CCA-10, INV-CMA-10): one
// per accepted dispatch, naming what ran and never an env value of a command
// payload.

// readRecords returns every record of kind in the JSONL log at path.
func readRecords(t *testing.T, path, kind string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("event log line %q: %v", line, err)
		}
		if rec["kind"] == kind {
			out = append(out, rec)
		}
	}
	return out
}

func newTestLog(t *testing.T) (*eventlog.Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := eventlog.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return w, path
}

func strSlice(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("want a JSON array, got %T (%v)", v, v)
	}
	out := make([]string, len(raw))
	for i, e := range raw {
		out[i] = e.(string)
	}
	return out
}

func dispatchCommand(t *testing.T, role roles.Role, log *eventlog.Writer) error {
	t.Helper()
	d := DispatchContext{Role: role, Item: item.Item{ID: "b-1", Type: "task"}}
	_, err := commandExecutor{}.Dispatch(context.Background(), d, Deps{Cmd: query.OSCommander{}, Log: log})
	return err
}

func TestCommandDispatch_writesDispatchStart_strictNamesNoValues(t *testing.T) {
	setHandlerEnv(t)
	log, path := newTestLog(t)
	role := roles.Role{
		Name: "cmdrole", Type: "command", StrictBaseEnv: true,
		Command: &roles.CommandConfig{Argv: []string{"/bin/sh", "-c", "exit 0", "{{.BeadID}}"}},
	}
	if err := dispatchCommand(t, role, log); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, path, DispatchStartKind)
	if len(recs) != 1 {
		t.Fatalf("want exactly one dispatch_start, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["role"] != "cmdrole" || rec["item_id"] != "b-1" || rec["item_type"] != "task" {
		t.Errorf("role/item = %v/%v/%v", rec["role"], rec["item_id"], rec["item_type"])
	}
	if got, want := strSlice(t, rec["argv"]), []string{"/bin/sh", "-c", "exit 0", "b-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want the RENDERED argv %v", got, want)
	}
	if rec["strict_base_env"] != true {
		t.Errorf("strict_base_env = %v, want true", rec["strict_base_env"])
	}
	names := strSlice(t, rec["base_env_names"])
	_, wantNames := commandPayloadEnv(role, item.Item{ID: "b-1", Type: "task"})
	if !reflect.DeepEqual(names, wantNames) || len(names) == 0 {
		t.Errorf("base_env_names = %v, want %v", names, wantNames)
	}
	for _, n := range []string{"HOME", "PATH", "PG_ROUTER_ITEM_ID", "WORKSPACE_ROOT"} {
		found := false
		for _, got := range names {
			found = found || got == n
		}
		if !found {
			t.Errorf("base_env_names missing %s: %v", n, names)
		}
	}
	// No env value, handler's own or item context, may reach the record.
	b, _ := os.ReadFile(path)
	for _, secret := range []string{"s3cret", "/custom/bin", "/h"} {
		if strings.Contains(string(b), `"`+secret+`"`) || strings.Contains(string(b), "="+secret) {
			t.Errorf("event log holds an env value %q: %s", secret, b)
		}
	}
	for _, v := range rec {
		if s, ok := v.(string); ok && (s == "s3cret" || s == "/custom/bin:/usr/bin:/bin") {
			t.Errorf("record holds an env value: %v", rec)
		}
	}
}

func TestCommandDispatch_writesDispatchStart_strictOffHasNoNames(t *testing.T) {
	log, path := newTestLog(t)
	role := shCommandRole("exit 0")
	if err := dispatchCommand(t, role, log); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, path, DispatchStartKind)
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_start, got %d", len(recs))
	}
	if recs[0]["strict_base_env"] != false {
		t.Errorf("strict_base_env = %v, want false", recs[0]["strict_base_env"])
	}
	if got := strSlice(t, recs[0]["base_env_names"]); len(got) != 0 {
		t.Errorf("base_env_names = %v, want none when the switch is off", got)
	}
}

// A failing payload (any exit code but 9) is still an accepted dispatch.
func TestCommandDispatch_dispatchStartOnFailureButNotOnExit9(t *testing.T) {
	for _, tc := range []struct {
		code      string
		wantStart int
	}{{"3", 1}, {"9", 0}} {
		t.Run("exit "+tc.code, func(t *testing.T) {
			log, path := newTestLog(t)
			_ = dispatchCommand(t, shCommandRole("exit "+tc.code), log)
			if got := len(readRecords(t, path, DispatchStartKind)); got != tc.wantStart {
				t.Errorf("dispatch_start records = %d, want %d", got, tc.wantStart)
			}
		})
	}
}

func TestCommandDispatch_nilLogIsFine(t *testing.T) {
	if err := dispatchCommand(t, shCommandRole("exit 0"), nil); err != nil {
		t.Fatal(err)
	}
}

// A fresh launch through ccpool new records route "launched" and the role's
// sessionEnv entries, values included, once Ensure returned ready.
func TestDispatch_writesDispatchStart_launched(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	role := workerRole(cfg)
	role.SessionEnv = map[string]string{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml", "FLAG": "on"}
	log, path := newTestLog(t)
	deps := newExec(cc, bd, cfg).deps
	deps.Log = log
	deps.ExternalID = "pg-router-worker-zr-w"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w", Type: "task"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	recs := readRecords(t, path, DispatchStartKind)
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_start, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["route"] != RouteLaunched || RouteLaunched != "launched" {
		t.Errorf("route = %v, want %q", rec["route"], "launched")
	}
	if rec["role"] != role.Name || rec["item_id"] != "zr-w" || rec["item_type"] != "task" || rec["session"] != "pg-router-worker-zr-w" {
		t.Errorf("record = %v", rec)
	}
	want := map[string]any{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml", "FLAG": "on"}
	if !reflect.DeepEqual(rec["session_env"], want) {
		t.Errorf("session_env = %v, want %v (values included)", rec["session_env"], want)
	}
}

// Adopting a live session of the same event's turn records route "adopted".
func TestDispatch_writesDispatchStart_adopted(t *testing.T) {
	cfg := leaseCfg()
	clk := &dtest.ManualClock{T: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	sbd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{turnRow()}}}
	deps := leaseDeps(t, cc, sbd, cfg, clk)
	deps.ExternalID = "att-new"
	deps.LockDir = t.TempDir()
	log, path := newTestLog(t)
	deps.Log = log
	role := noBudget(workerRole(cfg))
	role.SessionEnv = map[string]string{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml"}
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w", Type: "task"}, EventID: absorbEvent}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("absorb dispatch: %v", err)
	}
	if len(cc.Ensured) != 0 {
		t.Fatalf("an absorbing dispatch must not launch; Ensured=%v", cc.Ensured)
	}
	recs := readRecords(t, path, DispatchStartKind)
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_start, got %d: %v", len(recs), recs)
	}
	if recs[0]["route"] != RouteAdopted || RouteAdopted != "adopted" || recs[0]["session"] != "att-old" {
		t.Errorf("record = %v, want route adopted of session att-old", recs[0])
	}
	if !reflect.DeepEqual(recs[0]["session_env"], map[string]any{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml"}) {
		t.Errorf("session_env = %v", recs[0]["session_env"])
	}
}

// A launch that fails before ready is a decline, not an accepted dispatch: no
// dispatch_start.
func TestDispatch_launchFailed_writesNoDispatchStart(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: context.DeadlineExceeded}
	h := newAbandonedHarness(t, cc, workerRole)
	log, path := newTestLog(t)
	h.run.deps.Log = log
	_ = h.dispatch(context.Background())
	if b, _ := os.ReadFile(path); strings.Contains(string(b), DispatchStartKind) {
		t.Errorf("a failed launch must write no dispatch_start: %s", b)
	}
}
