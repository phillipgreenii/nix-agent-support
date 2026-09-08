package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider is a mock Provider used to assert (a) that it satisfies the
// Provider interface's method set, and (b) that NewDispatchTable wires
// search to the right method and passes args/results/errors straight
// through.
type fakeProvider struct {
	searchFn func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Search(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
	return f.searchFn(ctx, query, fields)
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

func TestNewDispatchTable_Search(t *testing.T) {
	want := []schema.SearchResult{{Type: "pr", ID: "pr-1", Title: "Fix the thing", URL: "https://example.invalid/pr-1", Source: "pg-connector-pr-github"}}
	p := &fakeProvider{
		searchFn: func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
			if query != "fix" {
				t.Fatalf("query = %q, want %q", query, "fix")
			}
			if len(fields) != 2 || fields[0] != "branch" || fields[1] != "status" {
				t.Fatalf("fields = %#v", fields)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["search"]
	if !ok {
		t.Fatal(`table["search"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"query":"fix","fields":["branch","status"]}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.([]schema.SearchResult)
	if !ok || len(got) != 1 || got[0].ID != "pr-1" || got[0].Source != "pg-connector-pr-github" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Search_EmptyFields(t *testing.T) {
	// fields MAY be empty ("no specific attributes requested") — the
	// handler must not choke on an absent fields key.
	called := false
	p := &fakeProvider{
		searchFn: func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
			called = true
			if len(fields) != 0 {
				t.Fatalf("fields = %#v, want empty", fields)
			}
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	if _, err := table["search"].Handle(context.Background(), json.RawMessage(`{"query":"fix"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Fatal("Search was not called")
	}
}

func TestNewDispatchTable_Search_SchemaVersion(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{searchFn: func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
		return nil, nil
	}})
	if table["search"].SchemaVersion != schema.SearchSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", table["search"].SchemaVersion, schema.SearchSchemaVersion)
	}
}

func TestNewDispatchTable_Search_ErrorPassesThroughUnwrapped(t *testing.T) {
	// A well-behaved Provider wraps its own errors with the matching
	// pkg/scriptout.Err* sentinel; NewDispatchTable must pass that error
	// through unchanged, not translate it.
	sentinelErr := scriptout.WrapError(scriptout.ErrUnavailable, "source temporarily unavailable")
	p := &fakeProvider{
		searchFn: func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["search"].Handle(context.Background(), json.RawMessage(`{"query":"fix"}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestNewDispatchTable_Search_DecodeFailureIsInvalidArgument(t *testing.T) {
	// A malformed args payload fails scriptout.Decode -- a caller mistake,
	// not backend ill-health -- so NewDispatchTable must classify it as
	// invalid_argument, not unavailable (INV-ERR-2).
	p := &fakeProvider{
		searchFn: func(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
			t.Fatal("Search must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["search"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatal("a decode failure must not be reported as unavailable")
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
