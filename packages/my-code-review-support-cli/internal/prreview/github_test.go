package prreview

import (
	"context"
	"testing"
)

func TestParsePRNumber(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"12345", 12345},
		{"#12345", 12345},
		{"0", 0},
		{"abc", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := parsePRNumber(tt.input); got != tt.want {
				t.Errorf("parsePRNumber(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"https://github.com/example-org/example-repo/pull/12345", 12345},
		{"https://github.com/org/repo/pull/67890", 67890},
		{"https://github.com/org/repo/pull/12345/files", 12345},
		{"not a url", 0},
		{"https://github.com/org/repo/issues/12345", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := parsePRURL(tt.input); got != tt.want {
				t.Errorf("parsePRURL(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGitHubClient_IdentifyPR_ByNumber(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh pr", MockResponse{
		Stdout: []byte(`{
			"number": 12345,
			"title": "Test PR",
			"headRefName": "feature-branch",
			"baseRefName": "main",
			"url": "https://github.com/example-org/example-repo/pull/12345",
			"state": "OPEN",
			"headRefOid": "abc123"
		}`),
	})

	client := NewGitHubClient("/test", mock)
	pr, err := client.IdentifyPR(context.Background(), "12345")

	if err != nil {
		t.Fatalf("IdentifyPR() error = %v", err)
	}

	if pr.Number != 12345 {
		t.Errorf("Number = %d, want 12345", pr.Number)
	}
	if pr.Title != "Test PR" {
		t.Errorf("Title = %s, want 'Test PR'", pr.Title)
	}
	if pr.HeadRefName != "feature-branch" {
		t.Errorf("HeadRefName = %s, want 'feature-branch'", pr.HeadRefName)
	}
}

func TestGitHubClient_IdentifyPR_ByURL(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh pr", MockResponse{
		Stdout: []byte(`{
			"number": 67890,
			"title": "Another PR",
			"headRefName": "another-branch",
			"baseRefName": "main",
			"url": "https://github.com/example-org/example-repo/pull/67890",
			"state": "OPEN",
			"headRefOid": "def456"
		}`),
	})

	client := NewGitHubClient("/test", mock)
	pr, err := client.IdentifyPR(context.Background(), "https://github.com/example-org/example-repo/pull/67890")

	if err != nil {
		t.Fatalf("IdentifyPR() error = %v", err)
	}

	if pr.Number != 67890 {
		t.Errorf("Number = %d, want 67890", pr.Number)
	}
}

func TestGitHubClient_IdentifyPR_ByBranch(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh pr", MockResponse{
		Stdout: []byte(`[{
			"number": 11111,
			"title": "Branch PR",
			"headRefName": "my-feature",
			"baseRefName": "main",
			"url": "https://github.com/example-org/example-repo/pull/11111",
			"state": "OPEN",
			"headRefOid": "xyz789"
		}]`),
	})

	client := NewGitHubClient("/test", mock)
	pr, err := client.IdentifyPR(context.Background(), "my-feature")

	if err != nil {
		t.Fatalf("IdentifyPR() error = %v", err)
	}

	if pr.Number != 11111 {
		t.Errorf("Number = %d, want 11111", pr.Number)
	}
}

func TestGitHubClient_IdentifyPR_NotFound(t *testing.T) {
	mock := NewMockExecutor()
	mock.DefaultResponse = MockResponse{
		Stdout: []byte(`[]`),
	}

	client := NewGitHubClient("/test", mock)
	_, err := client.IdentifyPR(context.Background(), "nonexistent-branch")

	if err == nil {
		t.Error("IdentifyPR() expected error for not found")
	}
}

func TestGitHubClient_GetPendingReview(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{
		Stdout: []byte(`{"id": 999, "state": "PENDING", "body": "Initial review"}`),
	})

	client := NewGitHubClient("/test", mock)
	review, err := client.GetPendingReview(context.Background(), 12345)

	if err != nil {
		t.Fatalf("GetPendingReview() error = %v", err)
	}

	if review == nil {
		t.Fatal("GetPendingReview() returned nil")
	}

	if review.ID != 999 {
		t.Errorf("ID = %d, want 999", review.ID)
	}
	if review.State != "PENDING" {
		t.Errorf("State = %s, want 'PENDING'", review.State)
	}
}

func TestGitHubClient_GetPendingReview_None(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{
		Stdout: []byte(``),
	})

	client := NewGitHubClient("/test", mock)
	review, err := client.GetPendingReview(context.Background(), 12345)

	if err != nil {
		t.Fatalf("GetPendingReview() error = %v", err)
	}

	if review != nil {
		t.Error("GetPendingReview() expected nil for no pending review")
	}
}

func TestGitHubClient_CreateReview(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{
		Stdout: []byte(`{"id": 1000}`),
	})

	client := NewGitHubClient("/test", mock)

	line := 42
	comments := []GitHubComment{
		{Path: "test.go", Line: &line, Side: "RIGHT", Body: "Test comment\n\n🤖"},
	}

	err := client.CreateReview(context.Background(), 12345, "Review body", comments)

	if err != nil {
		t.Fatalf("CreateReview() error = %v", err)
	}

	if mock.CallCount() == 0 {
		t.Error("Expected at least one call")
	}
}

func TestGitHubClient_AddCommentToReview(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("gh api", MockResponse{
		Stdout: []byte(`{}`),
	})

	client := NewGitHubClient("/test", mock)

	line := 42
	comment := GitHubComment{
		Path: "test.go",
		Line: &line,
		Side: "RIGHT",
		Body: "Comment\n\n🤖",
	}

	err := client.AddCommentToReview(context.Background(), 12345, 999, comment)

	if err != nil {
		t.Fatalf("AddCommentToReview() error = %v", err)
	}

	call := mock.GetCall(0)
	if call == nil {
		t.Fatal("Expected a call")
	}

	args := call.Args
	foundPath := false
	foundLine := false
	for i, arg := range args {
		if arg == "path=test.go" {
			foundPath = true
		}
		if i > 0 && args[i-1] == "-f" && arg == "line=42" {
			foundLine = true
		}
	}
	if !foundPath {
		t.Error("Expected path=test.go in args")
	}
	if !foundLine {
		t.Error("Expected line=42 in args")
	}
}
