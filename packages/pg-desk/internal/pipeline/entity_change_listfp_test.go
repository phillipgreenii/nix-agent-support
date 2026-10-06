package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func strp(s string) *string { return &s }

func listFPNow(t *testing.T, p *Pipeline) string {
	t.Helper()
	e, found, err := p.store.GetEntity(ecRepo, "issue", "i-1")
	if err != nil || !found {
		t.Fatalf("entity: found=%v err=%v", found, err)
	}
	return e.ListFP
}

// The list fingerprint is written with the snapshot, hydrated_at and the
// change_log row in one transaction, and it is the fingerprint the caller
// observed, never one derived from the hydrated payload.
func TestRunEntityChange_ListFPIsWrittenWithTheSnapshotAndIsTheObservedValue(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)

	r := mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{ListFP: strp("list-F1")})
	if !r.Written || listFPNow(t, p) != "list-F1" {
		t.Fatalf("result = %+v list_fp = %q", r, listFPNow(t, p))
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}

	// The payload now hashes differently, yet the stored value stays the
	// observed list fingerprint of THIS call.
	res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", nil)
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ListFP: strp("list-F2")})
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if after.ContentHash == before.ContentHash {
		t.Fatal("test setup: the hydrated payload must hash differently")
	}
	if after.ListFP != "list-F2" || after.ListFP == after.ContentHash {
		t.Errorf("list_fp = %q, want the observed list-F2 (content hash %q)", after.ListFP, after.ContentHash)
	}
}

// A nil ListFP (confirmation read, reset replay, sweep) leaves list_fp as it was.
func TestRunEntityChange_NilListFPLeavesTheStoredValue(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{ListFP: strp("list-F1")})
	res = ecIssue(t, "open", "you", "2026-09-16T01:00:00Z", nil)
	mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if got := listFPNow(t, p); got != "list-F1" {
		t.Errorf("list_fp = %q, want it untouched by a nil ListFP", got)
	}
}

// Every failure path leaves list_fp unchanged and writes no change_log row
// (the entity then stays different from the next listing and is found again).
func TestRunEntityChange_FailuresLeaveListFPAndLogUntouched(t *testing.T) {
	setup := func(t *testing.T) (*Pipeline, *gather.GatherResult, *error) {
		t.Helper()
		var gerr error
		res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
		p := ecPipeline(t, &res, &gerr)
		mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{ListFP: strp("list-F1")})
		return p, &res, &gerr
	}
	cases := map[string]func(t *testing.T, p *Pipeline, res *gather.GatherResult, gerr *error) error{
		"gather error": func(t *testing.T, p *Pipeline, res *gather.GatherResult, gerr *error) error {
			*gerr = errors.New("boom")
			_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ListFP: strp("list-F2")})
			return err
		},
		"degraded read": func(t *testing.T, p *Pipeline, res *gather.GatherResult, gerr *error) error {
			*res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", nil)
			res.Degraded = "deps read failed"
			_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ListFP: strp("list-F2")})
			return err
		},
		"failure inside the transaction": func(t *testing.T, p *Pipeline, res *gather.GatherResult, gerr *error) error {
			*res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", nil)
			p.store.SetBetweenBumpAndAppendHook(func() error { return errors.New("injected crash") })
			defer p.store.SetBetweenBumpAndAppendHook(nil)
			_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ListFP: strp("list-F2")})
			return err
		},
		"lost compare-and-set": func(t *testing.T, p *Pipeline, res *gather.GatherResult, gerr *error) error {
			*res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", nil)
			opts := EntityChangeOptions{ListFP: strp("list-F2"), MaxRetries: 1, beforeWrite: func(int) {
				e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
				if _, err := p.store.WriteEntityWithLog(e, e.Version, nil, "other", "t"); err != nil {
					t.Errorf("interloper: %v", err)
				}
			}}
			_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, opts)
			if !errors.Is(err, store.ErrVersionConflict) {
				t.Errorf("err = %v, want ErrVersionConflict", err)
			}
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			p, res, gerr := setup(t)
			logBefore := p.changeRows(t, "issue", "i-1")
			err := run(t, p, res, gerr)
			degraded := name == "degraded read"
			if (err == nil) != degraded {
				t.Fatalf("err = %v", err)
			}
			if got := listFPNow(t, p); got != "list-F1" {
				t.Errorf("list_fp = %q, want list-F1 unchanged", got)
			}
			got := p.changeRows(t, "issue", "i-1")
			if name == "lost compare-and-set" {
				// The interloper's own (kind-less) write appends nothing.
				got = got[:len(logBefore)]
			}
			if !reflect.DeepEqual(got, logBefore) {
				t.Errorf("change_log moved: %+v -> %+v", logBefore, got)
			}
		})
	}
}
