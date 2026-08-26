package bd

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

const gateListJSON = `{
  "data": [
    {"id":"x-1","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}},
    {"id":"x-2","issue_type":"gate","await_type":"timer","await_id":""}
  ],
  "schema_version": 1
}`

func TestListGates_parsesEnvelopeAndMetadata(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: gateListJSON}, nil)
	gates, err := Client{R: f}.ListGates(context.Background(), "/db")
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("len = %d", len(gates))
	}
	if gates[0].AwaitType != "pn:applied" || gates[0].AwaitID != "home:repo-a:abc123" {
		t.Errorf("gate0 = %+v", gates[0])
	}
	if gates[0].Metadata["applied_baseline"] != "base1" {
		t.Errorf("baseline = %q", gates[0].Metadata["applied_baseline"])
	}
	if gates[0].CreatedAt != "2026-06-26T00:00:00Z" {
		t.Errorf("created_at = %q", gates[0].CreatedAt)
	}
	// BD_JSON_ENVELOPE=1 must be set on the call.
	call := f.Calls()[0]
	if !envHas(call.Opts.Env, "BD_JSON_ENVELOPE=1") {
		t.Errorf("BD_JSON_ENVELOPE=1 not set; env=%v", call.Opts.Env)
	}
}

func TestCreateGate_returnsID(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd",
		[]string{
			"-C", "/db", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
			"--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json",
		},
		run.Result{Stdout: `{"data":{"id":"g-9"},"schema_version":1}`}, nil)
	id, err := Client{R: f}.CreateGate(context.Background(), "/db", "b-1", "pn:applied", "home:repo-a:abc123", "pn:applied gate")
	if err != nil {
		t.Fatalf("CreateGate: %v", err)
	}
	if id != "g-9" {
		t.Errorf("id = %q, want g-9", id)
	}
}

func TestSetMetadata_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "g-9", "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)
	if err := (Client{R: f}).SetMetadata(context.Background(), "/db", "g-9", "applied_baseline", "base1"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
}

func TestResolveGate_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "gate", "resolve", "g-9", "--reason", "applied"},
		run.Result{}, nil)
	if err := (Client{R: f}).ResolveGate(context.Background(), "/db", "g-9", "applied"); err != nil {
		t.Fatalf("ResolveGate: %v", err)
	}
}

func TestHasBead(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "show", "b-1", "--json"}, run.Result{Stdout: "{}"}, nil)
	if !(Client{R: f}).HasBead(context.Background(), "/db", "b-1") {
		t.Error("HasBead = false, want true (scripted exit 0)")
	}
	// unscripted call → FakeRunner returns an error → HasBead false
	if (Client{R: f}).HasBead(context.Background(), "/db", "ghost") {
		t.Error("HasBead(ghost) = true, want false")
	}
}

func TestAddLabel_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "g-9", "--add-label", "human"}, run.Result{}, nil)
	if err := (Client{R: f}).AddLabel(context.Background(), "/db", "g-9", "human"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
}

func TestListGates_propagatesError(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "gate", "list", "--limit", "0", "--json"},
		run.Result{ExitCode: 1}, errors.New("boom"))
	if _, err := (Client{R: f}).ListGates(context.Background(), "/db"); err == nil {
		t.Fatal("expected error propagated")
	}
}

func envHas(env []string, want string) bool {
	return slices.Contains(env, want)
}

func TestCreateBead_argvAndID(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "verify x after apply (pg2-a)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-a",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	c := Client{R: f}
	id, err := c.CreateBead(context.Background(), "/db",
		"verify x after apply (pg2-a)", "2126-01-01", "discovered-from:pg2-a", "sess-1")
	if err != nil {
		t.Fatalf("CreateBead: %v", err)
	}
	if id != "pg2-child" {
		t.Errorf("id = %q, want pg2-child", id)
	}
}

func TestCreateBead_arrayEnvelope(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "t", "--defer", "2126-01-01",
		"--deps", "discovered-from:pg2-a", "--actor", "s", "--json",
	}, run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)
	id, err := Client{R: f}.CreateBead(context.Background(), "/db", "t", "2126-01-01", "discovered-from:pg2-a", "s")
	if err != nil || id != "pg2-child" {
		t.Fatalf("id, err = %q, %v; want pg2-child, nil", id, err)
	}
}

func TestCreateBead_noIDErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "t", "--defer", "2126-01-01",
		"--deps", "discovered-from:pg2-a", "--actor", "s", "--json",
	}, run.Result{Stdout: `{"data":{}}`}, nil)
	if _, err := (Client{R: f}).CreateBead(context.Background(), "/db", "t", "2126-01-01", "discovered-from:pg2-a", "s"); err == nil {
		t.Fatal("expected error when bd create returns no id")
	}
}

func TestReadyIDs_uncappedQueryAndParse(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-x"},{"id":"pg2-y"}]}`}, nil)
	ids, err := Client{R: f}.ReadyIDs(context.Background(), "/db")
	if err != nil {
		t.Fatalf("ReadyIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "pg2-x" || ids[1] != "pg2-y" {
		t.Errorf("ids = %v", ids)
	}
}

func TestReadyIDs_emptyQueueIsNotAnError(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	ids, err := Client{R: f}.ReadyIDs(context.Background(), "/db")
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids, err = %v, %v; want empty, nil", ids, err)
	}
}

// The `data` key's PRESENCE is the positive control: output that parses but
// carries no data key (an error envelope, `{}`) must be an ERROR, never an
// empty set — an absence check against a vacuous parse proves nothing.
func TestReadyIDs_missingDataKeyErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{}`}, nil)
	if _, err := (Client{R: f}).ReadyIDs(context.Background(), "/db"); err == nil {
		t.Fatal("expected error for envelope without a data key")
	}
}

func TestReadyIDs_nullDataErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":null,"error":"boom"}`}, nil)
	if _, err := (Client{R: f}).ReadyIDs(context.Background(), "/db"); err == nil {
		t.Fatal("expected error for null data")
	}
}

func TestUpdateDefer_clearUsesEmptyValue(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "pg2-c", "--defer", "", "--actor", "s"},
		run.Result{}, nil)
	if err := (Client{R: f}).UpdateDefer(context.Background(), "/db", "pg2-c", "", "s"); err != nil {
		t.Fatalf("UpdateDefer: %v", err)
	}
}

func TestComment_argv(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "comment", "pg2-a",
		"post-deploy verification gated as pg2-c (pn:applied).", "--actor", "s",
	},
		run.Result{}, nil)
	if err := (Client{R: f}).Comment(context.Background(), "/db", "pg2-a",
		"post-deploy verification gated as pg2-c (pn:applied).", "s"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}
