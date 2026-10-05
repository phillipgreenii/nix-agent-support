// escalation_test.go: the "list" verb's --exclude-escalated-query filter
// (bead pg2-vhs3e). Fixtures are synthetic.
package main

import (
	"sort"
	"strings"
	"testing"
)

func listedIDs(t *testing.T, stdout string) []string {
	t.Helper()
	var ids []string
	for _, it := range mustUnmarshalItems(t, stdout) {
		ids = append(ids, it.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestList_ExcludeEscalated_DropsCoveredPRsOnly(t *testing.T) {
	withFactory(t, "list_review_escalations")
	stdout, stderr, code := runCLI(t, "list", "issue", "review-ready", "--backend", "b",
		"--title-prefix", "review-pr: ", "--exclude-escalated-query", "pending-review-escalations")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty on success", stderr)
	}
	// r1 per-PR same head      -> suppressed
	// r2 per-PR, head advanced -> resumes (listed)
	// r3 covered by a roll-up  -> suppressed even though its head differs
	// r4 no escalation         -> listed
	// r5 escalation w/o a head -> suppressed
	// r6 same number, other repo's escalation -> listed
	// r7 numeric pr_number, same head -> suppressed
	// r8 no repo/pr_number metadata -> listed (cannot be matched)
	want := []string{"r2", "r4", "r6", "r8"}
	if got := listedIDs(t, stdout); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("listed = %v, want %v", got, want)
	}
}

func TestList_ExcludeEscalated_AbsentFlagListsEverything(t *testing.T) {
	withFactory(t, "list_review_escalations")
	stdout, stderr, code := runCLI(t, "list", "issue", "review-ready", "--backend", "b")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	if got := listedIDs(t, stdout); len(got) != 8 {
		t.Fatalf("listed = %v, want all 8 review items when the flag is absent", got)
	}
}

func TestList_ExcludeEscalated_QueryFailureFailsOpenWithWarning(t *testing.T) {
	for _, behavior := range []string{"list_review_escalations_query_fails", "list_review_escalations_bad_json"} {
		t.Run(behavior, func(t *testing.T) {
			withFactory(t, behavior)
			stdout, stderr, code := runCLI(t, "list", "issue", "review-ready", "--backend", "b",
				"--exclude-escalated-query", "pending-review-escalations")

			if code != 0 {
				t.Fatalf("exit code = %d, want 0: a failing escalation query must not stop review dispatch (stderr=%q)", code, stderr)
			}
			if got := listedIDs(t, stdout); len(got) != 8 {
				t.Fatalf("listed = %v, want every review item (fail open)", got)
			}
			if !strings.Contains(stderr, "warning: escalation query") {
				t.Fatalf("stderr = %q, want a warning naming the escalation query", stderr)
			}
		})
	}
}

func TestEscalationIndex_Suppresses(t *testing.T) {
	idx, err := buildEscalationIndex([]byte(`{"entities":[` +
		`{"id":"e1","metadata":{"review_escalation_key":"pr:acme/widgets#1","review_escalation_head":"h1"}},` +
		`{"id":"e2","metadata":{"review_escalation_key":"rollup:x","review_escalation_prs":"acme/widgets#2; acme/widgets#3"}}` +
		`]}`))
	if err != nil {
		t.Fatalf("buildEscalationIndex: %v", err)
	}
	md := func(repo, pr, head string) map[string]any {
		m := map[string]any{}
		if repo != "" {
			m["repo"] = repo
		}
		if pr != "" {
			m["pr_number"] = pr
		}
		if head != "" {
			m["head_sha"] = head
		}
		return m
	}
	for _, tc := range []struct {
		name string
		md   map[string]any
		want bool
	}{
		{"same head", md("acme/widgets", "1", "h1"), true},
		{"advanced head", md("acme/widgets", "1", "h2"), false},
		{"item without a head cannot be told apart", md("acme/widgets", "1", ""), true},
		{"roll-up member, any head", md("acme/widgets", "3", "whatever"), true},
		{"roll-up member list is whitespace-trimmed", md("acme/widgets", "2", "h"), true},
		{"unrelated PR", md("acme/widgets", "9", "h1"), false},
		{"no repo", md("", "1", "h1"), false},
		{"no pr number", md("acme/widgets", "", "h1"), false},
	} {
		if got := idx.suppresses(tc.md); got != tc.want {
			t.Errorf("%s: suppresses = %v, want %v", tc.name, got, tc.want)
		}
	}
}
