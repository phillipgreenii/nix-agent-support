package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// sigDoc is one synthetic gather of the same pull request, kept as plain maps
// so a test can vary exactly one field. build() renders it as gather.Facts.
type sigDoc struct {
	show      map[string]any
	ci        map[string]any
	workBeads map[string]any
	jira      map[string]any
	headSHA   string
	asOf      string
}

func sigBead(id, title string, meta map[string]string, labels []string, updatedAt string) map[string]any {
	return map[string]any{
		"id": id, "title": title, "state": "open", "issue_type": "task",
		"labels": labels, "updated_at": updatedAt, "as_of": "2026-10-07T10:00:00Z",
		"metadata": meta,
	}
}

// newSigDoc is a synthetic open PR acme/widgets#7 with one comment, one review,
// one CI run, its own two beads and two other PRs' beads.
func newSigDoc() sigDoc {
	return sigDoc{
		headSHA: "head1",
		asOf:    "2026-10-07T10:00:00Z",
		show: map[string]any{
			"id": "acme/widgets#7", "repo": "acme/widgets", "number": 7, "state": "open",
			"title": "t", "head_sha": "head1", "as_of": "2026-10-07T10:00:00Z",
			"served_from": "origin", "age_seconds": 0,
			"mergeable": "MERGEABLE", "merge_state_status": "CLEAN",
			"updated_at": "2026-10-01T00:00:00Z", "comment_count": 1, "review_count": 1,
			"comments": []any{map[string]any{"id": "c1", "author": "a", "body": "hi"}},
			"reviews":  []any{map[string]any{"id": "r1", "state": "APPROVED", "author": "b"}},
		},
		ci: map[string]any{
			"runs":    []any{map[string]any{"id": "run1", "conclusion": "success", "status": "completed", "as_of": "2026-10-07T10:00:00Z"}},
			"sources": []any{map[string]any{"source": "ci-a", "status": "succeeded", "count": 1}},
		},
		workBeads: map[string]any{
			"entities": []any{
				sigBead("b-own-anchor", "acme/widgets#7: t", map[string]string{"repo": "acme/widgets", "pr_number": "7", "last_checked_at": "2026-10-07T03:00:00Z"}, []string{"x"}, "2026-10-07T03:00:00Z"),
				sigBead("b-own-fb", "process-feedback: acme/widgets#7", map[string]string{"repo": "acme/widgets", "pr_number": "7"}, []string{"fbsum:aaa"}, "2026-10-07T04:00:00Z"),
				sigBead("b-other-anchor", "acme/widgets#8: u", map[string]string{"repo": "acme/widgets", "pr_number": "8"}, []string{"y"}, "2026-10-07T05:00:00Z"),
				sigBead("b-other-fb", "process-feedback: acme/widgets#9", map[string]string{"repo": "acme/widgets", "pr_number": "9"}, []string{"fbsum:bbb"}, "2026-10-07T06:00:00Z"),
			},
			"present_ids": []any{"b-own-anchor", "b-own-fb", "b-other-anchor", "b-other-fb"},
			"sources": []any{
				map[string]any{"source": "issue-beads", "status": "succeeded", "count": 4},
				map[string]any{"source": "issue-jira", "status": "disabled", "count": 0, "reason": "not applicable"},
			},
		},
		jira: map[string]any{
			"ABC-1": map[string]any{"id": "ABC-1", "state": "open", "served_from": "origin", "age_seconds": 0, "as_of": "2026-10-07T10:00:00Z"},
		},
	}
}

func (d sigDoc) build(t *testing.T) gather.Facts {
	t.Helper()
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f := gather.Facts{
		PRShow: raw(d.show), CI: raw(d.ci), WorkBeads: raw(d.workBeads),
		HeadSHA: d.headSHA, AsOf: d.asOf,
	}
	jira := map[string]json.RawMessage{}
	for k, v := range d.jira {
		jira[k] = raw(v)
	}
	f.JiraIssues = jira
	return f
}

// ownBead returns the i-th entity of workBeads for in-place edits.
func ownBead(d sigDoc, i int) map[string]any {
	return d.workBeads["entities"].([]any)[i].(map[string]any)
}

func TestPRWorkBeadsView(t *testing.T) {
	d := newSigDoc()
	raw, err := json.Marshal(d.workBeads)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := gather.PRWorkBeadsView(raw, "acme/widgets", 7)
	if !ok {
		t.Fatal("not ok")
	}
	var got struct {
		Entities   []map[string]any `json:"entities"`
		Sources    []map[string]any `json:"sources"`
		PresentIDs []string         `json:"present_ids"`
	}
	if err := json.Unmarshal(view, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entities) != 2 || got.Entities[0]["id"] != "b-own-anchor" || got.Entities[1]["id"] != "b-own-fb" {
		t.Errorf("entities = %v, want the PR's own two beads sorted by id", got.Entities)
	}
	if got.PresentIDs != nil {
		t.Errorf("present_ids kept: %v", got.PresentIDs)
	}
	if len(got.Sources) != 2 || got.Sources[1]["status"] != "disabled" || got.Sources[0]["count"] != nil || got.Sources[1]["reason"] != nil {
		t.Errorf("sources = %v, want name and status only", got.Sources)
	}
	if _, ok := gather.PRWorkBeadsView(json.RawMessage(`[1]`), "acme/widgets", 7); ok {
		t.Error("an undecodable value must report not ok")
	}
}
