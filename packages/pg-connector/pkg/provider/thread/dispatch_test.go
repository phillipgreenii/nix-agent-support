package thread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider is a mock Provider used to assert (a) that it satisfies the
// Provider interface's method set (a compile-time assertion, below), and
// (b) that NewDispatchTable wires each op to the right method and passes
// args/results/errors straight through — mirrors
// pkg/provider/issue/dispatch_test.go's identical fakeProvider convention.
type fakeProvider struct {
	showFn func(ctx context.Context, id string) (*schema.Thread, error)
	listFn func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Show(ctx context.Context, id string) (*schema.Thread, error) {
	return f.showFn(ctx, id)
}

func (f *fakeProvider) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error) {
	return f.listFn(ctx, query, idsOnly)
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

func TestNewDispatchTable_Show(t *testing.T) {
	want := &schema.Thread{ID: "thread-1", Channel: "C1", Text: "hello"}
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Thread, error) {
			if id != "thread-1" {
				t.Fatalf("id = %q, want thread-1", id)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["show"]
	if !ok {
		t.Fatal(`table["show"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"thread-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.Thread)
	if !ok || got.ID != "thread-1" || got.Text != "hello" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Show_UnavailablePassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrUnavailable, "malformed reply")
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Thread, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`{"id":"thread-404"}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestNewDispatchTable_Show_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Thread, error) {
			t.Fatal("Show must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_List_ResolvesQueryFromConfig(t *testing.T) {
	var gotQuery schema.QueryExpr
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error) {
			gotQuery = query
			return &schema.ThreadListResult{Entities: []schema.Thread{{ID: "thread-1"}}, PresentIDs: []string{"thread-1"}, Truncated: true}, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"involving-me":"involving-me"}}`))
	result, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"involving-me"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(gotQuery) != 1 || gotQuery[0] != "involving-me" {
		t.Fatalf("query = %#v", gotQuery)
	}
	got, ok := result.(*schema.ThreadListResult)
	if !ok || len(got.PresentIDs) != 1 || got.PresentIDs[0] != "thread-1" || !got.Truncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_List_QueryNotRecognized(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error) {
			t.Fatal("List must not be invoked for an unrecognized query name")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"involving-me":"involving-me"}}`))
	_, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"nonexistent-name"}`))
	if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Fatalf("err = %v, want errors.Is(err, ErrQueryNotRecognized)", err)
	}
}

func TestNewDispatchTable_List_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error) {
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

// TestNewDispatchTable_NoWriteOps proves this capability's table carries
// no write-op entries at all (Provider declares only Show/List — see
// iface.go's own doc comment for why) — a regression guard against
// accidentally widening the table beyond what NewDispatchTable's own doc
// comment promises.
func TestNewDispatchTable_NoWriteOps(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	for _, op := range []string{"create", "comment", "transition", "update", "close", "deps"} {
		if _, ok := table[op]; ok {
			t.Fatalf("table[%q] present; the thread capability has no write ops", op)
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
