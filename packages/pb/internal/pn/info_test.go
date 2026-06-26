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
