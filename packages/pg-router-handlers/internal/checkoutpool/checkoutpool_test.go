package checkoutpool

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type call struct {
	name string
	args []string
}

func fakeExec(out, errOut string, code int, rec *[]call) Exec {
	return func(_ context.Context, name string, args []string) ([]byte, []byte, int, error) {
		*rec = append(*rec, call{name, args})
		return []byte(out), []byte(errOut), code, nil
	}
}

func TestLeaseBuildsArgvAndParses(t *testing.T) {
	var calls []call
	c := CLI{Root: "/r", Exec: fakeExec(`{"leaseId":"L1","slot":"slot-01","path":"/r/slots/slot-01/tree","branch":"b"}`, "", 0, &calls)}
	got, err := c.Lease(t.Context(), LeaseRequest{Repo: "/repo", Ref: "origin/main", Owner: "sess-1", Bead: "bead-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "L1" || got.Path != "/r/slots/slot-01/tree" {
		t.Fatalf("lease = %+v", got)
	}
	want := []string{"lease", "--repo", "/repo", "--ref", "origin/main", "--owner", "sess-1", "--wait", "0s", "--reuse", "--bead", "bead-1", "--root", "/r"}
	if calls[0].name != Binary || !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("call = %+v, want args %v", calls[0], want)
	}
}

func TestExitCodesMapToSentinels(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{{20, ErrExhausted}, {21, ErrLowDisk}, {4, ErrNoLease}} {
		var calls []call
		c := CLI{Exec: fakeExec("", "boom", tc.code, &calls)}
		_, err := c.Lease(t.Context(), LeaseRequest{Repo: "/r", Ref: "x", Owner: "o"})
		if !errors.Is(err, tc.want) {
			t.Errorf("exit %d: err = %v, want %v", tc.code, err, tc.want)
		}
	}
	var calls []call
	// The old meanings (3 exhausted, 8 no-lease) and the shared Busy 9 and
	// Error 1 must not be mistaken for a sentinel.
	for _, code := range []int{1, 3, 8, 9} {
		err := CLI{Exec: fakeExec("", "x", code, &calls)}.Release(t.Context(), "L")
		if err == nil || errors.Is(err, ErrNoLease) || errors.Is(err, ErrExhausted) || errors.Is(err, ErrLowDisk) {
			t.Fatalf("exit %d err = %v", code, err)
		}
	}
}

func TestReleaseAndRenewArgv(t *testing.T) {
	var calls []call
	c := CLI{Exec: fakeExec("{}", "", 0, &calls)}
	if err := c.Release(t.Context(), "L1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Renew(t.Context(), "L1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[0].args, []string{"release", "--lease", "L1"}) ||
		!reflect.DeepEqual(calls[1].args, []string{"renew", "--lease", "L1"}) {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestLeaseRejectsIncompleteOutput(t *testing.T) {
	var calls []call
	if _, err := (CLI{Exec: fakeExec(`{"leaseId":"L"}`, "", 0, &calls)}).Lease(t.Context(), LeaseRequest{}); err == nil {
		t.Fatal("want error for a lease with no path")
	}
}

// A redispatch leases again under the same owner: the command answers exit 11
// unless --reuse is passed, and the client passes it, so the second Lease
// returns the first lease.
func TestLeaseTwiceForOneOwnerReusesTheLease(t *testing.T) {
	held := map[string]bool{}
	const out = `{"leaseId":"L1","slot":"slot-01","path":"/r/slots/slot-01/tree","branch":"b"}`
	ex := func(_ context.Context, _ string, args []string) ([]byte, []byte, int, error) {
		owner, reuse := "", false
		for i, a := range args {
			if a == "--owner" {
				owner = args[i+1]
			}
			if a == "--reuse" {
				reuse = true
			}
		}
		if held[owner] && !reuse {
			return nil, []byte("owner already leases"), 11, nil
		}
		held[owner] = true
		return []byte(out), nil, 0, nil
	}
	c := CLI{Exec: ex}
	first, err := c.Lease(t.Context(), LeaseRequest{Repo: "/repo", Ref: "main", Owner: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Lease(t.Context(), LeaseRequest{Repo: "/repo", Ref: "main", Owner: "sess-1"})
	if err != nil {
		t.Fatalf("redispatch lease: %v", err)
	}
	if first != second {
		t.Fatalf("redispatch lease = %+v, want %+v", second, first)
	}
}
