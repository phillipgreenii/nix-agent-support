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
	listFn        func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error)
	filesFn       func(ctx context.Context, id string) (*schema.PRFilesResult, error)
	commitsFn     func(ctx context.Context, id string) (*schema.PRCommitsResult, error)
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

func (f *fakeProvider) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
	return f.listFn(ctx, query, idsOnly)
}

func (f *fakeProvider) Files(ctx context.Context, id string) (*schema.PRFilesResult, error) {
	return f.filesFn(ctx, id)
}

func (f *fakeProvider) Commits(ctx context.Context, id string) (*schema.PRCommitsResult, error) {
	return f.commitsFn(ctx, id)
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
	// (INV-ERR-2) — NewDispatchTable must pass the provider's own
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

func TestNewDispatchTable_Show_DecodeFailureIsInvalidArgument(t *testing.T) {
	// A malformed args payload fails scriptout.Decode -- a caller mistake,
	// not backend ill-health -- so NewDispatchTable must classify it as
	// invalid_argument, not unavailable (INV-ERR-2; bug pg2-vmfzp).
	p := &fakeProvider{
		showFn: func(ctx context.Context, id string) (*schema.PR, error) {
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

// TestNewDispatchTable_List_ResolvesQueryFromConfig proves the "list" entry
// resolves args.query against the request's own config.queries block
// (threaded via scriptout.ConfigFromContext, not the args payload itself)
// before ever calling p.List, and passes the resolved schema.QueryExpr
// through unchanged.
func TestNewDispatchTable_List_ResolvesQueryFromConfig(t *testing.T) {
	var gotQuery schema.QueryExpr
	var gotIDsOnly bool
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
			gotQuery = query
			gotIDsOnly = idsOnly
			return &schema.PRListResult{Entities: []schema.PR{{ID: "pr-1"}}, PresentIDs: []string{"pr-1"}}, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"team":"is:open author:@me"}}`))
	result, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"team","cursor":null,"ids_only":true}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(gotQuery) != 1 || gotQuery[0] != "is:open author:@me" {
		t.Fatalf("query = %#v", gotQuery)
	}
	if !gotIDsOnly {
		t.Fatal("ids_only was not passed through")
	}
	got, ok := result.(*schema.PRListResult)
	if !ok || len(got.PresentIDs) != 1 || got.PresentIDs[0] != "pr-1" {
		t.Fatalf("result = %#v", result)
	}
}

// TestNewDispatchTable_List_QueryNotRecognized proves an unresolvable
// query name is rejected with ErrQueryNotRecognized BEFORE p.List is ever
// called — design's "MUST NOT treat an unrecognized name as a usage
// error, crash, or empty result" is the Provider's obligation not to
// worry about, since the dispatch table itself never reaches it.
func TestNewDispatchTable_List_QueryNotRecognized(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
			t.Fatal("List must not be invoked for an unrecognized query name")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"team":"is:open"}}`))
	_, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"nonexistent-name"}`))
	if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Fatalf("err = %v, want errors.Is(err, ErrQueryNotRecognized)", err)
	}
}

// TestNewDispatchTable_List_NoConfig_QueryNotRecognized proves a request
// with no config member at all (ConfigFromContext returns nil) is treated
// the same as "no queries block" — every name is unrecognized, never a
// nil-pointer panic.
func TestNewDispatchTable_List_NoConfig_QueryNotRecognized(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
			t.Fatal("List must not be invoked when no config was ever registered")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["list"].Handle(context.Background(), json.RawMessage(`{"query":"team"}`))
	if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Fatalf("err = %v, want errors.Is(err, ErrQueryNotRecognized)", err)
	}
}

func TestNewDispatchTable_List_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listFn: func(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error) {
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

func TestNewDispatchTable_Files(t *testing.T) {
	want := &schema.PRFilesResult{ID: "pr-1", Files: []schema.PRFile{{Path: "a.go", Additions: 3, Deletions: 1}}}
	p := &fakeProvider{
		filesFn: func(ctx context.Context, id string) (*schema.PRFilesResult, error) {
			if id != "pr-1" {
				t.Fatalf("id = %q, want pr-1", id)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["files"]
	if !ok {
		t.Fatal(`table["files"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.PRFilesResult)
	if !ok || got.ID != "pr-1" || len(got.Files) != 1 || got.Files[0].Path != "a.go" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Files_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		filesFn: func(ctx context.Context, id string) (*schema.PRFilesResult, error) {
			t.Fatal("Files must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["files"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_Commits(t *testing.T) {
	want := &schema.PRCommitsResult{ID: "pr-1", Commits: []schema.PRCommit{{SHA: "abc123", Author: "alice", Message: "fix"}}}
	p := &fakeProvider{
		commitsFn: func(ctx context.Context, id string) (*schema.PRCommitsResult, error) {
			if id != "pr-1" {
				t.Fatalf("id = %q, want pr-1", id)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["commits"]
	if !ok {
		t.Fatal(`table["commits"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.PRCommitsResult)
	if !ok || got.ID != "pr-1" || len(got.Commits) != 1 || got.Commits[0].Author != "alice" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Commits_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		commitsFn: func(ctx context.Context, id string) (*schema.PRCommitsResult, error) {
			t.Fatal("Commits must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["commits"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
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
