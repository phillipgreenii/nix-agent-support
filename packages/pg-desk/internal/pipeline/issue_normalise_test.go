package pipeline

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// issuePayload renders an IssueFacts-shaped payload from an issue_show map
// and an optional issue_deps map.
func issuePayload(t *testing.T, show, deps map[string]any) []byte {
	t.Helper()
	f := map[string]any{"issue_show": show}
	if deps != nil {
		f["issue_deps"] = deps
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseShow() map[string]any {
	return map[string]any{
		"id": "i-1", "title": "a title", "state": "open", "status_category": "new",
		"labels": []any{"x"}, "updated_at": "2026-10-01T00:00:00Z",
		"as_of": "2026-10-07T10:00:00Z", "stale": false,
		"served_from": "origin", "age_seconds": 0,
		"metadata": map[string]any{"keep": "v", "last_checked_at": "2026-10-07T03:00:00Z"},
		"comments": []any{map[string]any{"id": "c1", "body": "hi", "updated": "2026-10-02T00:00:00Z", "as_of": "2026-10-07T10:00:00Z"}},
	}
}

func baseDeps() map[string]any {
	return map[string]any{"ids": []any{"i-2"}, "as_of": "2026-10-07T10:00:00Z", "served_from": "origin", "age_seconds": 3}
}

func mustNormalize(t *testing.T, raw []byte) []byte {
	t.Helper()
	n, err := NormalizeIssuePayload(raw)
	if err != nil {
		t.Fatalf("NormalizeIssuePayload: %v", err)
	}
	return n
}

// An issue payload that differs only in volatile fields normalises to
// identical bytes (the T-1 case for issues).
func TestNormalizeIssuePayload_VolatileFieldsNormaliseEqual(t *testing.T) {
	base := mustNormalize(t, issuePayload(t, baseShow(), baseDeps()))
	cases := map[string]func(show, deps map[string]any){
		"as_of":        func(s, d map[string]any) { s["as_of"] = "2026-10-08T00:00:00Z"; d["as_of"] = "2026-10-08T00:00:00Z" },
		"stale":        func(s, d map[string]any) { s["stale"] = true },
		"stale absent": func(s, d map[string]any) { delete(s, "stale") },
		"updated_at":   func(s, d map[string]any) { s["updated_at"] = "2026-10-09T00:00:00Z" },
		"nested updated and as_of": func(s, d map[string]any) {
			c := s["comments"].([]any)[0].(map[string]any)
			c["updated"], c["as_of"] = "2030-01-01T00:00:00Z", "2030-01-01T00:00:00Z"
		},
		"served_from and age_seconds": func(s, d map[string]any) {
			s["served_from"], s["age_seconds"] = "cache", 90
			d["served_from"], d["age_seconds"] = "cache", 91
		},
		"entity_meta watched true":  func(s, d map[string]any) { s["entity_meta"] = map[string]any{"watched": true} },
		"entity_meta watched false": func(s, d map[string]any) { s["entity_meta"] = map[string]any{"watched": false} },
		"entity_meta in deps":       func(s, d map[string]any) { d["entity_meta"] = map[string]any{"watched": true} },
		"connector fingerprint exclude": func(s, d map[string]any) {
			s["metadata"].(map[string]any)["last_checked_at"] = "2031-01-01T00:00:00Z"
		},
		"top-level volatile keys": func(s, d map[string]any) {},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			show, deps := baseShow(), baseDeps()
			mutate(show, deps)
			raw := issuePayload(t, show, deps)
			if name == "top-level volatile keys" {
				var f map[string]any
				_ = json.Unmarshal(raw, &f)
				f["as_of"], f["entity_meta"], f["served_from"] = "x", map[string]any{"watched": true}, "cache"
				raw, _ = json.Marshal(f)
			}
			if got := mustNormalize(t, raw); !bytes.Equal(got, base) {
				t.Errorf("normalised bytes differ:\n got %s\nwant %s", got, base)
			}
		})
	}
}

// entity_meta present (either value) and absent all normalise equal.
func TestNormalizeIssuePayload_EntityMetaPresentFlippedAbsent(t *testing.T) {
	withMeta := func(v map[string]any) []byte {
		s := baseShow()
		if v != nil {
			s["entity_meta"] = v
		}
		return mustNormalize(t, issuePayload(t, s, nil))
	}
	absent := withMeta(nil)
	for _, v := range []map[string]any{{"watched": true}, {"watched": false}} {
		if got := withMeta(v); !bytes.Equal(got, absent) {
			t.Errorf("entity_meta %v: %s != %s", v, got, absent)
		}
	}
}

// A real field difference is a different payload.
func TestNormalizeIssuePayload_RealChangesDiffer(t *testing.T) {
	base := mustNormalize(t, issuePayload(t, baseShow(), baseDeps()))
	cases := map[string]func(show, deps map[string]any){
		"title":    func(s, d map[string]any) { s["title"] = "another title" },
		"labels":   func(s, d map[string]any) { s["labels"] = []any{"x", "y"} },
		"status":   func(s, d map[string]any) { s["state"] = "done"; s["status_category"] = "done" },
		"metadata": func(s, d map[string]any) { s["metadata"].(map[string]any)["keep"] = "w" },
		"comment":  func(s, d map[string]any) { s["comments"].([]any)[0].(map[string]any)["body"] = "edited" },
		"deps":     func(s, d map[string]any) { d["ids"] = []any{"i-2", "i-3"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			show, deps := baseShow(), baseDeps()
			mutate(show, deps)
			if got := mustNormalize(t, issuePayload(t, show, deps)); bytes.Equal(got, base) {
				t.Errorf("a real change normalised equal: %s", got)
			}
		})
	}
}

// The output is canonical: sorted keys, no whitespace, numbers verbatim, the
// input untouched, and an undecodable payload is an error.
func TestNormalizeIssuePayload_CanonicalForm(t *testing.T) {
	in := []byte(" { \"b\" : 1 , \"a\" : { \"z\" : 12345678901234567890 , \"as_of\":\"t\" , \"y\":\"<&>\" } } ")
	orig := append([]byte(nil), in...)
	got := mustNormalize(t, in)
	if want := `{"a":{"y":"<&>","z":12345678901234567890},"b":1}`; string(got) != want {
		t.Errorf("got %s want %s", got, want)
	}
	if !bytes.Equal(in, orig) {
		t.Error("input modified")
	}
	for _, bad := range []string{"", "{", "{} {}", "not json"} {
		if _, err := NormalizeIssuePayload([]byte(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// issueCompareBytes never turns an unreadable payload into "nothing changed".
func TestIssueCompareBytes_FallsBackToRaw(t *testing.T) {
	if got := issueCompareBytes([]byte("not json")); string(got) != "not json" {
		t.Errorf("got %q", got)
	}
}

func TestIssueUpdatedAt(t *testing.T) {
	if got := issueUpdatedAt(issuePayload(t, baseShow(), nil)); got != "2026-10-01T00:00:00Z" {
		t.Errorf("got %q", got)
	}
	s := baseShow()
	delete(s, "updated_at")
	if got := issueUpdatedAt(issuePayload(t, s, nil)); got != "" {
		t.Errorf("absent updated_at: got %q", got)
	}
	if got := issueUpdatedAt([]byte("nope")); got != "" {
		t.Errorf("undecodable: got %q", got)
	}
}

// Re-hydrating an unchanged issue whose stored facts carry an older as_of and
// entity_meta appends no row; the stored facts and hash follow the rules (raw
// facts, normalised hash); a real change still appends.
func TestRunEntityChange_IssueNormalisedComparison(t *testing.T) {
	mk := func(show map[string]any, asOf string) gather.GatherResult {
		return gather.GatherResult{Payload: issuePayload(t, show, nil), AsOf: asOf}
	}
	oldShow := baseShow()
	oldShow["entity_meta"] = map[string]any{"watched": true}
	res := mk(oldShow, "2026-10-07T10:00:00Z")
	var gerr error
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	first, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")

	newShow := baseShow()
	// updated_at stays as it was: an updated_at-only advance is the classifier's
	// call (the beads comments proxy), not this comparison's.
	newShow["as_of"] = "2026-10-08T10:00:00Z"
	newShow["served_from"], newShow["age_seconds"] = "cache", 40
	newShow["entity_meta"] = map[string]any{"watched": false}
	res = mk(newShow, "2026-10-08T10:00:00Z")
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if len(r.Kinds) != 0 {
		t.Errorf("kinds = %v, want none", r.Kinds)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("rows = %+v, want only the first observation", rows)
	}
	second, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if second.ContentHash != first.ContentHash {
		t.Errorf("content hash moved on a volatile-only change: %s -> %s", first.ContentHash, second.ContentHash)
	}
	var stored struct {
		IssueShow map[string]any `json:"issue_show"`
	}
	if err := json.Unmarshal([]byte(second.Facts), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.IssueShow["as_of"] != "2026-10-08T10:00:00Z" || stored.IssueShow["entity_meta"] == nil {
		t.Errorf("stored facts must stay RAW: %s", second.Facts)
	}
	if want := contentHash(mustNormalize(t, []byte(second.Facts))); second.ContentHash != want {
		t.Errorf("content hash is not the hash of the normalised bytes")
	}

	// A real change still appends a row.
	newShow["state"], newShow["status_category"] = "done", "done"
	res = mk(newShow, "2026-10-09T10:00:00Z")
	r = mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if len(r.Kinds) == 0 {
		t.Error("a real change appended nothing")
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}
