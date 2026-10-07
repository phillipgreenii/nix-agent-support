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
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// budgetResp is `gh api graphql`'s answer to the resolver's rate-limit probe.
func budgetResp(remaining int) []byte {
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"remaining":%d}}}`, remaining))
}

// newBudgetedFakeGH is a fakeGH whose rate-limit probe reports a healthy
// budget (well above the default reserve), so a test about the `pr view` read
// itself is not stopped by the guard in front of it.
func newBudgetedFakeGH() *fakeGH {
	gh := newFakeGH()
	gh.responses["api graphql"] = budgetResp(4000)
	return gh
}

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
	gh := newBudgetedFakeGH()
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
	r := newGHPRResolver(newBudgetedFakeGH())
	_, _, err := r.Resolve(context.Background(), "not-a-valid-id")
	if err == nil {
		t.Fatal("expected error for malformed pr id")
	}
	// A malformed id is the CALLER's mistake, not this backend being
	// unhealthy (INV-ERR-2; bug pg2-r9iok) — previously this fell
	// through unwrapped to codeForError's "unavailable" fallback.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestGHPRResolver_NonexistentPR_NotFound proves the GraphQL "could not
// resolve" phrasing gh returns for a nonexistent PR number is classified
// as not_found through classifyGHError, not left to fall through to
// "unavailable" (INV-ERR-2; bug pg2-r9iok).
func TestGHPRResolver_NonexistentPR_NotFound(t *testing.T) {
	gh := newBudgetedFakeGH()
	gh.errs["pr view"] = errors.New("gh pr view 999999999: exit status 1: GraphQL: Could not resolve to a PullRequest with the number of 999999999. (repository.pullRequest)")
	r := newGHPRResolver(gh)
	_, _, err := r.Resolve(context.Background(), "foo/bar#999999999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestGHPRResolver_PropagatesGHError(t *testing.T) {
	gh := newBudgetedFakeGH()
	gh.errs["pr view"] = errors.New("boom")
	r := newGHPRResolver(gh)
	if _, _, err := r.Resolve(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("expected propagated gh error")
	}
}

func TestGHPRResolver_ClassifiesAuthError(t *testing.T) {
	gh := newBudgetedFakeGH()
	gh.errs["pr view"] = github.ErrGHAuthInvalid
	r := newGHPRResolver(gh)
	_, _, err := r.Resolve(context.Background(), "foo/bar#42")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestGHPRResolver_EmptyHeadRefName(t *testing.T) {
	gh := newBudgetedFakeGH()
	gh.responses["pr view"] = []byte(`{"headRefName":""}`)
	r := newGHPRResolver(gh)
	if _, _, err := r.Resolve(context.Background(), "foo/bar#42"); err == nil {
		t.Fatal("expected error for empty head branch")
	}
}

func TestGHPRResolver_MalformedJSON(t *testing.T) {
	gh := newBudgetedFakeGH()
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

// TestGHPRResolver_BelowReserve_AnswersUnavailableWithoutReading is the
// guard (bead pg2-ir8bs): with the GraphQL budget under the reserve the
// resolver answers unavailable and never issues the `gh pr view` GraphQL read.
func TestGHPRResolver_BelowReserve_AnswersUnavailableWithoutReading(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = budgetResp(400)
	gh.responses["pr view"] = []byte(`{"headRefName":"feat/x"}`)
	r := newGHPRResolver(gh)

	_, _, err := r.Resolve(context.Background(), "foo/bar#42")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "1000") {
		t.Errorf("err = %v, want the remainder (400) and the default reserve (1000) named", err)
	}
	for _, call := range gh.calls {
		if len(call) >= 2 && call[0] == "pr" && call[1] == "view" {
			t.Fatalf("gh pr view was issued below the reserve: %v", gh.calls)
		}
	}
}

// The reserve is the shared config.rate_reserve_points key: lowering it lets
// the same budget through, raising it stops a budget the default would allow.
func TestGHPRResolver_ReserveComesFromConfig(t *testing.T) {
	cases := []struct {
		name      string
		remaining int
		config    string
		wantErr   bool
	}{
		{"default reserve, exactly at it", 1000, "", false},
		{"default reserve, one under it", 999, "", true},
		{"config lowers the reserve", 400, `{"rate_reserve_points":100}`, false},
		{"config raises the reserve", 2500, `{"rate_reserve_points":3000}`, true},
		{"malformed config falls back to the default", 500, `{"rate_reserve_points":"lots"}`, true},
		{"config without the key falls back to the default", 4000, `{"queries":{}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := newFakeGH()
			gh.responses["api graphql"] = budgetResp(c.remaining)
			gh.responses["pr view"] = []byte(`{"headRefName":"feat/x"}`)
			ctx := context.Background()
			if c.config != "" {
				ctx = scriptout.WithConfig(ctx, []byte(c.config))
			}
			_, _, err := newGHPRResolver(gh).Resolve(ctx, "foo/bar#42")
			if c.wantErr {
				if !errors.Is(err, scriptout.ErrUnavailable) {
					t.Fatalf("err = %v, want ErrUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want success", err)
			}
		})
	}
}

// Above the reserve the read runs after exactly one probe, probe first.
func TestGHPRResolver_AboveReserve_ProbesOnceThenReads(t *testing.T) {
	gh := newBudgetedFakeGH()
	gh.responses["pr view"] = []byte(`{"headRefName":"feat/x"}`)
	if _, _, err := newGHPRResolver(gh).Resolve(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(gh.calls) != 2 || gh.calls[0][0] != "api" || gh.calls[1][0] != "pr" {
		t.Fatalf("gh calls = %v, want [api graphql probe, pr view]", gh.calls)
	}
	if !strings.Contains(strings.Join(gh.calls[0], " "), "rateLimit") {
		t.Errorf("probe call %v does not query rateLimit", gh.calls[0])
	}
}

// A malformed id is rejected before any probe is spent.
func TestGHPRResolver_InvalidIDTakesNoProbe(t *testing.T) {
	gh := newBudgetedFakeGH()
	_, _, _ = newGHPRResolver(gh).Resolve(context.Background(), "not-a-valid-id")
	if len(gh.calls) != 0 {
		t.Errorf("gh calls = %v, want none for an invalid id", gh.calls)
	}
}

// A probe that fails (or cannot be parsed) stops the read: with no reading the
// guard cannot say the budget is safe. An auth failure keeps its class.
func TestGHPRResolver_ProbeFailureStopsTheRead(t *testing.T) {
	t.Run("auth failure", func(t *testing.T) {
		gh := newFakeGH()
		gh.errs["api graphql"] = github.ErrGHAuthInvalid
		_, _, err := newGHPRResolver(gh).Resolve(context.Background(), "foo/bar#42")
		if !errors.Is(err, scriptout.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if len(gh.calls) != 1 {
			t.Errorf("gh calls = %v, want only the probe", gh.calls)
		}
	})
	t.Run("unparseable probe", func(t *testing.T) {
		gh := newFakeGH()
		gh.responses["api graphql"] = []byte(`not json`)
		if _, _, err := newGHPRResolver(gh).Resolve(context.Background(), "foo/bar#42"); err == nil {
			t.Fatal("expected an error for an unparseable rate-limit reading")
		}
		if len(gh.calls) != 1 {
			t.Errorf("gh calls = %v, want only the probe", gh.calls)
		}
	})
}

// Through ListRuns: below the reserve the whole op answers unavailable and no
// `gh run list` follows the refused resolve.
func TestListRuns_BelowReserveAnswersUnavailableWithoutAnyRead(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = budgetResp(10)
	b := NewWithDeps(gh, newGHPRResolver(gh))
	_, err := b.ListRuns(context.Background(), "foo/bar#42")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if len(gh.calls) != 1 {
		t.Errorf("gh calls = %v, want only the rate-limit probe", gh.calls)
	}
}
