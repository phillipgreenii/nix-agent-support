package beadref

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

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

// --- connectorIssueShower: a minimal reentrant wire double for the real
// `pg-connector issue show` exec/decode path, mirroring
// internal/gather/gather_test.go and internal/sync/sync_test.go's own
// TestHelperProcess pattern (one level lighter: this package's only verb
// is "issue show", so it needs no scriptout/conformance cross-check).

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

func helperCmdFactory(behavior string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_HELPER_BEHAVIOR="+behavior)
		return cmd
	}
}

func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { execCmdFactory = orig })
}

// helperMain answers `issue show <id>` per GO_HELPER_BEHAVIOR: "ok" writes
// a well-formed wire-envelope success with a fixed title/metadata, "not-
// found" exits 4, and "error" exits 1 with a wire-envelope error.
func helperMain() {
	args := findChildArgs()
	if len(args) < 2 || args[0] != "issue" || args[1] != "show" {
		os.Stderr.WriteString("unexpected pg-connector invocation: " + strings.Join(args, " "))
		os.Exit(99)
	}
	switch os.Getenv("GO_HELPER_BEHAVIOR") {
	case "ok":
		os.Stdout.WriteString(`{"protocolVersion":1,"schemaVersion":5,"result":{"id":"bead-1","title":"myorg/repo#42: fix the bug","metadata":{"repo":"myorg/repo","pr_number":"42"}}}`)
		os.Exit(0)
	case "not-found":
		os.Exit(4)
	case "error":
		os.Stdout.WriteString(`{"protocolVersion":1,"error":{"code":"internal","message":"boom"}}`)
		os.Exit(1)
	default:
		os.Stderr.WriteString("unknown GO_HELPER_BEHAVIOR")
		os.Exit(99)
	}
}

func findChildArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			if i+2 <= len(os.Args) {
				return os.Args[i+2:]
			}
			return nil
		}
	}
	return nil
}

func TestConnectorIssueShower_Show_Success(t *testing.T) {
	withFactory(t, "ok")
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
