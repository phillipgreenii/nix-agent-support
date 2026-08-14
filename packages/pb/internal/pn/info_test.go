package pn

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// REAL shape: pn workspace info --json emits a BARE object (no {data} envelope).
const sampleInfoJSON = `{
  "wsid": "home",
  "root": "/ws",
  "terminal": "machine",
  "repos": [
    {"name": "repo-a", "path": "/ws/repo-a", "applied_ref": "3e1f4b1", "dirty": false},
    {"name": "repo-b", "path": "/ws/repo-b", "applied_ref": "", "dirty": true}
  ]
}`

func TestInfo_parsesBareAndFields(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: sampleInfoJSON}, nil)
	info, err := Client{R: f}.Info(context.Background(), "/ws")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Wsid != "home" || info.Root != "/ws" || info.Terminal != "machine" {
		t.Fatalf("top-level = %+v", info)
	}
	if len(info.Repos) != 2 {
		t.Fatalf("repos len = %d", len(info.Repos))
	}
	a, ok := info.RepoByName("repo-a")
	if !ok || a.AppliedRef != "3e1f4b1" || a.Dirty {
		t.Errorf("repo-a = %+v ok=%v", a, ok)
	}
	b, _ := info.RepoByName("repo-b")
	if b.AppliedRef != "" || !b.Dirty {
		t.Errorf("repo-b applied_ref must be empty string + dirty: %+v", b)
	}
}

// TestInfo_parsesAppliedStateProjection pins the WIRE NAMES of pn's applied-state
// projection, which is the cross-repo seam this client exists to consume: the JSON
// keys are produced by phillipg-nix-repo-base (ADR 0025 and its "what the apply
// overrode" amendment) and a rename or typo on either side is invisible to the
// compiler. `overridden` matters most, because it is what makes gate condition 2
// conditional (bead pg2-14yqh) — a key that does not bind reads as false, which
// silently restores the unconditional behaviour the ruling replaced.
func TestInfo_parsesAppliedStateProjection(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: `{
	  "wsid": "home", "root": "/ws", "terminal": "machine",
	  "repos": [
	    {"name": "overridden", "path": "/ws/overridden", "applied_ref": "aaa", "dirty": false,
	     "applied_state_schema": 3, "terminal_input": true, "locked_rev": "lockrev", "overridden": true},
	    {"name": "lockbuilt", "path": "/ws/lockbuilt", "applied_ref": "bbb", "dirty": false,
	     "applied_state_schema": 3, "terminal_input": true, "locked_rev": "lockrev", "overridden": false}
	  ]}`}, nil)
	info, err := Client{R: f}.Info(context.Background(), "/ws")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, want := range []Repo{
		{Name: "overridden", AppliedStateSchema: 3, TerminalInput: true, LockedRev: "lockrev", Overridden: true},
		{Name: "lockbuilt", AppliedStateSchema: 3, TerminalInput: true, LockedRev: "lockrev", Overridden: false},
	} {
		got, ok := info.RepoByName(want.Name)
		if !ok {
			t.Fatalf("repo %q missing", want.Name)
		}
		if got.AppliedStateSchema != want.AppliedStateSchema || got.TerminalInput != want.TerminalInput ||
			got.LockedRev != want.LockedRev || got.Overridden != want.Overridden {
			t.Errorf("%s: schema=%d terminal_input=%v locked_rev=%q overridden=%v; want %d/%v/%q/%v",
				want.Name, got.AppliedStateSchema, got.TerminalInput, got.LockedRev, got.Overridden,
				want.AppliedStateSchema, want.TerminalInput, want.LockedRev, want.Overridden)
		}
	}
}

func TestInfo_tolerablesEnvelope(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stdout: `{"data":{"wsid":"w","root":"/r","terminal":"t","repos":[]},"schema_version":1}`}, nil)
	info, err := Client{R: f}.Info(context.Background(), "/r")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Root != "/r" || info.Wsid != "w" {
		t.Errorf("enveloped parse = %+v", info)
	}
}

func TestInfo_errorsWhenNotInWorkspace(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stderr: "not in a workspace", ExitCode: 1}, errors.New("exit 1"))
	if _, err := (Client{R: f}).Info(context.Background(), "/tmp"); err == nil {
		t.Fatal("expected error when pn exits non-zero")
	}
}

func TestInfo_errorsOnEmptyRoot(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stdout: `{"wsid":"w","root":"","repos":[]}`}, nil)
	if _, err := (Client{R: f}).Info(context.Background(), "/tmp"); err == nil {
		t.Fatal("expected error on empty root")
	}
}
