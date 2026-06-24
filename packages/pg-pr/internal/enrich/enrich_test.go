package enrich

import (
	"reflect"
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
