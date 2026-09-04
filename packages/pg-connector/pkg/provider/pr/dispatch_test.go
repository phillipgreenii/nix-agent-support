package pr

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
	showFn        func(ctx context.Context, id string) (*schema.PR, error)
	categorizeFn  func(ctx context.Context, id, category string) (*schema.CategorizeResult, error)
	feedbackSetFn func(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Show(ctx context.Context, id string) (*schema.PR, error) {
	return f.showFn(ctx, id)
}

func (f *fakeProvider) Categorize(ctx context.Context, id, category string) (*schema.CategorizeResult, error) {
	return f.categorizeFn(ctx, id, category)
}

func (f *fakeProvider) FeedbackSet(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error) {
	return f.feedbackSetFn(ctx, id, commentID, disposition)
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
	want := &schema.PR{ID: "pr-1", Title: "hello"}
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.PR, error) {
			if id != "pr-1" {
				t.Fatalf("id = %q, want pr-1", id)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["show"]
	if !ok {
		t.Fatal(`table["show"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.PR)
	if !ok || got.ID != "pr-1" || got.Title != "hello" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Categorize(t *testing.T) {
	p := &fakeProvider{
		categorizeFn: func(ctx context.Context, id, category string) (*schema.CategorizeResult, error) {
			if id != "pr-1" || category != "focus" {
				t.Fatalf("id=%q category=%q", id, category)
			}
			return &schema.CategorizeResult{ID: id, Category: category}, nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["categorize"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1","category":"focus"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.CategorizeResult)
	if !ok || got.ID != "pr-1" || got.Category != "focus" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_FeedbackSet(t *testing.T) {
	p := &fakeProvider{
		feedbackSetFn: func(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error) {
			if id != "pr-1" || commentID != "c1" || disposition != schema.DispositionWontFix {
				t.Fatalf("id=%q commentID=%q disposition=%q", id, commentID, disposition)
			}
			return &schema.FeedbackSetResult{ID: id, CommentID: commentID, Disposition: disposition}, nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["feedback_set"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1","comment_id":"c1","disposition":"wont-fix"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.FeedbackSetResult)
	if !ok || got.CommentID != "c1" || got.Disposition != schema.DispositionWontFix {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_FeedbackSet_NotFoundPassesThroughUnwrapped(t *testing.T) {
	// A not_found response from feedback_set (e.g. the comment id no longer
	// exists) is a well-formed negative answer, not a broken call
	// [design: §4.5, §6.1] — NewDispatchTable must pass the provider's own
	// ErrNotFound-wrapped error through unchanged, not translate it.
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "comment c1 not found")
	p := &fakeProvider{
		feedbackSetFn: func(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error) {
			return nil, sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["feedback_set"].Handle(context.Background(), json.RawMessage(`{"id":"pr-1","comment_id":"c1","disposition":"open"}`))
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
