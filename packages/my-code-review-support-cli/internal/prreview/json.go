package prreview

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractJSON extracts a JSON object from text by finding the first { and last }.
// This allows parsing JSON embedded in other output (like agent responses).
func ExtractJSON(input string) (string, error) {
	firstBrace := strings.Index(input, "{")
	if firstBrace == -1 {
		return "", ErrNoJSONFound
	}

	lastBrace := strings.LastIndex(input, "}")
	if lastBrace == -1 || lastBrace < firstBrace {
		return "", ErrNoJSONFound
	}

	jsonStr := input[firstBrace : lastBrace+1]

	var test interface{}
	if err := json.Unmarshal([]byte(jsonStr), &test); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidJSON, err.Error())
	}

	return jsonStr, nil
}

// ParseReviewOutput parses the review subagent's JSON output.
func ParseReviewOutput(jsonStr string) (*ReviewOutput, error) {
	var output ReviewOutput
	if err := json.Unmarshal([]byte(jsonStr), &output); err != nil {
		return nil, fmt.Errorf("unmarshal review output: %w", err)
	}
	return &output, nil
}

// TransformToGitHubComments transforms review comments to GitHub API format.
// Returns (fileComments, prLevelComments).
func TransformToGitHubComments(comments []ReviewComment) ([]GitHubComment, []ReviewComment) {
	var fileComments []GitHubComment
	var prLevelComments []ReviewComment

	for _, c := range comments {
		if c.Path == nil {
			prLevelComments = append(prLevelComments, c)
			continue
		}

		body := c.Message + "\n\n🤖"
		ghComment := GitHubComment{
			Path: *c.Path,
			Body: body,
		}

		if c.Lines == nil {
			ghComment.SubjectType = "file"
		} else if len(c.Lines) == 1 {
			line := c.Lines[0]
			ghComment.Line = &line
			ghComment.Side = "RIGHT"
		} else if len(c.Lines) >= 2 {
			startLine := c.Lines[0]
			endLine := c.Lines[len(c.Lines)-1]
			ghComment.StartLine = &startLine
			ghComment.Line = &endLine
			ghComment.Side = "RIGHT"
		}

		fileComments = append(fileComments, ghComment)
	}

	return fileComments, prLevelComments
}

// BuildReviewBody builds the review body from PR-level comments.
func BuildReviewBody(comments []ReviewComment) string {
	if len(comments) == 0 {
		return ""
	}

	var lines []string
	for _, c := range comments {
		lines = append(lines, "- **["+c.Severity+"]** "+c.Message)
	}

	return strings.Join(lines, "\n\n") + "\n\n🤖"
}

// IsDuplicate checks if a new comment is a duplicate of an existing one.
// Compares path, line, and first 100 characters of body.
func IsDuplicate(newComment GitHubComment, existing []ExistingComment) bool {
	for _, e := range existing {
		if newComment.Path != e.Path {
			continue
		}

		if !linesMatch(newComment.Line, e.Line) {
			continue
		}

		newBody := truncate(newComment.Body, 100)
		existingBody := truncate(e.Body, 100)
		if newBody == existingBody {
			return true
		}
	}
	return false
}

// DeduplicateComments removes comments that already exist in the review.
func DeduplicateComments(comments []GitHubComment, existing []ExistingComment) (unique []GitHubComment, skipped int) {
	for _, c := range comments {
		if IsDuplicate(c, existing) {
			skipped++
		} else {
			unique = append(unique, c)
		}
	}
	return unique, skipped
}

func linesMatch(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
