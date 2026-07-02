package enrich

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestBucketSize(t *testing.T) {
	cases := []struct {
		total int
		want  string
	}{
		{0, "XS"}, {9, "XS"}, {10, "S"}, {29, "S"}, {30, "M"},
		{99, "M"}, {100, "L"}, {499, "L"}, {500, "XL"}, {5000, "XL"},
	}
	for _, c := range cases {
		if got := bucketSize(c.total); got != c.want {
			t.Errorf("bucketSize(%d) = %q; want %q", c.total, got, c.want)
		}
	}
}

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		branch  string
		commits []string
		want    string
	}{
		{"title conventional fix", "fix(store): wrong scan", "anything", nil, "bugfix"},
		{"title feat with bang", "feat!: breaking change", "x", nil, "feature"},
		{"branch prefix when title plain", "tidy things up", "refactor/cleanup", nil, "refactor"},
		{"branch feature alias", "stuff", "feature/new-ui", nil, "feature"},
		{"commit majority when title+branch plain", "wip", "wip", []string{"fix: a", "fix: b", "docs: c"}, "bugfix"},
		{"fallback other", "random work", "wip", nil, "other"},
		{"title wins over branch", "docs: readme", "fix/typo", nil, "docs"},
	}
	for _, c := range cases {
		if got := classifyKind(c.title, c.branch, c.commits); got != c.want {
			t.Errorf("%s: classifyKind(%q,%q,%v) = %q; want %q", c.name, c.title, c.branch, c.commits, got, c.want)
		}
	}
}

func failingRun() api.CIRun  { return api.CIRun{Status: "completed", Conclusion: "failure"} }
func successRun2() api.CIRun { return api.CIRun{Status: "completed", Conclusion: "success"} }

func TestScoreUrgency(t *testing.T) {
	t.Run("none → low", func(t *testing.T) {
		lvl, score, reasons := scoreUrgency(Input{PR: api.PR{Title: "feat: x", Body: "normal"}, CIRuns: []api.CIRun{successRun2()}})
		if lvl != "low" || score != 0 || len(reasons) != 0 {
			t.Fatalf("got %q score=%d reasons=%v; want low/0/[]", lvl, score, reasons)
		}
	})
	t.Run("urgency label → high", func(t *testing.T) {
		lvl, score, reasons := scoreUrgency(Input{PR: api.PR{Title: "x"}, Labels: []string{"P0"}})
		if lvl != "high" || score != 3 || !reflect.DeepEqual(reasons, []string{"label:p0"}) {
			t.Fatalf("got %q score=%d reasons=%v; want high/3/[label:p0]", lvl, score, reasons)
		}
	})
	t.Run("keyword → medium", func(t *testing.T) {
		lvl, _, reasons := scoreUrgency(Input{PR: api.PR{Title: "Fix for production incident"}})
		if lvl != "medium" || !reflect.DeepEqual(reasons, []string{"keyword:production incident"}) {
			t.Fatalf("got %q reasons=%v; want medium/[keyword:production incident]", lvl, reasons)
		}
	})
	t.Run("bugfix commit alone → medium", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "wip"}, Commits: []string{"fix: a"}})
		if lvl != "medium" || score != 1 {
			t.Fatalf("got %q score=%d; want medium/1", lvl, score)
		}
	})
	t.Run("ci failing → medium", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "x"}, CIRuns: []api.CIRun{failingRun()}})
		if lvl != "medium" || score != 2 {
			t.Fatalf("got %q score=%d; want medium/2", lvl, score)
		}
	})
	t.Run("keyword + ci failing → high", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "hotfix outage"}, CIRuns: []api.CIRun{failingRun()}})
		if lvl != "high" || score < 3 {
			t.Fatalf("got %q score=%d; want high/>=3", lvl, score)
		}
	})
}

func TestCompute(t *testing.T) {
	in := Input{
		PR:      api.PR{Title: "fix(api): null deref", Body: "production incident", Additions: 40, Deletions: 5, Branch: "fix/null"},
		Files:   []string{"a.go", "b.go", "c.py"},
		Commits: []string{"fix: handle nil"},
		Labels:  []string{"p1"},
		CIRuns:  []api.CIRun{failingRun()},
	}
	got := Compute(in)
	if got.Kind != "bugfix" {
		t.Errorf("Kind = %q; want bugfix", got.Kind)
	}
	if !reflect.DeepEqual(got.Languages, []string{"Go", "Python"}) {
		t.Errorf("Languages = %v; want [Go Python]", got.Languages)
	}
	if got.Size != "M" { // 45 lines
		t.Errorf("Size = %q; want M", got.Size)
	}
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high", got.Urgency)
	}
	if got.UrgencyScore < 3 || len(got.UrgencyReasons) == 0 {
		t.Errorf("UrgencyScore=%d reasons=%v; want >=3 and non-empty", got.UrgencyScore, got.UrgencyReasons)
	}
}

// --- app-path derivation tests ---

func TestAppPathsFromFiles(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{
			name:  "root-only files → empty (no dir prefix)",
			files: []string{"README.md", "go.mod"},
			want:  nil,
		},
		{
			name:  "single app-path",
			files: []string{"svc/alpha/main.go", "svc/alpha/service.go"},
			want:  []string{"svc/alpha"},
		},
		{
			name:  "multiple distinct app-paths, deduped and sorted",
			files: []string{"svc/alpha/main.go", "example/widget/handler.go", "svc/alpha/types.go"},
			want:  []string{"example/widget", "svc/alpha"},
		},
		{
			name:  "top-level single segment path included",
			files: []string{"myapp/main.go"},
			want:  []string{"myapp"},
		},
		{
			name:  "empty files → nil",
			files: nil,
			want:  nil,
		},
		{
			name:  "depth-3 paths use immediate parent only",
			files: []string{"org/team/service/handler.go"},
			want:  []string{"org/team/service"},
		},
	}
	for _, c := range cases {
		got := appPathsFromFiles(c.files)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: appPathsFromFiles(%v) = %v; want %v", c.name, c.files, got, c.want)
		}
	}
}

// --- mock ProjectHealthChecker ---

// mockHealthChecker is a test double for ProjectHealthChecker.
type mockHealthChecker struct {
	// mainBroken reports whether main is broken for a given app-path.
	mainBroken map[string]bool
	// prBranchGreen reports whether the PR's branch is green for a given app-path.
	prBranchGreen map[string]bool
}

func (m *mockHealthChecker) ProjectHealth(ctx context.Context, appPath, prBranch string) (ProjectHealthResult, error) {
	return ProjectHealthResult{
		MainBroken:    m.mainBroken[appPath],
		PRBranchGreen: m.prBranchGreen[appPath],
	}, nil
}

// --- ProjectHealthChecker interface & urgency signal tests ---

func TestScoreUrgencyWithProjectHealth(t *testing.T) {
	ctx := context.Background()

	t.Run("main broken and PR branch green → HIGHEST urgency (PR-is-the-fix)", func(t *testing.T) {
		checker := &mockHealthChecker{
			mainBroken:    map[string]bool{"svc/beta": true},
			prBranchGreen: map[string]bool{"svc/beta": true},
		}
		in := Input{
			PR:                api.PR{Title: "fix payment timeout", Branch: "fix/payment-timeout"},
			Files:             []string{"svc/beta/handler.go"},
			ProjectHealthFunc: checker.ProjectHealth,
		}
		lvl, score, reasons := scoreUrgencyWithHealth(ctx, in)
		if lvl != "high" {
			t.Fatalf("want high urgency; got %q", lvl)
		}
		if score < 4 {
			t.Fatalf("want score>=4 for PR-is-fix signal; got %d", score)
		}
		found := false
		for _, r := range reasons {
			if r == "project-broken-main:svc/beta" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("want reason project-broken-main:svc/beta; got %v", reasons)
		}
	})

	t.Run("main broken but PR branch also broken → no PR-is-fix signal", func(t *testing.T) {
		checker := &mockHealthChecker{
			mainBroken:    map[string]bool{"svc/beta": true},
			prBranchGreen: map[string]bool{"svc/beta": false},
		}
		in := Input{
			PR:                api.PR{Title: "work in progress", Branch: "feat/new-thing"},
			Files:             []string{"svc/beta/handler.go"},
			ProjectHealthFunc: checker.ProjectHealth,
		}
		lvl, _, reasons := scoreUrgencyWithHealth(ctx, in)
		// no PR-is-fix reason; urgency could still be "low"
		for _, r := range reasons {
			if r == "project-broken-main:svc/beta" {
				t.Fatalf("should not have PR-is-fix reason when PR branch is also broken; reasons=%v", reasons)
			}
		}
		_ = lvl // could be any level depending on other signals
	})

	t.Run("main healthy → no project-health signal", func(t *testing.T) {
		checker := &mockHealthChecker{
			mainBroken:    map[string]bool{"svc/beta": false},
			prBranchGreen: map[string]bool{"svc/beta": true},
		}
		in := Input{
			PR:                api.PR{Title: "refactor", Branch: "refactor/cleanup"},
			Files:             []string{"svc/beta/handler.go"},
			ProjectHealthFunc: checker.ProjectHealth,
		}
		_, _, reasons := scoreUrgencyWithHealth(ctx, in)
		for _, r := range reasons {
			if strings.HasPrefix(r, "project-broken-main:") {
				t.Fatalf("should not have project-broken-main reason when main is healthy; reasons=%v", reasons)
			}
		}
	})

	t.Run("no checker → no health signal, no error", func(t *testing.T) {
		in := Input{
			PR:    api.PR{Title: "refactor", Branch: "refactor/cleanup"},
			Files: []string{"svc/beta/handler.go"},
			// ProjectHealthFunc is nil
		}
		// scoreUrgencyWithHealth should work fine with nil checker
		lvl, score, reasons := scoreUrgencyWithHealth(ctx, in)
		_ = lvl
		_ = score
		for _, r := range reasons {
			if strings.HasPrefix(r, "project-broken-main:") {
				t.Fatalf("should not have project-broken-main reason with nil checker; reasons=%v", reasons)
			}
		}
	})

	t.Run("multiple app-paths: one broken on main, PR branch green for it → signal fires once per broken path", func(t *testing.T) {
		checker := &mockHealthChecker{
			mainBroken:    map[string]bool{"svc/beta": true, "platform/auth": false},
			prBranchGreen: map[string]bool{"svc/beta": true, "platform/auth": true},
		}
		in := Input{
			PR:                api.PR{Title: "fix payment", Branch: "fix/payment"},
			Files:             []string{"svc/beta/x.go", "platform/auth/y.go"},
			ProjectHealthFunc: checker.ProjectHealth,
		}
		_, _, reasons := scoreUrgencyWithHealth(ctx, in)
		countSignal := 0
		for _, r := range reasons {
			if strings.HasPrefix(r, "project-broken-main:") {
				countSignal++
			}
		}
		if countSignal != 1 {
			t.Fatalf("want exactly 1 project-broken-main reason (only svc/beta is broken); got %d reasons=%v", countSignal, reasons)
		}
	})
}

// TestComputeWithProjectHealth verifies that Compute passes through the
// ProjectHealthFunc and the urgency score reflects the PR-is-fix signal.
func TestComputeWithProjectHealth(t *testing.T) {
	ctx := context.Background()
	checker := &mockHealthChecker{
		mainBroken:    map[string]bool{"svc/beta": true},
		prBranchGreen: map[string]bool{"svc/beta": true},
	}
	in := Input{
		PR:                api.PR{Title: "plain fix", Branch: "fix/payments", Additions: 20, Deletions: 5},
		Files:             []string{"svc/beta/service.go"},
		ProjectHealthFunc: checker.ProjectHealth,
	}
	got := ComputeWithContext(ctx, in)
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high", got.Urgency)
	}
	found := false
	for _, r := range got.UrgencyReasons {
		if r == "project-broken-main:svc/beta" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("UrgencyReasons = %v; want to include project-broken-main:svc/beta", got.UrgencyReasons)
	}
}
