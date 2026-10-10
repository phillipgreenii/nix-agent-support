package bd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

func TestSetAwaitID_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "g-9", "--await-id", "home:repo-a:def456"},
		run.Result{}, nil)
	if err := (Client{R: f}).SetAwaitID(context.Background(), "/db", "g-9", "home:repo-a:def456"); err != nil {
		t.Fatalf("SetAwaitID: %v", err)
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

// bd reports --json errors on STDOUT with an empty stderr; every wrapped call
// must still say what bd said (pg2-cjakt).
func TestCreateBead_surfacesBdJSONErrorFromStdout(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "create", "t", "--actor", "s", "--json"},
		run.Result{Stdout: `{"error":"title must be 500 characters or less (got 660)"}`, ExitCode: 1},
		errors.New("bd -C /db create t --actor s --json: exit 1: "))
	_, err := (Client{R: f}).CreateBead(context.Background(), "/db", "t", "", "", "s")
	if err == nil || !strings.Contains(err.Error(), "title must be 500 characters or less (got 660)") {
		t.Fatalf("err = %v, want bd's own message", err)
	}
}

func TestCreateBead_surfacesNonJSONStdout(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "create", "t", "--actor", "s", "--json"},
		run.Result{Stdout: "Error: something plain\n", ExitCode: 1},
		errors.New("bd create: exit 1: "))
	_, err := (Client{R: f}).CreateBead(context.Background(), "/db", "t", "", "", "s")
	if err == nil || !strings.Contains(err.Error(), "Error: something plain") {
		t.Fatalf("err = %v, want the stdout text", err)
	}
}

func TestCreateBead_emptyOutputStillSaysSomething(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "create", "t", "--actor", "s", "--json"},
		run.Result{ExitCode: 1}, errors.New("bd create: exit 1: "))
	_, err := (Client{R: f}).CreateBead(context.Background(), "/db", "t", "", "", "s")
	if err == nil || !strings.Contains(err.Error(), "no output from bd") {
		t.Fatalf("err = %v, want \"no output from bd\"", err)
	}
}

func TestWrappedErrorsNeverEndEmpty_allBdCalls(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(Client) error{
		"gate list":    func(c Client) error { _, e := c.ListGates(ctx, "/db"); return e },
		"gate create":  func(c Client) error { _, e := c.CreateGate(ctx, "/db", "b", "pn:applied", "id", ""); return e },
		"set-metadata": func(c Client) error { return c.SetMetadata(ctx, "/db", "i", "k", "v") },
		"await-id":     func(c Client) error { return c.SetAwaitID(ctx, "/db", "i", "a") },
		"gate resolve": func(c Client) error { return c.ResolveGate(ctx, "/db", "i", "") },
		"add-label":    func(c Client) error { return c.AddLabel(ctx, "/db", "i", "human") },
		"create":       func(c Client) error { _, e := c.CreateBead(ctx, "/db", "t", "", "", "s"); return e },
		"ready":        func(c Client) error { _, e := c.ReadyIDs(ctx, "/db"); return e },
		"update defer": func(c Client) error { return c.UpdateDefer(ctx, "/db", "i", "", "s") },
		"comment":      func(c Client) error { return c.Comment(ctx, "/db", "i", "x", "s") },
	}
	for name, call := range calls {
		c := Client{R: failingRunner{res: run.Result{Stdout: `{"error":"bd said no"}`, ExitCode: 1}}}
		err := call(c)
		if err == nil || !strings.Contains(err.Error(), "bd said no") {
			t.Errorf("%s: err = %v, want bd's stdout message", name, err)
		}
		c = Client{R: failingRunner{res: run.Result{ExitCode: 1}}}
		if err := call(c); err == nil || !strings.Contains(err.Error(), "no output from bd") {
			t.Errorf("%s (empty): err = %v, want \"no output from bd\"", name, err)
		}
	}
}

// failingRunner fails every call with a CLIRunner-shaped error whose trailing
// message is empty (what an stderr-only capture produces under --json).
type failingRunner struct{ res run.Result }

func (f failingRunner) Run(_ context.Context, name string, args []string, _ run.Options) (run.Result, error) {
	return f.res, fmt.Errorf("%s %s: exit %d: ", name, strings.Join(args, " "), f.res.ExitCode)
}

func TestValidateTitle(t *testing.T) {
	if err := ValidateTitle(strings.Repeat("a", 500)); err != nil {
		t.Errorf("500 chars: %v", err)
	}
	err := ValidateTitle(strings.Repeat("a", 501))
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "501") {
		t.Errorf("501 chars: err = %v, want it to name the 500 limit and the got length", err)
	}
	// Counted in runes (bd's unit was not verified): 500 multibyte runes pass.
	if err := ValidateTitle(strings.Repeat("é", 500)); err != nil {
		t.Errorf("500 runes: %v", err)
	}
	if err := ValidateTitle(strings.Repeat("é", 501)); err == nil {
		t.Error("501 runes: want error")
	}
}

func TestExport_argsAndTimeout(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "export", "-o", "/w/export.jsonl"}, run.Result{}, nil)
	if err := (Client{R: f}).Export(context.Background(), "/db", "/w/export.jsonl"); err != nil {
		t.Fatalf("Export: %v", err)
	}
	call := f.Calls()[0]
	if call.Opts.Timeout != ExportTimeout {
		t.Errorf("Timeout = %v, want %v", call.Opts.Timeout, ExportTimeout)
	}
	if !envHas(call.Opts.Env, "BD_JSON_ENVELOPE=1") {
		t.Errorf("BD_JSON_ENVELOPE=1 not set")
	}
}

func TestExport_errorWrapsDetail(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "export", "-o", "/w/e"},
		run.Result{Stderr: "boom", ExitCode: 1}, errors.New("exit 1"))
	err := (Client{R: f}).Export(context.Background(), "/db", "/w/e")
	if err == nil || !strings.Contains(err.Error(), "bd export") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestExport_timeoutErrorPreserved(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "export", "-o", "/w/e"}, run.Result{}, run.ErrTimeout)
	err := (Client{R: f}).Export(context.Background(), "/db", "/w/e")
	if !errors.Is(err, run.ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestReady_table(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		wantIDs []string
		wantErr string
	}{
		{"envelope", `{"data":[{"id":"x-1","labels":["human"]},{"id":"x-2","is_template":true}],"schema_version":1}`, []string{"x-1", "x-2"}, ""},
		{"bare array", `[{"id":"x-1"}]`, []string{"x-1"}, ""},
		{"empty envelope", `{"data":[],"schema_version":1}`, nil, ""},
		{"null data", `{"data":null}`, nil, "positive control"},
		{"garbage", `nope`, nil, "parse bd ready json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := run.NewFakeRunner()
			f.AddResponse("bd", []string{"-C", "/db", "ready", "-n", "0", "--json"}, run.Result{Stdout: tt.stdout}, nil)
			rows, raw, err := Client{R: f}.Ready(context.Background(), "/db")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ready: %v", err)
			}
			var ids []string
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
			}
			if string(raw) != tt.stdout {
				t.Errorf("raw not preserved: %q", raw)
			}
			if got := f.Calls()[0].Opts.Timeout; got != ReadyTimeout {
				t.Errorf("Timeout = %v, want %v", got, ReadyTimeout)
			}
		})
	}
}

func TestReady_bdFailureWrapped(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "-n", "0", "--json"},
		run.Result{Stderr: "dolt down", ExitCode: 1}, errors.New("exit 1"))
	_, _, err := Client{R: f}.Ready(context.Background(), "/db")
	if err == nil || !strings.Contains(err.Error(), "dolt down") {
		t.Errorf("err = %v", err)
	}
}
