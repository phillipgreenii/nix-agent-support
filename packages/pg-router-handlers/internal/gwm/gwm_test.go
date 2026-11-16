package gwm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

type fakeExec struct {
	calls [][]string
	res   cmdrun.Result
}

func (f *fakeExec) run(_ context.Context, c cmdrun.Call) cmdrun.Result {
	f.calls = append(f.calls, append([]string{c.Name}, c.Args...))
	return f.res
}

func TestFormatTTL(t *testing.T) {
	cases := map[time.Duration]string{
		72 * time.Hour: "3d", 30 * time.Minute: "30m", 90 * time.Minute: "90m", 2 * time.Hour: "2h",
		45 * time.Second: "45s", 1500 * time.Millisecond: "2s", 0: "none",
	}
	for d, want := range cases {
		if got := FormatTTL(d); got != want {
			t.Errorf("FormatTTL(%v) = %q, want %q", d, got, want)
		}
	}
	if d, err := ParseTTL("3d"); err != nil || d != 72*time.Hour {
		t.Errorf("ParseTTL(3d) = %v, %v", d, err)
	}
	for _, bad := range []string{"", "d", "3x", "ad"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("ParseTTL(%q) accepted", bad)
		}
	}
}

func TestLeaseArgvAndRecord(t *testing.T) {
	f := &fakeExec{res: cmdrun.Result{Stdout: []byte(`{"id":"w-1","lease_id":"l-1","worktree_path":"/wt","branch":"pg-router/bd-1","name":"worker/bd-1","head":"abc","renewed":true}`)}}
	c := &CLI{Exec: f.run, RepoRoot: "/root"}
	meta := map[string]string{"session": "s1", "bead": "bd-1", "role": "worker"}
	got, err := c.Lease(context.Background(), LeaseRequest{Name: "worker/bd-1", Owner: "s1", TTL: 30 * time.Minute, Metadata: meta, Branch: "pg-router/bd-1", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pg-gwm", "lease", "--name", "worker/bd-1", "--or-renew", "--owner", "s1", "--ttl", "30m",
		"--metadata", "role=worker", "--metadata", "bead=bd-1", "--metadata", "session=s1",
		"-b", "pg-router/bd-1", "--repo", "/root", "--format=json",
	}
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("argv = %v\nwant   %v", f.calls[0], want)
	}
	if got != (Lease{ID: "w-1", LeaseID: "l-1", Path: "/wt", Branch: "pg-router/bd-1", Name: "worker/bd-1", Head: "abc", Renewed: true}) {
		t.Errorf("lease = %+v", got)
	}
	// existing branch, write role: positional; read role: --detach [branch].
	for _, tc := range []struct {
		req  LeaseRequest
		tail []string
	}{
		{LeaseRequest{Name: "n", TTL: time.Hour, Branch: "b"}, []string{"--ttl", "1h", "--repo", "/root", "--format=json", "b"}},
		{LeaseRequest{Name: "n", TTL: time.Hour, Branch: "b", Detach: true}, []string{"--ttl", "1h", "--detach", "--repo", "/root", "--format=json", "b"}},
		{LeaseRequest{Name: "n", TTL: time.Hour, Detach: true}, []string{"--ttl", "1h", "--detach", "--repo", "/root", "--format=json"}},
	} {
		f.calls = nil
		if _, err := c.Lease(context.Background(), tc.req); err != nil {
			t.Fatal(err)
		}
		if a := f.calls[0]; !reflect.DeepEqual(a[len(a)-len(tc.tail):], tc.tail) {
			t.Errorf("argv tail = %v, want %v", a, tc.tail)
		}
	}
}

func TestRefusalsMapToSentinels(t *testing.T) {
	cases := map[int]error{
		20: ErrMaxReached, 21: ErrNameHeld, 22: ErrBranchInUse, 23: ErrDirty, 24: ErrLeaseReleased,
		25: ErrLeaseLost, 26: ErrRefUnresolvable, 28: ErrBroken, 29: ErrLowDisk, 9: ErrBusy, 4: ErrNotFound, 2: ErrUsage,
	}
	for code, sentinel := range cases {
		f := &fakeExec{res: cmdrun.Result{Code: code, Stdout: []byte(`{"schema_version":1,"error":{"name":"n","code":1,"message":"msg","hint":"hnt","details":{"path":"/other","name":"x/y"}}}`), Stderr: []byte("noise")}}
		c := &CLI{Exec: f.run, RepoRoot: "/r"}
		err := c.Release(context.Background(), Key{Name: "x/y"})
		if !errors.Is(err, sentinel) {
			t.Errorf("exit %d: %v does not match %v", code, err, sentinel)
			continue
		}
		e, ok := AsError(err)
		if !ok || e.Message != "msg" || e.Hint != "hnt" || e.Name != "n" || !strings.Contains(e.Holder(), "path=/other") {
			t.Errorf("exit %d: envelope lost: %+v", code, e)
		}
	}
	f := &fakeExec{res: cmdrun.Result{Code: 1}}
	if err := (&CLI{Exec: f.run}).Expire(context.Background(), Key{LeaseID: "l-1"}); err == nil || errors.Is(err, ErrBusy) {
		t.Errorf("exit 1: %v", err)
	}
}

func TestByKeyAndRenewArgv(t *testing.T) {
	f := &fakeExec{}
	c := &CLI{Exec: f.run, RepoRoot: "/r"}
	_ = c.Release(context.Background(), Key{Name: "worker/bd-1"})
	_ = c.Expire(context.Background(), Key{LeaseID: "l-9"})
	_ = c.Renew(context.Background(), "l-9", 72*time.Hour)
	want := [][]string{
		{"pg-gwm", "release", "--name", "worker/bd-1", "--repo", "/r", "--format=json"},
		{"pg-gwm", "expire", "--repo", "/r", "--format=json", "l-9"},
		{"pg-gwm", "renew", "--ttl", "3d", "--repo", "/r", "--format=json", "l-9"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v", f.calls)
	}
}
