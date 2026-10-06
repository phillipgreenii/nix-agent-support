package interpret

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func TestDerivePRAttentionFacts(t *testing.T) {
	failedRun := `{"id":"1","name":"build","status":"completed","conclusion":"failure","head_sha":"h1","attempt":1,"jobs":[{"id":"j","name":"lint","status":"completed","conclusion":"failure"}]}`
	facts := func(show, ci string) string {
		s := `{"pr_show":` + show
		if ci != "" {
			s += `,"ci":{"runs":[` + ci + `]}`
		}
		return s + `}`
	}
	open := `{"state":"open","head_sha":"h1"}`

	t.Run("open non-draft failing CI", func(t *testing.T) {
		f, err := DerivePRAttentionFacts(facts(open, failedRun), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Open || f.Draft || f.CIState != "failure" || f.CIReviewState != "failure" || f.Conflict || f.UnresolvedThread {
			t.Errorf("facts = %+v", f)
		}
	})
	t.Run("review_exempt_checks softens the review state but never the display state", func(t *testing.T) {
		f, err := DerivePRAttentionFacts(facts(open, failedRun), &config.Config{ReviewExemptChecks: []string{"lint"}})
		if err != nil {
			t.Fatal(err)
		}
		if f.CIState != "failure" || f.CIReviewState != "success" {
			t.Errorf("CIState=%s CIReviewState=%s, want failure/success", f.CIState, f.CIReviewState)
		}
	})
	t.Run("check_interpreters exclusions apply to both", func(t *testing.T) {
		cfg := &config.Config{CheckInterpreters: []config.CheckInterpreterConfig{{Type: "x", Patterns: []string{"^build$"}}}}
		f, err := DerivePRAttentionFacts(facts(open, failedRun), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if f.CIState != "none" {
			t.Errorf("CIState = %s, want none after the exclusion", f.CIState)
		}
	})
	t.Run("conflict, thread and draft", func(t *testing.T) {
		show := `{"state":"open","draft":true,"merge_state_status":"DIRTY","comments":[{"id":"c","thread_id":"t","resolved":false}]}`
		f, err := DerivePRAttentionFacts(facts(show, ""), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Draft || !f.Conflict || !f.UnresolvedThread || f.CIState != "none" {
			t.Errorf("facts = %+v", f)
		}
	})
	t.Run("a resolved thread is not unresolved", func(t *testing.T) {
		f, err := DerivePRAttentionFacts(facts(`{"state":"open","comments":[{"id":"c","thread_id":"t","resolved":true}]}`, ""), nil)
		if err != nil || f.UnresolvedThread {
			t.Errorf("facts = %+v err=%v", f, err)
		}
	})
	t.Run("closed", func(t *testing.T) {
		f, err := DerivePRAttentionFacts(facts(`{"state":"closed"}`, ""), nil)
		if err != nil || f.Open {
			t.Errorf("facts = %+v err=%v", f, err)
		}
	})
	t.Run("missing or malformed facts are an error", func(t *testing.T) {
		for _, bad := range []string{`{}`, `not json`, `{"pr_show":"x"}`} {
			if _, err := DerivePRAttentionFacts(bad, nil); err == nil || !strings.Contains(err.Error(), "interpret:") {
				t.Errorf("%q: err = %v, want an error", bad, err)
			}
		}
	})
}
