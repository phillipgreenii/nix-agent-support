package beadref

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/cmdrun"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// fakeShower is this suite's wire double for issueShower, so
// TestResolvePR can exercise ClassifyBead's dispatch against fixture bead
// title/metadata without a real pg-connector subprocess on $PATH —
// mirrors internal/pipeline/pipeline_test.go's own gatherFunc/syncerFunc
// pattern.
type fakeShower struct {
	title    string
	metadata map[string]string
	err      error
	calls    int
}

func (f *fakeShower) Show(ctx context.Context, id string) (string, map[string]string, error) {
	f.calls++
	return f.title, f.metadata, f.err
}

// TestResolvePR_ThreeBeadKindsAndTitleAdoptedFallback proves the
// acceptance criteria's four resolvable shapes: an anchor bead (metadata),
// a feedback-cycle bead, a review-request bead, and a title-adopted
// (no-metadata) merge-request bead resolving via its title prefix.
func TestResolvePR_ThreeBeadKindsAndTitleAdoptedFallback(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		metadata   map[string]string
		wantRepo   string
		wantEntity string
	}{
		{
			"anchor by metadata",
			"myorg/repo#42: fix the bug",
			map[string]string{"repo": "myorg/repo", "pr_number": "42"},
			"myorg/repo", "myorg/repo#42",
		},
		{
			"feedback cycle",
			"process-feedback: myorg/repo#7",
			map[string]string{"repo": "myorg/repo", "pr_number": "7"},
			"myorg/repo", "myorg/repo#7",
		},
		{
			"review request",
			"review-pr: myorg/repo#9",
			map[string]string{"repo": "myorg/repo", "pr_number": "9"},
			"myorg/repo", "myorg/repo#9",
		},
		{
			"title-adopted merge-request bead (no metadata)",
			"myorg/repo#11: some pr title",
			nil,
			"myorg/repo", "myorg/repo#11",
		},
		{
			"pre-existing feedback cycle with no metadata (title fallback)",
			"process-feedback: myorg/repo#13",
			nil,
			"myorg/repo", "myorg/repo#13",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shower := &fakeShower{title: tc.title, metadata: tc.metadata}
			r := &Resolver{show: shower}
			repo, entityID, err := r.ResolvePR(context.Background(), "bead-1")
			if err != nil {
				t.Fatalf("ResolvePR: unexpected error: %v", err)
			}
			if repo != tc.wantRepo || entityID != tc.wantEntity {
				t.Fatalf("ResolvePR = (%q, %q), want (%q, %q)", repo, entityID, tc.wantRepo, tc.wantEntity)
			}
			if shower.calls != 1 {
				t.Fatalf("issueShower.Show called %d times, want 1", shower.calls)
			}
		})
	}
}

// TestResolvePR_UnresolvableBeadReturnsClearError proves the acceptance
// criterion "a bead whose metadata and title both fail to match any known
// shape returns a clear, non-panicking error" — the test itself surviving
// to completion is the non-panic half of that proof.
func TestResolvePR_UnresolvableBeadReturnsClearError(t *testing.T) {
	shower := &fakeShower{title: "some unrelated bead with no recognizable shape"}
	r := &Resolver{show: shower}
	repo, entityID, err := r.ResolvePR(context.Background(), "bead-99")
	if err == nil {
		t.Fatalf("ResolvePR: expected an error, got repo=%q entityID=%q", repo, entityID)
	}
	if !strings.Contains(err.Error(), "bead-99") || !strings.Contains(err.Error(), "matches no known bead shape") {
		t.Fatalf("ResolvePR error = %q, want it to clearly name the bead and the failure", err.Error())
	}
}

// TestResolvePR_DisagreeingMetadataIsUnresolvable proves ClassifyBead's
// own disagreeing-metadata rule surfaces as the same clear error here too
// (this package adds no separate cross-check of its own).
func TestResolvePR_DisagreeingMetadataIsUnresolvable(t *testing.T) {
	shower := &fakeShower{
		title:    "myorg/repo#42: fix the bug",
		metadata: map[string]string{"repo": "myorg/repo", "pr_number": "7"},
	}
	r := &Resolver{show: shower}
	if _, _, err := r.ResolvePR(context.Background(), "bead-1"); err == nil {
		t.Fatal("ResolvePR: expected an error for disagreeing title/metadata, got nil")
	}
}

// TestResolvePR_ShowFailurePropagates proves a failure to even read the
// bead (pg-connector unreachable, or a genuine not_found) surfaces as a
// wrapped, non-panicking error rather than being swallowed.
func TestResolvePR_ShowFailurePropagates(t *testing.T) {
	wantErr := errors.New("pg-connector [issue show bead-1]: not_found")
	shower := &fakeShower{err: wantErr}
	r := &Resolver{show: shower}
	_, _, err := r.ResolvePR(context.Background(), "bead-1")
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("ResolvePR: err = %v, want it to wrap the show failure %v", err, wantErr)
	}
}

// --- connectorIssueShower: an in-process wire double (a cmdrun.Runner) for
// the `pg-connector issue show` exec/decode path. It answers `issue show
// <id>` per behavior ("ok", "not-found", "error"), records every Call, and
// starts no process; the real exec path is covered by internal/cmdrun.

func helperRunner(behavior string, calls *[]cmdrun.Call) cmdrun.Runner {
	return func(_ context.Context, c cmdrun.Call) cmdrun.Result {
		*calls = append(*calls, c)
		args := c.Args
		if len(args) < 2 || args[0] != "issue" || args[1] != "show" {
			return cmdrun.Result{Stderr: "unexpected pg-connector invocation: " + strings.Join(args, " "), Code: 99}
		}
		switch behavior {
		case "ok":
			return cmdrun.Result{Stdout: `{"protocolVersion":1,"schemaVersion":5,"result":{"id":"bead-1","title":"myorg/repo#42: fix the bug","metadata":{"repo":"myorg/repo","pr_number":"42"}}}`}
		case "not-found":
			return cmdrun.Result{Code: 4}
		case "error":
			return cmdrun.Result{Stdout: `{"protocolVersion":1,"error":{"code":"internal","message":"boom"}}`, Code: 1}
		default:
			return cmdrun.Result{Stderr: "unknown GO_HELPER_BEHAVIOR", Code: 99}
		}
	}
}

func withFactory(t *testing.T, behavior string) *[]cmdrun.Call {
	t.Helper()
	orig := runner
	calls := &[]cmdrun.Call{}
	runner = helperRunner(behavior, calls)
	t.Cleanup(func() { runner = orig })
	return calls
}

func TestConnectorIssueShower_Show_Success(t *testing.T) {
	calls := withFactory(t, "ok")
	c := &connectorIssueShower{cfg: &config.Config{Repos: []config.RepoConfig{{Remote: "myorg/repo"}}}}
	title, metadata, err := c.Show(context.Background(), "bead-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if title != "myorg/repo#42: fix the bug" {
		t.Fatalf("title = %q, want the fixture title", title)
	}
	if metadata["repo"] != "myorg/repo" || metadata["pr_number"] != "42" {
		t.Fatalf("metadata = %v, want repo/pr_number from the fixture", metadata)
	}
	if len(*calls) != 1 || (*calls)[0].Name != "pg-connector" || strings.Join((*calls)[0].Args, " ") != "issue show bead-1" {
		t.Fatalf("calls = %+v, want one pg-connector [issue show bead-1]", *calls)
	}
	if len((*calls)[0].Env) != 0 {
		t.Fatalf("env = %v, want none when no beads_dir is configured", (*calls)[0].Env)
	}
}

// The configured repo's beads_dir and agent tracker backend reach the child
// as the PG_CONNECTOR_ISSUE_BEADS_DIR env entry and the --backend flag.
func TestConnectorIssueShower_Show_PassesBeadsDirEnvAndBackendFlag(t *testing.T) {
	calls := withFactory(t, "ok")
	c := &connectorIssueShower{cfg: &config.Config{
		AgentTrackerBackend: "beads-x",
		Repos:               []config.RepoConfig{{Remote: "myorg/repo", BeadsDir: "/w/.beads"}},
	}}
	if _, _, err := c.Show(context.Background(), "bead-1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	got := (*calls)[0]
	if strings.Join(got.Args, " ") != "issue show bead-1 --backend beads-x" {
		t.Errorf("args = %v", got.Args)
	}
	if len(got.Env) != 1 || got.Env[0] != "PG_CONNECTOR_ISSUE_BEADS_DIR=/w/.beads" {
		t.Errorf("env = %v", got.Env)
	}
}

func TestConnectorIssueShower_Show_NotFound(t *testing.T) {
	withFactory(t, "not-found")
	c := &connectorIssueShower{cfg: &config.Config{}}
	if _, _, err := c.Show(context.Background(), "bead-missing"); err == nil {
		t.Fatal("Show: expected an error for not_found, got nil")
	}
}

func TestConnectorIssueShower_Show_Error(t *testing.T) {
	withFactory(t, "error")
	c := &connectorIssueShower{cfg: &config.Config{}}
	_, _, err := c.Show(context.Background(), "bead-1")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Show: err = %v, want it to surface the wire error message", err)
	}
}
