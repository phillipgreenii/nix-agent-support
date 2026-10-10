package workitem

import "testing"

func TestDedupKeyExactStrings(t *testing.T) {
	e := EntityRef{Type: "pr", ID: "acme/widgets#42", NodeID: "PR_node_42"}
	c := Context{HeadSHA: "9f3c1e2", Branch: "feature/retry", Base: "main", BaseSHA: "b45e000", Digest: "d1"}

	idForms := []struct {
		k    Kind
		want string
	}{
		{KindReviewPR, "pr:acme/widgets#42:review-pr"},
		{KindFixCI, "pr:acme/widgets#42:fix-ci:9f3c1e2"},
		{KindResolveConflict, "pr:acme/widgets#42:resolve-conflict:feature/retry:9f3c1e2:main:b45e000"},
		{KindProcessFeedback, "pr:acme/widgets#42:process-feedback:d1"},
		{KindAnchor, "pr:acme/widgets#42:anchor"},
		{KindFocusItem, "pr:acme/widgets#42:focus-item"},
	}
	nodeForms := []struct {
		k    Kind
		want string
	}{
		{KindReviewPR, "pr:PR_node_42:review-pr"},
		{KindFixCI, "pr:PR_node_42:fix-ci:9f3c1e2"},
		{KindResolveConflict, "pr:PR_node_42:resolve-conflict:feature/retry:9f3c1e2:main:b45e000"},
		{KindProcessFeedback, "pr:PR_node_42:process-feedback:d1"},
		{KindAnchor, "pr:PR_node_42:anchor"},
		{KindFocusItem, "pr:PR_node_42:focus-item"},
	}
	for _, tc := range idForms {
		if got := DedupKey(e, tc.k, c); got != tc.want {
			t.Errorf("DedupKey(%s) = %q, want %q", tc.k, got, tc.want)
		}
	}
	for _, tc := range nodeForms {
		got, ok := DedupKeyNodeID(e, tc.k, c)
		if !ok || got != tc.want {
			t.Errorf("DedupKeyNodeID(%s) = %q,%v want %q", tc.k, got, ok, tc.want)
		}
	}
}

func TestDedupKeyNodeIDAbsent(t *testing.T) {
	e := EntityRef{Type: "pr", ID: "acme/widgets#42"}
	if got, ok := DedupKeyNodeID(e, KindReviewPR, Context{}); ok || got != "" {
		t.Fatalf("got %q,%v", got, ok)
	}
}

func TestContextSuffix(t *testing.T) {
	c := Context{HeadSHA: "h", Branch: "b", Base: "m", BaseSHA: "s", Digest: "d"}
	cases := map[Kind]string{
		KindAnchor:          "",
		KindReviewPR:        "",
		KindFixCI:           ":h",
		KindResolveConflict: ":b:h:m:s",
		KindProcessFeedback: ":d",
		KindFocusItem:       "",
		Kind("bogus"):       "",
	}
	for k, want := range cases {
		if got := ContextSuffix(k, c); got != want {
			t.Errorf("ContextSuffix(%s) = %q, want %q", k, got, want)
		}
	}
}

// D-F13: one bead per source entity, so the key never varies with the day or
// with any context value.
func TestFocusItemDedupKeyIsStableAndHasNoSuffix(t *testing.T) {
	e := EntityRef{Type: "issue", ID: "PROJ-1"}
	const want = "issue:PROJ-1:focus-item"
	for _, c := range []Context{{}, {HeadSHA: "a", Branch: "b", Base: "c", BaseSHA: "d", Digest: "e"}, {HeadSHA: "z"}} {
		if got := DedupKey(e, KindFocusItem, c); got != want {
			t.Errorf("DedupKey(%+v) = %q, want %q", c, got, want)
		}
	}
}

func TestParseKeyAcceptsFocusItemForIssueAndPR(t *testing.T) {
	cases := []struct{ key, typ, ident string }{
		{"issue:PROJ-1:focus-item", "issue", "PROJ-1"},
		{"pr:OWNER/REPO#3:focus-item", "pr", "OWNER/REPO#3"},
	}
	for _, tc := range cases {
		typ, ident, k, suffix, ok := ParseKey(tc.key)
		if !ok || typ != tc.typ || ident != tc.ident || k != KindFocusItem || suffix != "" {
			t.Errorf("ParseKey(%q) = %q %q %q %q %v", tc.key, typ, ident, k, suffix, ok)
		}
	}
}

func TestParseKeyRoundTrip(t *testing.T) {
	e := EntityRef{Type: "pr", ID: "acme/widgets#42", NodeID: "PR_node_42"}
	c := Context{HeadSHA: "h", Branch: "feature/x", Base: "main", BaseSHA: "s", Digest: "d"}
	for _, k := range Kinds() {
		for _, key := range []string{DedupKey(e, k, c), mustNode(t, e, k, c)} {
			typ, ident, gk, suffix, ok := ParseKey(key)
			if !ok || typ != "pr" || gk != k || suffix != ContextSuffix(k, c) {
				t.Errorf("ParseKey(%q) = %q %q %q %q %v", key, typ, ident, gk, suffix, ok)
			}
			if ident != e.ID && ident != e.NodeID {
				t.Errorf("ParseKey(%q) ident %q", key, ident)
			}
		}
	}
}

func mustNode(t *testing.T, e EntityRef, k Kind, c Context) string {
	t.Helper()
	s, ok := DedupKeyNodeID(e, k, c)
	if !ok {
		t.Fatal("no node id form")
	}
	return s
}

func TestParseKeyRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "not a key", "pr:acme/widgets#42", "pr:acme/widgets#42:bogus", "pr::review-pr", ":x:review-pr", "pr:x:", "a:b"} {
		if _, _, _, _, ok := ParseKey(s); ok {
			t.Errorf("ParseKey(%q) ok", s)
		}
	}
}

func TestEntityRefOwnsEitherForm(t *testing.T) {
	e := EntityRef{Type: "pr", ID: "acme/widgets#42", NodeID: "PR_node_42"}
	yes := []string{"pr:acme/widgets#42:review-pr", "pr:PR_node_42:fix-ci:h"}
	no := []string{"pr:other/repo#9:review-pr", "issue:acme/widgets#42:review-pr", "garbage"}
	for _, k := range yes {
		if !e.Owns(k) {
			t.Errorf("Owns(%q) = false", k)
		}
	}
	for _, k := range no {
		if e.Owns(k) {
			t.Errorf("Owns(%q) = true", k)
		}
	}
	noNode := EntityRef{Type: "pr", ID: "acme/widgets#42"}
	if noNode.Owns("pr:PR_node_42:review-pr") {
		t.Error("node form owned without a node id")
	}
	if (EntityRef{Type: "pr", ID: "", NodeID: ""}).Owns("pr::review-pr") {
		t.Error("empty ref owns something")
	}
}
