package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider is a mock Provider used to assert (a) that it satisfies the
// Provider interface's method set (a compile-time assertion, below), and
// (b) that NewDispatchTable wires each op to the right method and passes
// args/results/errors straight through — mirrors
// pkg/provider/thread/dispatch_test.go's identical fakeProvider convention.
type fakeProvider struct {
	listFn       func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error)
	listEventsFn func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
	return f.listFn(ctx, query, idsOnly)
}

func (f *fakeProvider) ListEvents(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
	return f.listEventsFn(ctx, start, end, calendar)
}

// fakeProviderWithAuth additionally implements pkg/provider.AuthChecker, to
// exercise NewDispatchTable's type-check-asserted auth_status entry.
type fakeProviderWithAuth struct {
	fakeProvider
	checkAuthFn func(ctx context.Context) error
}

func (f *fakeProviderWithAuth) CheckAuth(ctx context.Context) error {
	return f.checkAuthFn(ctx)
}

func TestNewDispatchTable_List_ResolvesQueryFromConfig(t *testing.T) {
	var gotQuery schema.QueryExpr
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
			gotQuery = query
			return &schema.CalendarListResult{Entities: []schema.CalendarEvent{{ID: "event-1"}}, PresentIDs: []string{"event-1"}, Truncated: false}, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"soon":"2h"}}`))
	result, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"soon"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(gotQuery) != 1 || gotQuery[0] != "2h" {
		t.Fatalf("query = %#v", gotQuery)
	}
	got, ok := result.(*schema.CalendarListResult)
	if !ok || len(got.PresentIDs) != 1 || got.PresentIDs[0] != "event-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_List_QueryNotRecognized(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
			t.Fatal("List must not be invoked for an unrecognized query name")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"soon":"2h"}}`))
	_, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"nonexistent-name"}`))
	if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Fatalf("err = %v, want errors.Is(err, ErrQueryNotRecognized)", err)
	}
}

func TestNewDispatchTable_List_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
			t.Fatal("List must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_List_UnavailablePassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrUnavailable, "malformed reply")
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"soon":"2h"}}`))
	_, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"soon"}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestNewDispatchTable_ListEvents_DecodesArgsAndInvokesProvider(t *testing.T) {
	wantStart := time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 9, 18, 16, 0, 0, 0, time.UTC)
	var gotStart, gotEnd time.Time
	var gotCalendar string
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			gotStart, gotEnd, gotCalendar = start, end, calendar
			return &schema.CalendarListResult{Entities: []schema.CalendarEvent{{ID: "event-1"}}, PresentIDs: []string{"event-1"}}, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["list_events"]
	if !ok {
		t.Fatal(`table["list_events"] missing`)
	}
	args := json.RawMessage(`{"start":"2026-09-18T15:00:00Z","end":"2026-09-18T16:00:00Z","calendar":"Work"}`)
	result, err := entry.Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) || gotCalendar != "Work" {
		t.Fatalf("start=%v end=%v calendar=%q, want start=%v end=%v calendar=%q", gotStart, gotEnd, gotCalendar, wantStart, wantEnd, "Work")
	}
	got, ok := result.(*schema.CalendarListResult)
	if !ok || len(got.PresentIDs) != 1 || got.PresentIDs[0] != "event-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_ListEvents_EmptyCalendarMeansEveryCalendar(t *testing.T) {
	var gotCalendar string
	calledWithEmpty := false
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			gotCalendar = calendar
			calledWithEmpty = calendar == ""
			return &schema.CalendarListResult{}, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_events"].Handle(context.Background(), json.RawMessage(`{"start":"2026-09-18T15:00:00Z","end":"2026-09-18T16:00:00Z"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !calledWithEmpty {
		t.Fatalf("calendar = %q, want empty string when unset", gotCalendar)
	}
}

func TestNewDispatchTable_ListEvents_MalformedStartIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			t.Fatal("ListEvents must not be invoked when start fails to parse")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_events"].Handle(context.Background(), json.RawMessage(`{"start":"not-a-time","end":"2026-09-18T16:00:00Z"}`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_ListEvents_MalformedEndIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			t.Fatal("ListEvents must not be invoked when end fails to parse")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_events"].Handle(context.Background(), json.RawMessage(`{"start":"2026-09-18T15:00:00Z","end":"not-a-time"}`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_ListEvents_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			t.Fatal("ListEvents must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_events"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_ListEvents_UnavailablePassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrUnavailable, "malformed reply")
	p := &fakeProvider{
		listEventsFn: func(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_events"].Handle(context.Background(), json.RawMessage(`{"start":"2026-09-18T15:00:00Z","end":"2026-09-18T16:00:00Z"}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

// TestNewDispatchTable_NoOtherOps proves this capability's table carries
// exactly list/list_events (plus the optional auth_status) — a regression
// guard against accidentally widening the table beyond what
// NewDispatchTable's own doc comment promises (no Show/Create/Update in
// this round — see iface.go's own package doc comment for why).
func TestNewDispatchTable_NoOtherOps(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	for _, op := range []string{"show", "create", "comment", "transition", "update", "close", "deps"} {
		if _, ok := table[op]; ok {
			t.Fatalf("table[%q] present; the calendar capability has no such op in this round", op)
		}
	}
}

func TestNewDispatchTable_AuthStatusAbsentWithoutAuthChecker(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	if _, ok := table[scriptout.OpAuthStatus]; ok {
		t.Fatal("auth_status entry present for a Provider not implementing AuthChecker")
	}
}

func TestNewDispatchTable_AuthStatusPresentWithAuthChecker_OK(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(ctx context.Context) error { return nil }}
	table := NewDispatchTable(p)
	entry, ok := table[scriptout.OpAuthStatus]
	if !ok {
		t.Fatal("auth_status entry missing for a Provider implementing AuthChecker")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	status, ok := result.(scriptout.AuthStatus)
	if !ok || status.State != scriptout.AuthOK {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_AuthStatusPresentWithAuthChecker_Failure(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(ctx context.Context) error { return errors.New("bad token") }}
	table := NewDispatchTable(p)
	result, err := table[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("auth_status must answer with a well-formed result, not a wire error: %v", err)
	}
	status, ok := result.(scriptout.AuthStatus)
	if !ok || status.State == scriptout.AuthOK || status.Detail == "" {
		t.Fatalf("result = %#v", result)
	}
}
