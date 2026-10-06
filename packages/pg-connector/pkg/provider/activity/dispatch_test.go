package activity

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider records the range it was called with and returns canned values.
type fakeProvider struct {
	called       int
	gotSince     time.Time
	gotBefore    time.Time
	result       *schema.ActivityListResult
	err          error
	listActivity func(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	f.called++
	f.gotSince, f.gotBefore = since, before
	if f.listActivity != nil {
		return f.listActivity(ctx, since, before)
	}
	return f.result, f.err
}

// fakeProviderWithAuth additionally implements pkg/provider.AuthChecker.
type fakeProviderWithAuth struct {
	fakeProvider
	checkAuthFn func(ctx context.Context) error
}

func (f *fakeProviderWithAuth) CheckAuth(ctx context.Context) error { return f.checkAuthFn(ctx) }

func TestNewDispatchTable_OpsWithoutAuthChecker(t *testing.T) {
	got := NewDispatchTable(&fakeProvider{}).Ops()
	if want := []string{"list_activity"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ops() = %v, want %v", got, want)
	}
}

func TestNewDispatchTable_OpsWithAuthChecker(t *testing.T) {
	got := NewDispatchTable(&fakeProviderWithAuth{}).Ops()
	if want := []string{"auth_status", "list_activity"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ops() = %v, want %v", got, want)
	}
}

func TestNewDispatchTable_SchemaVersion(t *testing.T) {
	table := NewDispatchTable(&fakeProviderWithAuth{})
	for _, op := range []string{"list_activity", scriptout.OpAuthStatus} {
		if table[op].SchemaVersion != schema.ActivitySchemaVersion {
			t.Errorf("%s SchemaVersion = %d, want %d", op, table[op].SchemaVersion, schema.ActivitySchemaVersion)
		}
	}
}

func TestListActivity_ValidRangeReachesProvider(t *testing.T) {
	want := &schema.ActivityListResult{Items: []schema.ActivityItem{{ID: "a-1"}}, Truncated: true}
	p := &fakeProvider{result: want}
	args := json.RawMessage(`{"since":"2026-01-01T00:00:00Z","before":"2026-02-01T00:00:00Z"}`)
	got, err := NewDispatchTable(p)["list_activity"].Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got != any(want) {
		t.Fatalf("result = %#v, want the provider's result", got)
	}
	if p.called != 1 {
		t.Fatalf("provider called %d times, want 1", p.called)
	}
	if wantSince := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !p.gotSince.Equal(wantSince) {
		t.Errorf("since = %v, want %v", p.gotSince, wantSince)
	}
	if wantBefore := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); !p.gotBefore.Equal(wantBefore) {
		t.Errorf("before = %v, want %v", p.gotBefore, wantBefore)
	}
}

func TestListActivity_SinceOmittedIsZero(t *testing.T) {
	p := &fakeProvider{result: &schema.ActivityListResult{}}
	_, err := NewDispatchTable(p)["list_activity"].Handle(context.Background(), json.RawMessage(`{"before":"2026-02-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p.called != 1 {
		t.Fatalf("provider called %d times, want 1", p.called)
	}
	if !p.gotSince.IsZero() {
		t.Errorf("since = %v, want zero", p.gotSince)
	}
}

func TestListActivity_InvalidArgs(t *testing.T) {
	cases := map[string]json.RawMessage{
		"no args":             nil,
		"empty object":        json.RawMessage(`{}`),
		"missing before":      json.RawMessage(`{"since":"2026-01-01T00:00:00Z"}`),
		"malformed before":    json.RawMessage(`{"before":"yesterday"}`),
		"malformed since":     json.RawMessage(`{"since":"nope","before":"2026-02-01T00:00:00Z"}`),
		"since equals before": json.RawMessage(`{"since":"2026-02-01T00:00:00Z","before":"2026-02-01T00:00:00Z"}`),
		"since after before":  json.RawMessage(`{"since":"2026-03-01T00:00:00Z","before":"2026-02-01T00:00:00Z"}`),
		"not an object":       json.RawMessage(`"2026-02-01T00:00:00Z"`),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{result: &schema.ActivityListResult{}}
			_, err := NewDispatchTable(p)["list_activity"].Handle(context.Background(), args)
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
			}
			if p.called != 0 {
				t.Fatalf("provider reached %d times for an invalid range", p.called)
			}
		})
	}
}

func TestListActivity_ErrorPassesThroughUnwrapped(t *testing.T) {
	sentinel := scriptout.WrapError(scriptout.ErrUnavailable, "source temporarily unavailable")
	p := &fakeProvider{err: sentinel}
	_, err := NewDispatchTable(p)["list_activity"].Handle(context.Background(), json.RawMessage(`{"before":"2026-02-01T00:00:00Z"}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if err != sentinel {
		t.Fatalf("err = %v, want the provider's own error returned unwrapped", err)
	}
}

func TestAuthStatus_OK(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(ctx context.Context) error { return nil }}
	result, err := NewDispatchTable(p)[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status, ok := result.(scriptout.AuthStatus); !ok || status.State != scriptout.AuthOK {
		t.Fatalf("result = %#v", result)
	}
}

func TestAuthStatus_FailureIsWellFormedResult(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(ctx context.Context) error { return errors.New("bad token") }}
	result, err := NewDispatchTable(p)[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("auth_status must answer with a result, not a wire error: %v", err)
	}
	if status, ok := result.(scriptout.AuthStatus); !ok || status.State == scriptout.AuthOK || status.Detail == "" {
		t.Fatalf("result = %#v", result)
	}
}
