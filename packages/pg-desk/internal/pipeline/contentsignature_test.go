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

// A second gather of the same unchanged PR differs only in what moves on its
// own, and MUST give the same signature.
func TestContentSignature_UnchangedPRIsStableAcrossGathers(t *testing.T) {
	base := newSigDoc().build(t)
	cases := map[string]func(d *sigDoc){
		"identical re-read": func(d *sigDoc) {},
		"new as_of everywhere": func(d *sigDoc) {
			d.asOf = "2026-10-07T11:00:00Z"
			d.show["as_of"] = "2026-10-07T11:00:00Z"
			d.ci["runs"].([]any)[0].(map[string]any)["as_of"] = "2026-10-07T11:00:00Z"
			ownBead(*d, 0)["as_of"] = "2026-10-07T11:00:00Z"
		},
		"cache provenance of the PR read": func(d *sigDoc) {
			d.show["served_from"], d.show["age_seconds"] = "cache", 47
		},
		"cache provenance of a ticket read": func(d *sigDoc) {
			d.jira["ABC-1"].(map[string]any)["served_from"] = "cache"
			d.jira["ABC-1"].(map[string]any)["age_seconds"] = 90
		},
		"mergeable UNKNOWN flap": func(d *sigDoc) {
			d.show["mergeable"], d.show["merge_state_status"] = "UNKNOWN", "UNKNOWN"
		},
		"merge state moves with outside events": func(d *sigDoc) {
			d.show["merge_state_status"] = "BLOCKED"
		},
		"another PR's anchor bead is rewritten": func(d *sigDoc) {
			b := ownBead(*d, 2)
			b["updated_at"] = "2026-10-07T10:30:00Z"
			b["labels"] = []any{"y", "z"}
			b["metadata"].(map[string]string)["last_checked_at"] = "2026-10-07T10:30:00Z"
		},
		"another PR's feedback bead is rewritten": func(d *sigDoc) {
			b := ownBead(*d, 3)
			b["updated_at"] = "2026-10-07T10:31:00Z"
			b["labels"] = []any{"fbsum:ccc"}
		},
		"a bead for another PR appears, counts and ids move": func(d *sigDoc) {
			ents := d.workBeads["entities"].([]any)
			d.workBeads["entities"] = append(ents, sigBead("b-new", "acme/widgets#10: v", map[string]string{"repo": "acme/widgets", "pr_number": "10"}, nil, "2026-10-07T10:40:00Z"))
			d.workBeads["present_ids"] = append(d.workBeads["present_ids"].([]any), "b-new")
			d.workBeads["sources"].([]any)[0].(map[string]any)["count"] = 5
		},
		"a source's reason text changes": func(d *sigDoc) {
			d.workBeads["sources"].([]any)[1].(map[string]any)["reason"] = "different words"
		},
		"work beads listed in another order": func(d *sigDoc) {
			ents := d.workBeads["entities"].([]any)
			ents[0], ents[1] = ents[1], ents[0]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := newSigDoc()
			mutate(&d)
			if got, want := contentSignature(d.build(t)), contentSignature(base); got != want {
				t.Errorf("signature changed without a real change to the PR")
			}
		})
	}
}

// Real changes to the PR MUST still change the signature.
func TestContentSignature_RealChangesAreStillChanges(t *testing.T) {
	base := newSigDoc().build(t)
	cases := map[string]func(d *sigDoc){
		"head change": func(d *sigDoc) {
			d.headSHA = "head2"
			d.show["head_sha"] = "head2"
		},
		"new comment": func(d *sigDoc) {
			d.show["comment_count"] = 2
			d.show["comments"] = append(d.show["comments"].([]any), map[string]any{"id": "c2", "author": "a", "body": "again"})
		},
		"edited comment": func(d *sigDoc) {
			d.show["comments"].([]any)[0].(map[string]any)["body"] = "edited"
		},
		"new review": func(d *sigDoc) {
			d.show["review_count"] = 2
			d.show["reviews"] = append(d.show["reviews"].([]any), map[string]any{"id": "r2", "state": "CHANGES_REQUESTED", "author": "c"})
		},
		"CI conclusion change": func(d *sigDoc) {
			d.ci["runs"].([]any)[0].(map[string]any)["conclusion"] = "failure"
		},
		"new CI run": func(d *sigDoc) {
			d.ci["runs"] = append(d.ci["runs"].([]any), map[string]any{"id": "run2", "conclusion": "success", "status": "completed"})
		},
		"PR starts conflicting": func(d *sigDoc) {
			d.show["mergeable"], d.show["merge_state_status"] = "CONFLICTING", "DIRTY"
		},
		"PR state changes": func(d *sigDoc) {
			d.show["state"] = "merged"
		},
		"own anchor bead is rewritten": func(d *sigDoc) {
			ownBead(*d, 0)["updated_at"] = "2026-10-07T10:30:00Z"
			ownBead(*d, 0)["labels"] = []any{"x", "p1"}
		},
		"own feedback bead is rewritten": func(d *sigDoc) {
			ownBead(*d, 1)["labels"] = []any{"fbsum:zzz"}
		},
		"own bead appears": func(d *sigDoc) {
			ents := d.workBeads["entities"].([]any)
			d.workBeads["entities"] = append(ents, sigBead("b-own-review", "acme/widgets#7: review", map[string]string{"repo": "acme/widgets", "pr_number": "7"}, nil, "2026-10-07T10:40:00Z"))
		},
		"a work-beads source becomes degraded": func(d *sigDoc) {
			d.workBeads["sources"].([]any)[0].(map[string]any)["status"] = "failed"
		},
		"ticket state changes": func(d *sigDoc) {
			d.jira["ABC-1"].(map[string]any)["state"] = "done"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := newSigDoc()
			mutate(&d)
			if contentSignature(d.build(t)) == contentSignature(base) {
				t.Errorf("signature did not change on a real change")
			}
		})
	}
}

// Without a decodable pr_show the work-beads value cannot be narrowed to the
// PR, so it is compared whole: a change must never be read as "no change".
func TestContentSignature_WorkBeadsComparedWholeWithoutPRIdentity(t *testing.T) {
	mk := func(updatedAt string) gather.Facts {
		d := newSigDoc()
		ownBead(d, 2)["updated_at"] = updatedAt
		f := d.build(t)
		f.PRShow = nil
		return f
	}
	if contentSignature(mk("2026-10-07T05:00:00Z")) == contentSignature(mk("2026-10-07T09:00:00Z")) {
		t.Error("work-beads must be compared whole when the PR identity is unknown")
	}
}

// The stored row round-trips through JSON before the comparison, and that
// MUST NOT make an unchanged PR look changed.
func TestContentSignature_StableAcrossStoreRoundTrip(t *testing.T) {
	fresh := newSigDoc().build(t)
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var stored gather.Facts
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	if contentSignature(stored) != contentSignature(fresh) {
		t.Error("signature must survive the store round trip")
	}
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
