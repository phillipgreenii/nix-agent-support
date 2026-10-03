package internal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Bead pg2-ltddq: the backend notes, on the in-flight call's event, how many
// pjira executions it made, the auth-status state, and whether a failure read
// as a throttle. These tests drive the real Backend through the real issue and
// search dispatch tables under eventlog.Instrument and assert the event the
// backend's own log would receive, and that the wire error is the one it
// always was.

type captureSink struct {
	mu     sync.Mutex
	events []eventlog.Event
}

func (c *captureSink) Write(ev eventlog.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *captureSink) last(t *testing.T) eventlog.Event {
	t.Helper()
	if len(c.events) == 0 {
		t.Fatal("no event written")
	}
	return c.events[len(c.events)-1]
}

func instrumentedTable(b *Backend, sink eventlog.Sink) scriptout.DispatchTable {
	table := issue.NewDispatchTable(b)
	for op, h := range search.NewDispatchTable(b) {
		table[op] = h
	}
	return eventlog.Instrument(table, sink, "test", time.Now)
}

const okIssue = `{"key":"PROJ-1","summary":"hello","status":"To Do","issuetype":"Task"}`

func TestBackendEvents_ShowOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		runner    func([]string) (string, error)
		wantErr   error
		wantCode  string
		wantClass string
	}{
		{"success", func([]string) (string, error) { return okIssue, nil }, nil, "", ""},
		{
			"not found", func([]string) (string, error) { return "", errors.New("exit status 1: pjira: issue PROJ-1 not found") },
			scriptout.ErrNotFound, "not_found", "",
		},
		{
			"401 is an auth failure", func([]string) (string, error) {
				return "", errors.New("exit status 1: pjira: get issue PROJ-1: status 401 Unauthorized")
			},
			scriptout.ErrUnauthenticated, "unauthenticated", eventlog.ClassAuth,
		},
		{
			"403 is an auth failure", func([]string) (string, error) {
				return "", errors.New("exit status 1: pjira: get issue PROJ-1: status 403 Forbidden")
			},
			scriptout.ErrUnauthenticated, "unauthenticated", eventlog.ClassAuth,
		},
		{
			"429 stays unavailable on the wire but is tagged rate_limited", func([]string) (string, error) {
				return "", errors.New("exit status 1: pjira: get issue PROJ-1: status 429 Too Many Requests")
			},
			scriptout.ErrUnavailable, "unavailable", eventlog.ClassRateLimited,
		},
		{
			"pjira missing", func([]string) (string, error) {
				return "", errors.New("pjira issue -- PROJ-1: exec: \"pjira\": executable file not found in $PATH (is pjira on PATH?)")
			},
			scriptout.ErrUnavailable, "unavailable", "",
		},
		{
			"5xx", func([]string) (string, error) {
				return "", errors.New("exit status 1: pjira: get issue PROJ-1: status 503 Service Unavailable")
			},
			scriptout.ErrUnavailable, "unavailable", "",
		},
		{
			"undecodable output", func([]string) (string, error) { return "not json", nil },
			scriptout.ErrUnavailable, "unavailable", "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &captureSink{}
			table := instrumentedTable(New(&fakeRunner{handle: c.runner}), sink)
			_, err := table["show"].Handle(context.Background(), json.RawMessage(`{"id":"PROJ-1"}`))
			if c.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("error = %v, want %v (the instrumented call must keep returning the same error)", err, c.wantErr)
			}
			ev := sink.last(t)
			if ev.Op != "show" || ev.ErrorCode != c.wantCode || ev.FailureClass != c.wantClass || ev.PjiraCalls != 1 {
				t.Errorf("event = %+v\nwant code=%q class=%q pjira_calls=1", ev, c.wantCode, c.wantClass)
			}
		})
	}
}

func TestBackendEvents_SearchCountsPjiraCalls(t *testing.T) {
	sink := &captureSink{}
	table := instrumentedTable(New(&fakeRunner{handle: func([]string) (string, error) {
		return `{"issues":[]}`, nil
	}}), sink)
	if _, err := table["search"].Handle(context.Background(), json.RawMessage(`{"query":"project = PROJ"}`)); err != nil {
		t.Fatal(err)
	}
	if ev := sink.last(t); ev.Op != "search" || ev.PjiraCalls != 1 || ev.ErrorCode != "" {
		t.Errorf("search event = %+v", ev)
	}
}

func TestBackendEvents_AuthStatus(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		runErr     error
		wantState  string
		wantClass  string
		wantLevel  string
		wantDetail string // wire Detail for a failed check (unchanged by this change)
	}{
		{"ok", "OK\n", nil, "OK", "", "info", ""},
		{"missing", "MISSING\n", errors.New("exit status 3"), "MISSING", eventlog.ClassAuth, "error", "pjira auth-status: exit status 3"},
		{"unauthenticated", "UNAUTHENTICATED\n", errors.New("exit status 5"), "UNAUTHENTICATED", eventlog.ClassAuth, "error", "pjira auth-status: exit status 5"},
		{"forbidden", "FORBIDDEN\n", errors.New("exit status 4"), "FORBIDDEN", eventlog.ClassAuth, "error", "pjira auth-status: exit status 4"},
		{"transport failure", "ERROR\n", errors.New("exit status 1: pjira auth-status: dial tcp: no such host"), "ERROR", "", "warn", ""},
		{"pjira missing", "", errors.New("exec: \"pjira\": executable file not found in $PATH"), "ERROR", "", "warn", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &captureSink{}
			table := instrumentedTable(New(&fakeRunner{handle: func([]string) (string, error) { return c.out, c.runErr }}), sink)
			res, err := table[scriptout.OpAuthStatus].Handle(context.Background(), nil)
			if err != nil {
				t.Fatalf("auth_status must stay a well-formed answer, got wire error %v", err)
			}
			st, ok := res.(scriptout.AuthStatus)
			if !ok {
				t.Fatalf("result type = %T", res)
			}
			if (c.wantState == "OK") != (st.State == scriptout.AuthOK) {
				t.Errorf("wire state = %q for pjira state %q", st.State, c.wantState)
			}
			ev := sink.last(t)
			if ev.Op != scriptout.OpAuthStatus || ev.AuthState != c.wantState || ev.FailureClass != c.wantClass ||
				ev.Level != c.wantLevel || ev.ErrorCode != "" || ev.PjiraCalls != 1 {
				t.Errorf("event = %+v\nwant state=%q class=%q level=%q", ev, c.wantState, c.wantClass, c.wantLevel)
			}
		})
	}
}

// Without an Instrument wrapper (every other test in this package) the
// Record* calls are no-ops, so Backend behaves exactly as before.
func TestBackendEvents_UninstrumentedBackendIsUnaffected(t *testing.T) {
	b := New(&fakeRunner{handle: func([]string) (string, error) { return okIssue, nil }})
	if _, err := b.Show(context.Background(), "PROJ-1"); err != nil {
		t.Fatal(err)
	}
	if got := b.runner.Binary(); got != "pjira" {
		t.Errorf("Binary() = %q: the recording decorator must pass Binary through", got)
	}
}
