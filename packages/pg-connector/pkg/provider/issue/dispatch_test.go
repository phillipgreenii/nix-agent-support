package issue

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
// args/results/errors straight through.
type fakeProvider struct {
	showFn       func(ctx context.Context, id string) (*schema.Issue, error)
	createFn     func(ctx context.Context, input IssueInput) (*schema.Issue, error)
	commentFn    func(ctx context.Context, id, body string) error
	transitionFn func(ctx context.Context, id, targetState string) error
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Show(ctx context.Context, id string) (*schema.Issue, error) {
	return f.showFn(ctx, id)
}

func (f *fakeProvider) Create(ctx context.Context, input IssueInput) (*schema.Issue, error) {
	return f.createFn(ctx, input)
}

func (f *fakeProvider) Comment(ctx context.Context, id, body string) error {
	return f.commentFn(ctx, id, body)
}

func (f *fakeProvider) Transition(ctx context.Context, id, targetState string) error {
	return f.transitionFn(ctx, id, targetState)
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
	want := &schema.Issue{ID: "issue-1", Title: "hello", State: "open"}
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Issue, error) {
			if id != "issue-1" {
				t.Fatalf("id = %q, want issue-1", id)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["show"]
	if !ok {
		t.Fatal(`table["show"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"issue-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.Issue)
	if !ok || got.ID != "issue-1" || got.Title != "hello" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Show_NotFoundPassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "issue issue-404 not found")
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Issue, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`{"id":"issue-404"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestNewDispatchTable_Create(t *testing.T) {
	p := &fakeProvider{
		createFn: func(ctx context.Context, input IssueInput) (*schema.Issue, error) {
			if input.Title != "new issue" || input.Priority != "High" || input.IssueType != "Bug" {
				t.Fatalf("input = %#v", input)
			}
			if len(input.Labels) != 2 || input.Labels[0] != "a" || input.Labels[1] != "b" {
				t.Fatalf("input.Labels = %#v", input.Labels)
			}
			return &schema.Issue{ID: "issue-2", Title: input.Title, State: "open", Priority: input.Priority, IssueType: input.IssueType, Labels: input.Labels}, nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["create"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"title":"new issue","priority":"High","issue_type":"Bug","labels":["a","b"]}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.Issue)
	if !ok || got.ID != "issue-2" || got.Title != "new issue" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Comment(t *testing.T) {
	var gotID, gotBody string
	p := &fakeProvider{
		commentFn: func(ctx context.Context, id, body string) error {
			gotID, gotBody = id, body
			return nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["comment"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"issue-1","body":"a comment"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil (Comment reports no value of its own)", result)
	}
	if gotID != "issue-1" || gotBody != "a comment" {
		t.Fatalf("id=%q body=%q", gotID, gotBody)
	}
}

func TestNewDispatchTable_Comment_NotFoundPassesThroughUnwrapped(t *testing.T) {
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "issue issue-404 not found")
	p := &fakeProvider{
		commentFn: func(ctx context.Context, id, body string) error {
			return sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["comment"].Handle(context.Background(), json.RawMessage(`{"id":"issue-404","body":"x"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestNewDispatchTable_Transition(t *testing.T) {
	var gotID, gotState string
	p := &fakeProvider{
		transitionFn: func(ctx context.Context, id, targetState string) error {
			gotID, gotState = id, targetState
			return nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["transition"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"issue-1","target_state":"Done"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil (Transition reports no value of its own)", result)
	}
	if gotID != "issue-1" || gotState != "Done" {
		t.Fatalf("id=%q targetState=%q", gotID, gotState)
	}
}

func TestNewDispatchTable_Transition_UnrecognizedTargetStateIsProviderError(t *testing.T) {
	// targetState's valid values are declared per-backend via capabilities,
	// never a shared Go enum — this table performs no client-side
	// vocabulary validation of its own; an unrecognized value is entirely
	// the concrete Provider's own well-formed error to report, and this
	// table must pass it through unwrapped rather than reject the call
	// itself before ever invoking Transition.
	vocabErr := scriptout.WrapError(scriptout.ErrUnavailable, `target state "bogus" not in vocabulary`)
	called := false
	p := &fakeProvider{
		transitionFn: func(ctx context.Context, id, targetState string) error {
			called = true
			return vocabErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["transition"].Handle(context.Background(), json.RawMessage(`{"id":"issue-1","target_state":"bogus"}`))
	if !called {
		t.Fatal("Transition was never invoked; vocabulary must be validated by the provider, not pre-filtered by this table")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatal("a vocabulary mismatch must not be reported as not_found")
	}
}

func TestNewDispatchTable_Show_DecodeFailureIsInvalidArgument(t *testing.T) {
	// A malformed args payload fails scriptout.Decode -- a caller mistake,
	// not backend ill-health -- so NewDispatchTable must classify it as
	// invalid_argument, not unavailable [design: §4.2, bug pg2-vmfzp].
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.Issue, error) {
			t.Fatal("Show must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`{not valid json`))
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
