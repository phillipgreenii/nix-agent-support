package prreview

import (
	"context"
	"testing"
)

func TestClient_Setup_Success(t *testing.T) {
	mock := NewMockExecutor()

	mock.AddResponse("gh pr", MockResponse{
		Stdout: []byte(`{
			"number": 12345,
			"title": "Test PR",
			"headRefName": "feature-branch",
			"baseRefName": "main",
			"url": "https://github.com/example-org/example-repo/pull/12345",
			"state": "OPEN",
			"headRefOid": "abc123def456"
		}`),
	})

	mock.AddResponse("git fetch", MockResponse{Stdout: []byte("")})
	mock.AddResponse("git rev-parse", MockResponse{Stdout: []byte("abc123def456\n")})
	mock.AddResponse("git worktree", MockResponse{Stdout: []byte("")})

	client := NewClientWithExecutor("/test", mock)
	result, err := client.Setup(context.Background(), "12345")

	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if result.PRNumber != 12345 {
		t.Errorf("PRNumber = %d, want 12345", result.PRNumber)
	}
	if result.Title != "Test PR" {
		t.Errorf("Title = %s, want 'Test PR'", result.Title)
	}
	if result.HeadBranch != "feature-branch" {
		t.Errorf("HeadBranch = %s, want 'feature-branch'", result.HeadBranch)
	}
	if result.BaseBranch != "main" {
		t.Errorf("BaseBranch = %s, want 'main'", result.BaseBranch)
	}
	if result.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %s, want 'abc123def456'", result.CommitSHA)
	}
	if result.WorktreePath != "/tmp/pr-review-12345" {
		t.Errorf("WorktreePath = %s, want '/tmp/pr-review-12345'", result.WorktreePath)
	}
}

func TestClient_Post_NoComments(t *testing.T) {
	mock := NewMockExecutor()

	client := NewClientWithExecutor("/test", mock)
	result, err := client.Post(context.Background(), 12345, `{"comments": []}`)

	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	if result.CommentsPosted != 0 {
		t.Errorf("CommentsPosted = %d, want 0", result.CommentsPosted)
	}
	if result.Mode != "none" {
		t.Errorf("Mode = %s, want 'none'", result.Mode)
	}
}

func TestClient_Post_NewReview(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{Stdout: []byte("")})

	client := NewClientWithExecutor("/test", mock)

	input := `{
		"comments": [
			{"path": "test.go", "lines": [42], "message": "Issue here", "severity": "error"}
		]
	}`

	result, err := client.Post(context.Background(), 12345, input)

	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	if result.CommentsPosted != 1 {
		t.Errorf("CommentsPosted = %d, want 1", result.CommentsPosted)
	}
	if result.Mode != "created_new" {
		t.Errorf("Mode = %s, want 'created_new'", result.Mode)
	}
}

func TestClient_Post_WithPRLevelComment(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{Stdout: []byte("")})

	client := NewClientWithExecutor("/test", mock)

	input := `{
		"comments": [
			{"path": null, "lines": null, "message": "Overall: add more tests", "severity": "suggestion"}
		]
	}`

	result, err := client.Post(context.Background(), 12345, input)

	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	if result.PRLevelComments != 1 {
		t.Errorf("PRLevelComments = %d, want 1", result.PRLevelComments)
	}
}

func TestClient_Post_ExtractsJSONFromText(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{Stdout: []byte("")})

	client := NewClientWithExecutor("/test", mock)

	input := `## Review Summary

Here are the issues I found:

` + "```json" + `
{
  "comments": [
    {"path": "main.go", "lines": [10], "message": "Bug", "severity": "error"}
  ]
}
` + "```" + `

Please fix these issues.`

	result, err := client.Post(context.Background(), 12345, input)

	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	if result.CommentsPosted != 1 {
		t.Errorf("CommentsPosted = %d, want 1", result.CommentsPosted)
	}
}

func TestClient_Post_InvalidJSON(t *testing.T) {
	mock := NewMockExecutor()

	client := NewClientWithExecutor("/test", mock)

	_, err := client.Post(context.Background(), 12345, "no json here")

	if err == nil {
		t.Error("Post() expected error for invalid input")
	}
}

func TestClient_Cleanup_Success(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git worktree", MockResponse{Stdout: []byte("")})

	client := NewClientWithExecutor("/test", mock)
	result, err := client.Cleanup(context.Background(), "/tmp/pr-review-12345")

	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	if result.Status != "ok" {
		t.Errorf("Status = %s, want 'ok'", result.Status)
	}
}

func TestClient_NewClient(t *testing.T) {
	client := NewClient("/test/path")

	if client == nil {
		t.Fatal("NewClient() returned nil")
	}
	if client.workDir != "/test/path" {
		t.Errorf("workDir = %s, want '/test/path'", client.workDir)
	}
}
