// resolver_test.go exercises the PRODUCTION resolver path (ghPRResolver),
// closing the gap this packet's original test suite left: every existing
// ListRuns/RerunFailed test (provider_test.go) injects fakePR and never
// runs the real resolver logic. These tests use the same fakeGH exec-seam
// double provider_test.go already uses (a *gh* double, not a *pg-connector*
// double) — because the fix under test replaces "shell out to pg-connector"
// with "call gh directly," fakeGH is what the real path now actually needs.
package internal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestParsePRID_Valid(t *testing.T) {
	repo, number, err := parsePRID("foo/bar#42")
	if err != nil {
		t.Fatalf("parsePRID: %v", err)
	}
	if repo != "foo/bar" || number != 42 {
		t.Fatalf("parsePRID(\"foo/bar#42\") = (%q, %d), want (\"foo/bar\", 42)", repo, number)
	}
}

func TestParsePRID_MissingHash(t *testing.T) {
	if _, _, err := parsePRID("foo/bar"); err == nil {
		t.Fatal("expected error for id with no '#'")
	}
}

func TestParsePRID_RepoNotOwnerSlashName(t *testing.T) {
	if _, _, err := parsePRID("bar#42"); err == nil {
		t.Fatal("expected error for repo part with no '/'")
	}
}

func TestParsePRID_NonPositiveNumber(t *testing.T) {
	for _, id := range []string{"foo/bar#0", "foo/bar#-1", "foo/bar#abc", "foo/bar#"} {
		if _, _, err := parsePRID(id); err == nil {
			t.Errorf("parsePRID(%q): expected error, got none", id)
		}
	}
}

// TestGHPRResolver_ResolvesRepoFromIDAndBranchFromGH is the real
// (non-fakePR) resolver path this bead's acceptance criteria requires:
// repo comes from parsing the id, branch comes from gh, and no
// pg-connector/other-backend subprocess is ever invoked (fakeGH is the
// ONLY double in play).
func TestGHPRResolver_ResolvesRepoFromIDAndBranchFromGH(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{"headRefName":"feat/x"}`)
	r := newGHPRResolver(gh)

	repo, branch, err := r.Resolve(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if repo != "foo/bar" {
		t.Errorf("repo = %q, want %q", repo, "foo/bar")
	}
	if branch != "feat/x" {
		t.Errorf("branch = %q, want %q", branch, "feat/x")
	}

	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	for _, want := range []string{"pr view 42", "--repo foo/bar", "--json headRefName"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args %v missing %q", last, want)
		}
	}
}

func TestGHPRResolver_InvalidID(t *testing.T) {
	r := newGHPRResolver(newFakeGH())
	_, _, err := r.Resolve(context.Background(), "not-a-valid-id")
	if err == nil {
		t.Fatal("expected error for malformed pr id")
	}
	// A malformed id is the CALLER's mistake, not this backend being
	// unhealthy [design: §4.2, bug pg2-r9iok] — previously this fell
	// through unwrapped to codeForError's "unavailable" fallback.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestGHPRResolver_NonexistentPR_NotFound proves the GraphQL "could not
// resolve" phrasing gh returns for a nonexistent PR number is classified
// as not_found through classifyGHError, not left to fall through to
// "unavailable" [design: §4.5, bug pg2-r9iok].
func TestGHPRResolver_NonexistentPR_NotFound(t *testing.T) {
	gh := newFakeGH()
	gh.errs["pr view"] = errors.New("gh pr view 999999999: exit status 1: GraphQL: Could not resolve to a PullRequest with the number of 999999999. (repository.pullRequest)")
	r := newGHPRResolver(gh)
	_, _, err := r.Resolve(context.Background(), "foo/bar#999999999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestGHPRResolver_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["pr view"] = errors.New("boom")
	r := newGHPRResolver(gh)
	if _, _, err := r.Resolve(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("expected propagated gh error")
	}
}

func TestGHPRResolver_ClassifiesAuthError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["pr view"] = github.ErrGHAuthInvalid
	r := newGHPRResolver(gh)
	_, _, err := r.Resolve(context.Background(), "foo/bar#42")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestGHPRResolver_EmptyHeadRefName(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{"headRefName":""}`)
	r := newGHPRResolver(gh)
	if _, _, err := r.Resolve(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("expected error for empty head branch")
	}
}

func TestGHPRResolver_MalformedJSON(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`not json`)
	r := newGHPRResolver(gh)
	if _, _, err := r.Resolve(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("expected error for malformed gh JSON")
	}
}

// TestNew_WiresGHPRResolverSharingOneGHGateway proves New()'s production
// wiring shares one gh gateway between run-list/logs/rerun and the
// PRResolver, and that constructing it never invokes any subprocess
// (pg-connector or otherwise) — the fix's whole point.
func TestNew_WiresGHPRResolverSharingOneGHGateway(t *testing.T) {
	b := New()
	if b.gh == nil {
		t.Fatal("Backend.gh is nil")
	}
	resolver, ok := b.pr.(*ghPRResolver)
	if !ok {
		t.Fatalf("Backend.pr = %T, want *ghPRResolver", b.pr)
	}
	if resolver.gh != b.gh {
		t.Fatal("ghPRResolver does not share Backend's own gh gateway")
	}
}
