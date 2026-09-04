package ci

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider is a mock Provider used to assert (a) that it satisfies the
// Provider interface's method set, and (b) that NewDispatchTable wires each
// op to the right method and passes args/results/errors straight through.
type fakeProvider struct {
	listRunsFn    func(ctx context.Context, prID string) ([]schema.CIRun, error)
	getLogsFn     func(ctx context.Context, runID string) ([]byte, error)
	rerunFailedFn func(ctx context.Context, prID string) error
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) ListRuns(ctx context.Context, prID string) ([]schema.CIRun, error) {
	return f.listRunsFn(ctx, prID)
}

func (f *fakeProvider) GetLogs(ctx context.Context, runID string) ([]byte, error) {
	return f.getLogsFn(ctx, runID)
}

func (f *fakeProvider) RerunFailed(ctx context.Context, prID string) error {
	return f.rerunFailedFn(ctx, prID)
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

func TestNewDispatchTable_ListRuns(t *testing.T) {
	want := []schema.CIRun{{ID: "run-1", PRID: "pr-1", Status: "completed"}}
	p := &fakeProvider{
		listRunsFn: func(ctx context.Context, prID string) ([]schema.CIRun, error) {
			if prID != "pr-1" {
				t.Fatalf("prID = %q, want pr-1", prID)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["list_runs"]
	if !ok {
		t.Fatal(`table["list_runs"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"pr_id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.([]schema.CIRun)
	if !ok || len(got) != 1 || got[0].ID != "run-1" || got[0].PRID != "pr-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_GetLogs(t *testing.T) {
	p := &fakeProvider{
		getLogsFn: func(ctx context.Context, runID string) ([]byte, error) {
			if runID != "run-1" {
				t.Fatalf("runID = %q, want run-1", runID)
			}
			return []byte("log output"), nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["get_logs"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"run_id":"run-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.([]byte)
	if !ok || string(got) != "log output" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_GetLogs_NotFoundPassesThroughUnwrapped(t *testing.T) {
	// A not_found response from get_logs (e.g. the run id no longer exists)
	// is a well-formed negative answer, not a broken call
	// [design: §4.5] — NewDispatchTable must pass the provider's own
	// ErrNotFound-wrapped error through unchanged, not translate it.
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "run run-1 not found")
	p := &fakeProvider{
		getLogsFn: func(ctx context.Context, runID string) ([]byte, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["get_logs"].Handle(context.Background(), json.RawMessage(`{"run_id":"run-1"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestNewDispatchTable_RerunFailed(t *testing.T) {
	p := &fakeProvider{
		rerunFailedFn: func(ctx context.Context, prID string) error {
			if prID != "pr-1" {
				t.Fatalf("prID = %q, want pr-1", prID)
			}
			return nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["rerun_failed"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"pr_id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
}

func TestNewDispatchTable_RerunFailed_NotFoundPassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "pr pr-1 not found")
	p := &fakeProvider{
		rerunFailedFn: func(ctx context.Context, prID string) error {
			return sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["rerun_failed"].Handle(context.Background(), json.RawMessage(`{"pr_id":"pr-1"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
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

func TestNewDispatchTable_SchemaVersionIsCISchemaVersion(t *testing.T) {
	// Every ci-capability dispatch-table entry must carry the ci
	// capability's own schema version, never pr's [design: §4.3].
	p := &fakeProvider{
		listRunsFn:    func(ctx context.Context, prID string) ([]schema.CIRun, error) { return nil, nil },
		getLogsFn:     func(ctx context.Context, runID string) ([]byte, error) { return nil, nil },
		rerunFailedFn: func(ctx context.Context, prID string) error { return nil },
	}
	table := NewDispatchTable(p)
	for _, op := range []string{"list_runs", "get_logs", "rerun_failed"} {
		entry, ok := table[op]
		if !ok {
			t.Fatalf("table[%q] missing", op)
		}
		if entry.SchemaVersion != schema.CISchemaVersion {
			t.Errorf("table[%q].SchemaVersion = %d, want %d", op, entry.SchemaVersion, schema.CISchemaVersion)
		}
	}
}
