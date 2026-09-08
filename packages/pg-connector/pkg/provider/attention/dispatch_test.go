package attention

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
// list_attention to the right method and passes results/errors straight
// through.
type fakeProvider struct {
	listAttentionFn func(ctx context.Context) ([]schema.AttentionItem, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	return f.listAttentionFn(ctx)
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

func TestNewDispatchTable_ListAttention(t *testing.T) {
	want := []schema.AttentionItem{{Type: "pr", ID: "pr-1", Summary: "needs review", Severity: schema.SeverityHigh}}
	p := &fakeProvider{
		listAttentionFn: func(ctx context.Context) ([]schema.AttentionItem, error) {
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["list_attention"]
	if !ok {
		t.Fatal(`table["list_attention"] missing`)
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.([]schema.AttentionItem)
	if !ok || len(got) != 1 || got[0].ID != "pr-1" || got[0].Severity != schema.SeverityHigh {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_ListAttention_SchemaVersion(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{listAttentionFn: func(ctx context.Context) ([]schema.AttentionItem, error) {
		return nil, nil
	}})
	if table["list_attention"].SchemaVersion != schema.AttentionSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", table["list_attention"].SchemaVersion, schema.AttentionSchemaVersion)
	}
}

func TestNewDispatchTable_ListAttention_ErrorPassesThroughUnwrapped(t *testing.T) {
	// A well-behaved Provider wraps its own errors with the matching
	// pkg/scriptout.Err* sentinel; NewDispatchTable must pass that error
	// through unchanged, not translate it.
	sentinelErr := scriptout.WrapError(scriptout.ErrUnavailable, "source temporarily unavailable")
	p := &fakeProvider{
		listAttentionFn: func(ctx context.Context) ([]schema.AttentionItem, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list_attention"].Handle(context.Background(), nil)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestNewDispatchTable_ListAttention_IgnoresArgs(t *testing.T) {
	// list_attention takes no arguments (interfaces.md's attention op
	// catalog) — a caller sending an args payload anyway must not break the
	// handler, since it never attempts to decode args at all.
	called := false
	p := &fakeProvider{
		listAttentionFn: func(ctx context.Context) ([]schema.AttentionItem, error) {
			called = true
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	if _, err := table["list_attention"].Handle(context.Background(), json.RawMessage(`{"unexpected":"field"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Fatal("ListAttention was not called")
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
