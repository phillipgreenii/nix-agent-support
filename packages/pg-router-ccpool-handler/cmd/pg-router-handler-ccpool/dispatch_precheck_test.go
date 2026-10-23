package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// roleUnderTest is a role as a request carries it: its name and [role.config].
type roleUnderTest struct {
	name string
	cfg  map[string]any
}

func configFrom(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const reviewEvent = `{"id":"e-1","type":"dispatch","payload":{"id":"zr-r","type":"review-pr","metadata":{"repo":"o/r","pr_number":120058,"head_sha":"abc"}}}`

// precheckEnv builds a hermetic environment for the review precheck through the
// REAL dispatch wiring: PATH holds only fake `bd` and `pg-connector` scripts (no
// `ccpool`, so a dispatch that gets past the precheck stops at its first ccpool
// call, the prior-session read of INV-CCH-30, and declines with reason
// pool-unreadable: an unreadable pool counts as one in flight. That is how these
// tests tell "launch path reached" from "skipped"). It returns the role and the
// log of every pg-connector call, so a test can prove the checks were read-only.
func precheckEnv(t *testing.T, beadStatus, prJSON, pendingJSON string) (role roleUnderTest, connLog string) {
	t.Helper()
	dir := t.TempDir()
	connLog = filepath.Join(dir, "connector.log")
	bin := stubBin(t, map[string]string{
		"bd": `printf '%s' '{"data":[{"id":"zr-r","status":"` + beadStatus + `","metadata":{"repo":"o/r","pr_number":120058,"head_sha":"abc"}}]}'` + "\n",
		"pg-connector": `echo "$*" >> '` + connLog + `'
case "$*" in
  "pr show "*) printf '%s' '` + prJSON + `' ;;
  "pr review pending "*) printf '%s' '` + pendingJSON + `' ;;
  *) echo "unexpected pg-connector call: $*" >&2; exit 2 ;;
esac
`,
	})
	// Only the fake bin dir: `sh` is reached through the scripts' shebang, and the
	// scripts use shell builtins only.
	t.Setenv("PATH", bin)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	return roleUnderTest{"review", configFrom(t, `{"ccpool":{"actor":"test-actor","completion":"close-or-handback","onFailure":"add-human","onDispatchFail":"unclaim","promptBody":"hello"}}`)}, connLog
}

func runDispatchReply(t *testing.T, r roleUnderTest, event string) (code int, reply map[string]any) {
	t.Helper()
	code, got := dispatchWith(t, r.name, r.cfg, "", event)
	return code, decodeReply(t, got)
}

const (
	prOpen      = `{"result":{"state":"open","merged":false}}`
	prMerged    = `{"result":{"state":"closed","merged":true}}`
	noPending   = `{"result":{"pending":false}}`
	freshReview = `{"result":{"pending":true,"review":{"stale":false}}}`
	staleReview = `{"result":{"pending":true,"review":{"stale":true}}}`
)

// The three precheck skips surface as the pre-accept busy decline carrying each
// skip's own reason (the metric label pg-router core counts), and every other
// review dispatch reaches the unchanged launch path (here: the prior-session
// read, which fails closed because the fake PATH has no ccpool).
func TestDispatch_reviewPrecheck(t *testing.T) {
	tests := []struct {
		name       string
		bead, pr   string
		pending    string
		wantReason string
		wantCalls  int // pg-connector invocations
	}{
		{"bead closed", "closed", prOpen, noPending, "skipped-bead-closed", 0},
		{"PR merged", "open", prMerged, noPending, "skipped-pr-merged", 1},
		{"pending review covers the head", "open", prOpen, freshReview, "skipped-pending-review", 2},
		{"launch path: nothing to skip", "open", prOpen, noPending, wire.BusyReasonPoolUnreadable, 2},
		{"launch path: stale pending review", "open", prOpen, staleReview, wire.BusyReasonPoolUnreadable, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			role, connLog := precheckEnv(t, tc.bead, tc.pr, tc.pending)
			code, reply := runDispatchReply(t, role, reviewEvent)
			if code != conformance.ExitBusy {
				t.Fatalf("exit = %d, want ExitBusy (%d)", code, conformance.ExitBusy)
			}
			if reply["reason"] != tc.wantReason {
				t.Fatalf("reason = %v, want %q", reply["reason"], tc.wantReason)
			}
			calls := 0
			if b, err := os.ReadFile(connLog); err == nil {
				calls = strings.Count(string(b), "\n")
			}
			if calls != tc.wantCalls {
				t.Fatalf("pg-connector calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// A fake pg-connector that, like the real one, writes an error envelope to stdout
// and exits 1. An unavailable answer declines retryably (kind busy, reason
// connector-unavailable, exit busy); every other error class and a bare failure
// keep failing open to the unchanged launch path.
func TestDispatch_reviewPrecheckConnectorErrors(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantReason string
		wantKind   string
	}{
		{"unavailable declines retryably", `printf '%s' '{"error":{"code":"unavailable","message":"daemon down"}}'; exit 1`, "connector-unavailable", "busy"},
		{"not_found fails open", `printf '%s' '{"error":{"code":"not_found","message":"gone"}}'; exit 4`, wire.BusyReasonPoolUnreadable, "fault"},
		{"unauthenticated fails open", `printf '%s' '{"error":{"code":"unauthenticated","message":"no token"}}'; exit 1`, wire.BusyReasonPoolUnreadable, "fault"},
		{"bare failure fails open", `echo boom >&2; exit 1`, wire.BusyReasonPoolUnreadable, "fault"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			role, _ := precheckEnv(t, "open", prOpen, noPending)
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "pg-connector"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			code, reply := runDispatchReply(t, role, reviewEvent)
			if code != conformance.ExitBusy {
				t.Fatalf("exit = %d, want ExitBusy (%d)", code, conformance.ExitBusy)
			}
			if reply["reason"] != tc.wantReason || reply["kind"] != tc.wantKind {
				t.Fatalf("reply = %v, want reason %q kind %q", reply, tc.wantReason, tc.wantKind)
			}
		})
	}
}

// With no pg-connector on PATH the precheck cannot read the PR, logs it, and
// launches as before.
func TestDispatch_reviewPrecheckFailsOpenWithoutConnector(t *testing.T) {
	role, _ := precheckEnv(t, "open", prOpen, noPending)
	if err := os.Remove(filepath.Join(os.Getenv("PATH"), "pg-connector")); err != nil {
		t.Fatal(err)
	}
	code, reply := runDispatchReply(t, role, reviewEvent)
	if code != conformance.ExitBusy || reply["reason"] != wire.BusyReasonPoolUnreadable {
		t.Fatalf("got exit %d reply %v, want the unchanged launch path (pool-unreadable: no ccpool on PATH)", code, reply)
	}
}

// readyPrecheckEnv is precheckEnv for a `ready` role (bead pg2-nk6th.3): PATH
// holds only a fake `bd` answering `bd show` with beadJSON (a bd-show-shaped
// single issue), and the role opts in with precheck = "ready" under actor
// "test-actor".
func readyPrecheckEnv(t *testing.T, beadJSON string) roleUnderTest {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", stubBin(t, map[string]string{"bd": "printf '%s' '{\"data\":[" + beadJSON + "]}'\n"}))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	return roleUnderTest{"drain", configFrom(t, `{"ccpool":{"actor":"test-actor","completion":"close-or-release","precheck":"ready","onFailure":"add-human","onDispatchFail":"leave","promptBody":"hello"}}`)}
}

// The precheck's decline reaches the wire through the real dispatch wiring: the
// role's `precheck` setting is read from role.config, the bead shape `bd show`
// really returns (including its dependencies) is decoded, and an own-actor
// in_progress bead (the restart-absorb path) is never declined.
func TestDispatch_readyPrecheck(t *testing.T) {
	const groomed = `"labels":["has-acceptance-criteria"]`
	tests := []struct {
		name       string
		bead       string
		wantReason string
	}{
		{"groomed open bead launches", `{"id":"zr-d","status":"open",` + groomed + `}`, wire.BusyReasonPoolUnreadable},
		{"closed", `{"id":"zr-d","status":"closed",` + groomed + `}`, "skipped-bead-not-open"},
		{"claimed by a peer", `{"id":"zr-d","status":"in_progress","assignee":"someone-else",` + groomed + `}`, "skipped-bead-not-open"},
		{"human", `{"id":"zr-d","status":"open","labels":["has-acceptance-criteria","human"]}`, "skipped-bead-human"},
		{"not groomed", `{"id":"zr-d","status":"open","labels":["x"]}`, "skipped-bead-not-groomed"},
		{"deferred status", `{"id":"zr-d","status":"deferred",` + groomed + `}`, "skipped-bead-deferred"},
		{"future defer_until", `{"id":"zr-d","status":"open","defer_until":"2999-01-01T00:00:00Z",` + groomed + `}`, "skipped-bead-deferred"},
		{"open blocker, bd show dependency shape", `{"id":"zr-d","status":"open",` + groomed + `,"dependencies":[{"id":"zr-b","status":"open","issue_type":"task","dependency_type":"blocks"}]}`, "skipped-bead-blocked"},
		{"own-actor in_progress is absorbed, not declined", `{"id":"zr-d","status":"in_progress","assignee":"test-actor","labels":["human"]}`, wire.BusyReasonPoolUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, reply := runDispatchReply(t, readyPrecheckEnv(t, tc.bead), `{"id":"e-1","type":"dispatch","payload":{"id":"zr-d","type":"task"}}`)
			if code != conformance.ExitBusy || reply["reason"] != tc.wantReason {
				t.Fatalf("got exit %d reply %v, want ExitBusy with %q", code, reply, tc.wantReason)
			}
		})
	}
}
