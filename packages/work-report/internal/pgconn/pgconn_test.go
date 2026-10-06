package pgconn_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn/fake"
)

func TestExecRunsBinaryFromPATH(t *testing.T) {
	rec := fake.Install(t, fake.Route{Match: []string{"activity", "list"}, Stdout: `{"sources":[]}`})
	out, code, err := pgconn.NewExec().Run(context.Background(), "activity", "list", "--before", "2026-01-02T00:00:00Z")
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if string(out) != `{"sources":[]}` {
		t.Errorf("stdout = %q", out)
	}
	want := [][]string{{"activity", "list", "--before", "2026-01-02T00:00:00Z"}}
	if got := rec.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestNonZeroExitIsDataNotError(t *testing.T) {
	fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: "partial", Exit: 2})
	r := pgconn.NewExec()
	out, code, err := r.Run(context.Background(), "activity", "list")
	if err != nil {
		t.Fatalf("a non-zero exit with output is data, got err %v", err)
	}
	if code != 2 || string(out) != "partial" {
		t.Errorf("code=%d out=%q", code, out)
	}
	d, ok := r.(pgconn.DetailedRunner)
	if !ok {
		t.Fatal("exec runner must be a DetailedRunner")
	}
	det, err := d.RunDetailed(context.Background(), "activity", "list")
	if err != nil {
		t.Fatal(err)
	}
	if det.ExitCode != 2 || !strings.Contains(string(det.Stderr), "simulated failure") {
		t.Errorf("detailed = %+v / stderr %q", det, det.Stderr)
	}
}

func TestAbsentBinaryIsError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, _, err := pgconn.NewExec().Run(context.Background(), "activity", "list")
	if err == nil {
		t.Fatal("want an error when pg-connector is not on PATH")
	}
}

func TestFakeRoutesFirstMatchWinsAndUnmatchedFails(t *testing.T) {
	fake.Install(
		t,
		fake.Route{Match: []string{"activity", "list", "--backend"}, Stdout: "pinned"},
		fake.Route{Match: []string{"activity", "list"}, Stdout: "fanout"},
	)
	r := pgconn.NewExec()
	out, _, _ := r.Run(context.Background(), "activity", "list", "--backend", "x")
	if string(out) != "pinned" {
		t.Errorf("pinned = %q", out)
	}
	out, _, _ = r.Run(context.Background(), "activity", "list", "--since", "s")
	if string(out) != "fanout" {
		t.Errorf("fanout = %q", out)
	}
	_, code, err := r.Run(context.Background(), "other")
	if err != nil || code != 64 {
		t.Errorf("unmatched: code=%d err=%v", code, err)
	}
}

func TestFakeQuotesAwkwardStdout(t *testing.T) {
	const awkward = "it's \"quoted\" $HOME `x` \\ done"
	fake.Install(t, fake.Route{Stdout: awkward})
	out, _, err := pgconn.NewExec().Run(context.Background(), "anything")
	if err != nil || string(out) != awkward {
		t.Errorf("out=%q err=%v", out, err)
	}
}
