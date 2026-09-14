package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router/conformance"
)

// fakeCC is a minimal ccpool.Runner test double — this module's own local
// reimplementation of packages/pg-router's (now-deleted) dtest.FakeCC shape
// (ListSeq/Closed/ClosedPurge), since that package is unreachable from here
// (Go's internal-package visibility rule).
type fakeCC struct {
	mu          sync.Mutex
	ListSeq     [][]ccpool.Session
	listIdx     int
	Closed      []string
	ClosedPurge []bool
}

func (f *fakeCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}
func (f *fakeCC) Send(context.Context, string, string, ccpool.SendMode) error { return nil }
func (f *fakeCC) Cancel(context.Context, string) error                        { return nil }

func (f *fakeCC) Close(_ context.Context, externalID string, purge bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = append(f.Closed, externalID)
	f.ClosedPurge = append(f.ClosedPurge, purge)
	return nil
}

func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ListSeq) == 0 {
		return nil, nil
	}
	i := f.listIdx
	if i >= len(f.ListSeq) {
		i = len(f.ListSeq) - 1
	}
	f.listIdx++
	return f.ListSeq[i], nil
}

var _ ccpool.Runner = (*fakeCC)(nil)

func contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

// TestTeardownAllSessions_purges ports packages/pg-router's own (now-deleted)
// Orchestrator.teardownAll test of the same name: it closes
// prefix-matching sessions with purge=true.
func TestTeardownAllSessions_purges(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-x", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	teardownAllSessions(context.Background(), cc, "pg-router-")
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Fatalf("teardown must close only the pg-router session; closed=%v", cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("teardown must purge; closedPurge=%v", cc.ClosedPurge)
	}
}

// TestTeardownAllSessions_returnsClosedCount locks the count fed into the
// "preShutdown: teardown" log line — only prefix-matching sessions count.
func TestTeardownAllSessions_returnsClosedCount(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-a", Live: true},
		{ExternalID: "pg-router-feedback-zr-b", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	n := teardownAllSessions(context.Background(), cc, "pg-router-")
	if n != 2 {
		t.Errorf("teardownAllSessions closed count = %d, want 2 (pg-router- sessions only); closed=%v", n, cc.Closed)
	}
}

// TestTeardownAllSessions_preservesNeedsInput: a pg-router session in
// needs_input is left alive (NOT closed) so the operator can still attach
// after the pass; other pg-router sessions are still reaped, and the
// returned count excludes the preserved one.
func TestTeardownAllSessions_preservesNeedsInput(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-need", Live: true, State: ccpool.StateNeedsInput},
		{ExternalID: "pg-router-worker-zr-done", Live: true, State: ccpool.StateIdle},
		{ExternalID: "cc-unrelated", Live: true, State: ccpool.StateWorking},
	}}}
	n := teardownAllSessions(context.Background(), cc, "pg-router-")
	if n != 1 {
		t.Errorf("teardownAllSessions closed count = %d, want 1 (needs_input preserved, stray excluded); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-done" {
		t.Fatalf("teardown must close the idle pg-router session only; closed=%v", cc.Closed)
	}
	if contains(cc.Closed, "pg-router-worker-zr-need") {
		t.Errorf("teardown must NOT close a needs_input session; closed=%v", cc.Closed)
	}
}

// TestServePreShutdown_success proves the wire contract end to end against
// a fake ccpool.Runner: a schema-legal request gets a
// handler.preShutdown-reply back, exit 0, and the sweep actually ran.
func TestServePreShutdown_success(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-x", Live: true},
	}}}
	var stdout bytes.Buffer
	code := servePreShutdown(cc, "pg-router-", strings.NewReader(`{"schemaVersion":"1","id":"hs-1"}`), &stdout)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitOK)
	}
	var reply map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not JSON: %v; got %s", err, stdout.String())
	}
	if reply["id"] != "hs-1" || reply["outcome"] != "ok" {
		t.Errorf("reply = %+v, want id=hs-1 outcome=ok", reply)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Errorf("servePreShutdown must run the sweep; closed=%v", cc.Closed)
	}
}

// TestServePreShutdown_rejectsMalformedRequest proves the schema check runs
// BEFORE the sweep — a malformed request must not touch ccpool at all.
func TestServePreShutdown_rejectsMalformedRequest(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-x", Live: true}}}}
	var stdout bytes.Buffer
	code := servePreShutdown(cc, "pg-router-", strings.NewReader(`{"schemaVersion":"1"}`), &stdout) // missing id
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d on a schema-invalid request", code, conformance.ExitError)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a malformed request must not run the sweep; closed=%v", cc.Closed)
	}
}
