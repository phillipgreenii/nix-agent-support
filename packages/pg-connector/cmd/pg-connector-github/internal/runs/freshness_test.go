package runs

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func content(rs ...schema.CIRun) Content { return Content{Runs: rs} }

func r(sha, status, conclusion string) schema.CIRun {
	return schema.CIRun{ID: "1", HeadSHA: sha, Status: status, Conclusion: conclusion}
}

func TestFreshness_ThreeCases(t *testing.T) {
	s := config.Defaults().Settings
	maxAt := t0.Add(30 * time.Minute)
	cases := []struct {
		name    string
		c       Content
		want    time.Time
		active  bool
		failing bool
	}{
		{"queued on the current head", content(r(head, "queued", "")), t0.Add(60 * time.Second), true, false},
		{"in progress on the current head", content(r(head, "in_progress", "")), t0.Add(60 * time.Second), true, false},
		{"waiting", content(r(head, "waiting", "")), t0.Add(60 * time.Second), true, false},
		{"requested", content(r(head, "requested", "")), t0.Add(60 * time.Second), true, false},
		{"pending", content(r(head, "pending", "")), t0.Add(60 * time.Second), true, false},
		{"active beats failing", content(r(head, "completed", "failure"), r(head, "in_progress", "")), t0.Add(60 * time.Second), true, true},
		{"failing", content(r(head, "completed", "failure")), t0.Add(5 * time.Minute), false, true},
		{"timed out", content(r(head, "completed", "timed_out")), t0.Add(5 * time.Minute), false, true},
		{"cancelled", content(r(head, "completed", "cancelled")), t0.Add(5 * time.Minute), false, true},
		{"passing", content(r(head, "completed", "success")), maxAt, false, false},
		{"neutral and skipped pass", content(r(head, "completed", "neutral"), r(head, "completed", "skipped")), maxAt, false, false},
		{"no runs", content(), maxAt, false, false},
		{"older head in progress is not active", content(r("head-old", "in_progress", "")), maxAt, false, false},
		{"older head failing is not failing", content(r("head-old", "completed", "failure"), r(head, "completed", "success")), maxAt, false, false},
	}
	for _, tc := range cases {
		fresh, maxAge := Freshness(tc.c, head, t0, s)
		if !fresh.Equal(tc.want) || !maxAge.Equal(maxAt) {
			t.Errorf("%s: fresh_until %v max_age_at %v, want %v and %v", tc.name, fresh, maxAge, tc.want, maxAt)
		}
		if Active(tc.c, head) != tc.active || Failing(tc.c, head) != tc.failing {
			t.Errorf("%s: Active %v Failing %v, want %v and %v", tc.name, Active(tc.c, head), Failing(tc.c, head), tc.active, tc.failing)
		}
	}
	if Active(content(r("", "queued", "")), "") || Failing(content(r("", "completed", "failure")), "") {
		t.Error("an empty current head matched a run with an empty head")
	}
}

func TestFreshness_ClampedAtMaxAge(t *testing.T) {
	s := config.Defaults().Settings
	s.CIMaxAge = config.Duration(40 * time.Second)
	for name, c := range map[string]Content{
		"active":  content(r(head, "queued", "")),
		"failing": content(r(head, "completed", "failure")),
		"idle":    content(),
	} {
		fresh, maxAge := Freshness(c, head, t0, s)
		if !fresh.Equal(t0.Add(40*time.Second)) || !maxAge.Equal(t0.Add(40*time.Second)) {
			t.Errorf("%s: fresh_until %v max_age_at %v, want both t0+40s", name, fresh, maxAge)
		}
	}
}
