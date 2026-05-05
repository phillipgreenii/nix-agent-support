package prreview

import (
	"context"
	"errors"
	"testing"
)

func TestWorktreeManager_FetchAndVerify_Success(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git fetch", MockResponse{Stdout: []byte("")})
	mock.AddResponse("git rev-parse", MockResponse{Stdout: []byte("abc123\n")})

	manager := NewWorktreeManager("/test", mock)
	err := manager.FetchAndVerify(context.Background(), "feature-branch", "abc123")

	if err != nil {
		t.Fatalf("FetchAndVerify() error = %v", err)
	}

	if mock.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2", mock.CallCount())
	}
}

func TestWorktreeManager_FetchAndVerify_Mismatch(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git fetch", MockResponse{Stdout: []byte("")})
	mock.AddResponse("git rev-parse", MockResponse{Stdout: []byte("different-sha\n")})

	manager := NewWorktreeManager("/test", mock)
	err := manager.FetchAndVerify(context.Background(), "feature-branch", "abc123")

	if err == nil {
		t.Fatal("FetchAndVerify() expected error for SHA mismatch")
	}

	if !errors.Is(err, ErrCommitMismatch) {
		t.Errorf("Expected ErrCommitMismatch, got %v", err)
	}
}

func TestWorktreeManager_FetchAndVerify_FetchFails(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git fetch", MockResponse{
		Stderr: []byte("fatal: couldn't find remote ref"),
		Err:    errors.New("exit status 1"),
	})

	manager := NewWorktreeManager("/test", mock)
	err := manager.FetchAndVerify(context.Background(), "nonexistent-branch", "abc123")

	if err == nil {
		t.Fatal("FetchAndVerify() expected error when fetch fails")
	}
}

func TestWorktreeManager_CreateWorktree_Success(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git worktree", MockResponse{Stdout: []byte("")})

	manager := NewWorktreeManager("/test", mock)
	err := manager.CreateWorktree(context.Background(), "feature-branch", "/tmp/nonexistent-worktree-test-12345")

	if err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
}

func TestWorktreeManager_CreateWorktree_Fails(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git worktree", MockResponse{
		Stderr: []byte("fatal: already exists"),
		Err:    errors.New("exit status 128"),
	})

	manager := NewWorktreeManager("/test", mock)
	err := manager.CreateWorktree(context.Background(), "feature-branch", "/tmp/nonexistent-path")

	if err == nil {
		t.Fatal("CreateWorktree() expected error")
	}
}

func TestWorktreeManager_RemoveWorktree_Success(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git worktree", MockResponse{Stdout: []byte("")})

	manager := NewWorktreeManager("/test", mock)
	err := manager.RemoveWorktree(context.Background(), "/tmp/some-worktree")

	if err != nil {
		t.Fatalf("RemoveWorktree() error = %v", err)
	}
}

func TestWorktreePath(t *testing.T) {
	tests := []struct {
		prNumber int
		want     string
	}{
		{12345, "/tmp/pr-review-12345"},
		{1, "/tmp/pr-review-1"},
		{99999, "/tmp/pr-review-99999"},
	}

	for _, tt := range tests {
		got := WorktreePath(tt.prNumber)
		if got != tt.want {
			t.Errorf("WorktreePath(%d) = %s, want %s", tt.prNumber, got, tt.want)
		}
	}
}
