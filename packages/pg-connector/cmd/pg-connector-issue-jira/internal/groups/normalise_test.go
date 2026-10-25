package groups

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func base() SummaryContent {
	return SummaryContent{
		Issue: schema.Issue{
			ID: "PROJ-12", Title: "t", State: "Open", StatusCategory: "new",
			Labels: []string{"a"}, CommentCount: 2, LatestCommentAt: "2026-10-05T10:00:00Z",
		},
		Updated: "2026-10-05T10:11:12.000+0000",
	}
}

func TestNormaliseDropsVolatileFields(t *testing.T) {
	a := base()
	b := base()
	b.Issue.AsOf = "2026-10-06T00:00:00Z"
	b.Issue.Stale = true
	b.Issue.Version = 9
	b.Issue.UpdatedAt = "2026-10-06T00:00:00Z"
	b.Updated = "2026-10-06T10:11:12.000+0000"

	ja, _ := json.Marshal(Normalise(a))
	jb, _ := json.Marshal(Normalise(b))
	if string(ja) != string(jb) {
		t.Fatalf("normalised bytes differ:\n%s\n%s", ja, jb)
	}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprints differ for volatile-only change")
	}
}

func TestNormaliseKeepsLabelChange(t *testing.T) {
	a := base()
	b := base()
	b.Issue.Labels = []string{"a", "b"}
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("a changed label must change the fingerprint")
	}
	ja, _ := json.Marshal(Normalise(a))
	jb, _ := json.Marshal(Normalise(b))
	if string(ja) == string(jb) {
		t.Fatal("a changed label must not normalise away")
	}
}

func TestNormaliseDoesNotModifyInput(t *testing.T) {
	a := base()
	_ = Normalise(a)
	if a.Updated == "" {
		t.Fatal("input modified")
	}
}

func TestStoredKeepsUpdated(t *testing.T) {
	a := base()
	a.Issue.AsOf = "x"
	a.Issue.Version = 3
	s := Stored(a)
	if s.Updated != a.Updated {
		t.Fatalf("Stored dropped Updated: %q", s.Updated)
	}
	if s.Issue.AsOf != "" || s.Issue.Version != 0 {
		t.Fatalf("Stored kept volatile fields: %+v", s.Issue)
	}
	if Normalise(a).Updated != "" {
		t.Fatal("Normalise must zero Updated")
	}
}

func TestNormaliseDetail(t *testing.T) {
	d := DetailContent{
		Description:     "d",
		Comments:        []Comment{{ID: "1", Body: "héllo wörld"}},
		StatusChangedAt: "s", OperatorUpdatedAt: "o",
	}
	n := NormaliseDetail(d, 2)
	if n.StatusChangedAt != "" || n.OperatorUpdatedAt != "" {
		t.Fatal("operator facts must be zeroed")
	}
	// "hé" is 3 bytes; a cap of 2 must back off the split rune to "h".
	if got := n.Comments[0].Body; got != "h" {
		t.Fatalf("body = %q, want %q", got, "h")
	}
	if d.Comments[0].Body != "héllo wörld" {
		t.Fatal("input comments modified")
	}
	if got := NormaliseDetail(d, 3).Comments[0].Body; got != "hé" {
		t.Fatalf("body = %q, want hé", got)
	}
	if got := NormaliseDetail(d, 0).Comments[0].Body; got != d.Comments[0].Body {
		t.Fatalf("cap 0 should not cut: %q", got)
	}
}

func TestFingerprintDetail(t *testing.T) {
	a := DetailContent{Description: "d", Comments: []Comment{{ID: "1", Body: strings.Repeat("x", 10)}}}
	b := a
	b.StatusChangedAt = "later"
	if FingerprintDetail(a, 4) != FingerprintDetail(b, 4) {
		t.Fatal("operator facts must not change the fingerprint")
	}
	// bodies differing only past the cap are equal.
	c := DetailContent{Description: "d", Comments: []Comment{{ID: "1", Body: strings.Repeat("x", 4) + "yyyy"}}}
	if FingerprintDetail(a, 4) != FingerprintDetail(c, 4) {
		t.Fatal("bodies equal within the cap must fingerprint equal")
	}
	e := a
	e.Description = "other"
	if FingerprintDetail(a, 4) == FingerprintDetail(e, 4) {
		t.Fatal("description change must change the fingerprint")
	}
}

func TestIsClosed(t *testing.T) {
	for cat, want := range map[string]bool{"done": true, "new": false, "indeterminate": false, "": false} {
		if got := IsClosed(schema.Issue{StatusCategory: cat}); got != want {
			t.Errorf("IsClosed(%q) = %v, want %v", cat, got, want)
		}
	}
}

func TestNamesAndContentShape(t *testing.T) {
	if got := All(); len(got) != 2 || got[0] != Summary || got[1] != Detail {
		t.Fatalf("All() = %v", got)
	}
	if Summary != "summary" || Detail != "detail" || EntityKind != "issue" {
		t.Fatal("names")
	}
	d := DetailContent{Description: "d", Comments: []Comment{{ID: "1"}}, Deps: []schema.IssueDependency{{ID: "P-1", Type: "blocks"}}, StatusChangedAt: "s", OperatorUpdatedAt: "o"}
	b, _ := json.Marshal(d)
	var back DetailContent
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Description != "d" || len(back.Comments) != 1 || len(back.Deps) != 1 || back.StatusChangedAt != "s" || back.OperatorUpdatedAt != "o" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}
