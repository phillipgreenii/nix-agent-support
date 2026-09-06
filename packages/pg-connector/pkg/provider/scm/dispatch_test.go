package scm

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
	worktreeAddFn    func(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error)
	worktreeRemoveFn func(ctx context.Context, path string) error
	worktreeListFn   func(ctx context.Context) ([]schema.WorktreeInfo, error)
	branchDetectFn   func(ctx context.Context, cwd string) (*schema.BranchInfo, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) WorktreeAdd(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error) {
	return f.worktreeAddFn(ctx, branchOrRef)
}

func (f *fakeProvider) WorktreeRemove(ctx context.Context, path string) error {
	return f.worktreeRemoveFn(ctx, path)
}

func (f *fakeProvider) WorktreeList(ctx context.Context) ([]schema.WorktreeInfo, error) {
	return f.worktreeListFn(ctx)
}

func (f *fakeProvider) BranchDetect(ctx context.Context, cwd string) (*schema.BranchInfo, error) {
	return f.branchDetectFn(ctx, cwd)
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

func TestNewDispatchTable_WorktreeAdd(t *testing.T) {
	want := &schema.WorktreeInfo{Path: "/w/feature", Branch: "feature", Ref: "feature"}
	p := &fakeProvider{
		worktreeAddFn: func(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error) {
			if branchOrRef != "feature" {
				t.Fatalf("branchOrRef = %q, want feature", branchOrRef)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry, ok := table["worktree_add"]
	if !ok {
		t.Fatal(`table["worktree_add"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"branch_or_ref":"feature"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.WorktreeInfo)
	if !ok || got.Path != "/w/feature" || got.Branch != "feature" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_WorktreeRemove_Success(t *testing.T) {
	p := &fakeProvider{
		worktreeRemoveFn: func(ctx context.Context, path string) error {
			if path != "/w/feature" {
				t.Fatalf("path = %q, want /w/feature", path)
			}
			return nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["worktree_remove"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"path":"/w/feature"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil (no success payload)", result)
	}
}

func TestNewDispatchTable_WorktreeRemove_NotFoundPassesThroughUnwrapped(t *testing.T) {
	// A not_found response (path is not a known worktree) is a well-formed
	// negative answer, not a broken call [design: §4.5, §4.7] —
	// NewDispatchTable must pass the provider's own ErrNotFound-wrapped
	// error through unchanged, not translate it.
	sentinelErr := scriptout.WrapError(scriptout.ErrNotFound, "worktree /w/missing not found")
	p := &fakeProvider{
		worktreeRemoveFn: func(ctx context.Context, path string) error {
			return sentinelErr
		},
	}
	table := NewDispatchTable(p)
	_, err := table["worktree_remove"].Handle(context.Background(), json.RawMessage(`{"path":"/w/missing"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestNewDispatchTable_WorktreeList(t *testing.T) {
	want := []schema.WorktreeInfo{
		{Path: "/w/a", Branch: "a", Ref: "a"},
		{Path: "/w/b", Branch: "b", Ref: "b"},
	}
	p := &fakeProvider{
		worktreeListFn: func(ctx context.Context) ([]schema.WorktreeInfo, error) {
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["worktree_list"]
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.([]schema.WorktreeInfo)
	if !ok || len(got) != 2 || got[0].Path != "/w/a" || got[1].Path != "/w/b" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_BranchDetect(t *testing.T) {
	want := &schema.BranchInfo{Repo: "owner/repo", Branch: "feature"}
	p := &fakeProvider{
		branchDetectFn: func(ctx context.Context, cwd string) (*schema.BranchInfo, error) {
			if cwd != "/home/u/repo" {
				t.Fatalf("cwd = %q, want /home/u/repo", cwd)
			}
			return want, nil
		},
	}
	table := NewDispatchTable(p)
	entry := table["branch_detect"]
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"cwd":"/home/u/repo"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.BranchInfo)
	if !ok || got.Repo != "owner/repo" || got.Branch != "feature" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_WorktreeAdd_DecodeFailureIsInvalidArgument(t *testing.T) {
	// A malformed args payload fails scriptout.Decode -- a caller mistake,
	// not backend ill-health -- so NewDispatchTable must classify it as
	// invalid_argument, not unavailable [design: §4.2, bug pg2-vmfzp].
	p := &fakeProvider{
		worktreeAddFn: func(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error) {
			t.Fatal("WorktreeAdd must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	table := NewDispatchTable(p)
	_, err := table["worktree_add"].Handle(context.Background(), json.RawMessage(`{not valid json`))
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
	p := &fakeProviderWithAuth{checkAuthFn: func(ctx context.Context) error { return errors.New("no git binary") }}
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
