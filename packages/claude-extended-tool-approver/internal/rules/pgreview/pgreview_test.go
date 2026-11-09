package pgreview

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestPgReviewRule(t *testing.T) {
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"pg-review agent q ccaudit error-rate-by-tool", hookio.Approve},
		{"pg-review agent q ccaudit top-signatures --tool Bash --limit 20", hookio.Approve},
		{"pg-review agent q hook-decisions denied-by-rule --run 7", hookio.Approve},
		{"pg-review agent q --run 7 hook-decisions denied-by-rule", hookio.Approve},
		{"pg-review --output json agent q ccaudit error-rate-by-tool", hookio.Approve},
		{"pg-review agent q ccaudit error-rate-by-tool \"two words\"", hookio.Approve},
		{"/nix/store/x/bin/pg-review agent q ccaudit q1", hookio.Approve},

		{"pg-review agent q ccaudit q1 'a;b'", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 $(id)", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 $X", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 '$(id)'", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 `id`", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 > out.txt", hookio.NoOpinion},
		{"pg-review agent q Bad_Provider q1", hookio.NoOpinion},
		{"pg-review agent q ccaudit", hookio.NoOpinion},
		{"pg-review agent q", hookio.NoOpinion},
		{"pg-review agent q --bogus ccaudit q1", hookio.NoOpinion},
		{"pg-review agent file findings.json", hookio.NoOpinion},
		{"pg-review agent context", hookio.NoOpinion},
		{"pg-review human rule 1 adopted", hookio.NoOpinion},
		{"pg-review job run --lens x", hookio.NoOpinion},
		{"pg-review", hookio.NoOpinion},
		{"git status", hookio.NoOpinion},
	}
	r := New()
	for _, c := range cases {
		ti, _ := json.Marshal(hookio.BashToolInput{Command: c.cmd})
		in := &hookio.HookInput{ToolName: "Bash", ToolInput: ti}
		got := hookio.Verdict(r.Evaluate(in)).Decision
		if got != c.want {
			t.Errorf("%q => %v, want %v", c.cmd, got, c.want)
		}
	}
}
