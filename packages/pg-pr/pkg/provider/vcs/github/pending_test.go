package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// pendingReviewJSON builds a gh GraphQL response for the pending-review query
// with the given viewer login and review (author-login, state) pairs.
func pendingReviewJSON(viewer string, reviews [][2]string) string {
	var nodes []string
	for _, r := range reviews {
		nodes = append(nodes, `{"author":{"login":"`+r[0]+`"},"state":"`+r[1]+`"}`)
	}
	return `{"data":{"viewer":{"login":"` + viewer + `"},` +
		`"repository":{"pullRequest":{"reviews":{"nodes":[` + strings.Join(nodes, ",") + `]}}}}}`
}

func TestHasPendingReviewByViewer_ViewerPending_True(t *testing.T) {
	raw := pendingReviewJSON("me", [][2]string{{"me", "PENDING"}})
	p := NewWithRunner(&fakeStdinRunner{out: []byte(raw)})

	got, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("HasPendingReviewByViewer: %v", err)
	}
	if !got {
		t.Fatalf("want true when the viewer has a PENDING review")
	}
}

func TestHasPendingReviewByViewer_OtherPending_False(t *testing.T) {
	raw := pendingReviewJSON("me", [][2]string{{"someone-else", "PENDING"}})
	p := NewWithRunner(&fakeStdinRunner{out: []byte(raw)})

	got, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("HasPendingReviewByViewer: %v", err)
	}
	if got {
		t.Fatalf("want false when only OTHER users have PENDING reviews")
	}
}

func TestHasPendingReviewByViewer_OnlySubmitted_False(t *testing.T) {
	// The query is asked to filter states:[PENDING], but harden the client so a
	// submitted review by the viewer never counts as pending.
	raw := pendingReviewJSON("me", [][2]string{{"me", "APPROVED"}, {"me", "COMMENTED"}})
	p := NewWithRunner(&fakeStdinRunner{out: []byte(raw)})

	got, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("HasPendingReviewByViewer: %v", err)
	}
	if got {
		t.Fatalf("want false when the viewer has only SUBMITTED reviews")
	}
}

func TestHasPendingReviewByViewer_NoReviews_False(t *testing.T) {
	raw := pendingReviewJSON("me", nil)
	p := NewWithRunner(&fakeStdinRunner{out: []byte(raw)})

	got, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("HasPendingReviewByViewer: %v", err)
	}
	if got {
		t.Fatalf("want false when there are no reviews")
	}
}

func TestHasPendingReviewByViewer_GraphQLError_Propagates(t *testing.T) {
	raw := `{"errors":[{"type":"NOT_FOUND","message":"boom"}]}`
	p := NewWithRunner(&fakeStdinRunner{out: []byte(raw)})

	if _, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42); err == nil {
		t.Fatalf("want error when GraphQL returns an errors envelope")
	}
}

func TestHasPendingReviewByViewer_RunnerError_Propagates(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{err: errors.New("gh boom")})

	if _, err := p.HasPendingReviewByViewer(context.Background(), "foo/bar", 42); err == nil {
		t.Fatalf("want error when the gh runner fails")
	}
}

func TestHasPendingReviewByViewer_InvalidRepo_Errors(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})

	if _, err := p.HasPendingReviewByViewer(context.Background(), "no-slash", 42); err == nil {
		t.Fatalf("want error for a repo not in owner/name form")
	}
}
