package verdict

import (
	"strings"
	"testing"
)

// All markers/patterns/logins in this file are synthetic and test-local.
// None of them are real vendor text (see the bead's public-repo-hygiene
// note); they reproduce the SHAPE of the real observed-mapping table with
// invented placeholder strings only.

const testMarker = "X-TEST-MARKER"

// Generation "v1" is the analog of an OLDER bot-format generation: a single
// bare/partial/denied verdict line, findings and authority intertwined in
// one signal.
const (
	v1CleanPat    = `(?im)^GEN1-APPROVED\b` // matches "GEN1-APPROVED" and "GEN1-APPROVED-PARTIAL"
	v1ProblemsPat = `(?im)^GEN1-NOT-APPROVED$`
	v1ApprovedPat = `(?im)^GEN1-APPROVED$` // bare only, NOT "-PARTIAL"
	v1WithheldPat = `(?im)^GEN1-FORMAL-BLOCKED$`
)

// Generation "v2" is the analog of the CURRENT/newer bot-format generation:
// findings and authority reported on separate lines.
const (
	v2CleanPat    = `(?im)^GEN2-CLEAN$`
	v2ProblemsPat = `(?im)^GEN2-PROBLEMS$`
	v2ApprovedPat = `(?im)^GEN2-AUTHORITY-GRANTED$`
	v2WithheldPat = `(?im)^GEN2-AUTHORITY-BLOCKED$`
)

// testGenerations returns fresh v1/v2 Generation values (declared in that
// order — v2 is the "highest declared" / most-recently-added generation) so
// each test gets its own slice and none share backing arrays via append.
func testGenerations() []Generation {
	return []Generation{
		{
			ID:                "v1",
			BodyMarker:        testMarker,
			FindingsPatterns:  []string{v1CleanPat, v1ProblemsPat},
			AuthorityPatterns: []string{v1ApprovedPat, v1WithheldPat},
		},
		{
			ID:                "v2",
			BodyMarker:        testMarker,
			FindingsPatterns:  []string{v2CleanPat, v2ProblemsPat},
			AuthorityPatterns: []string{v2ApprovedPat, v2WithheldPat},
		},
	}
}

func mustClassifier(t *testing.T, gens []Generation) *Classifier {
	t.Helper()
	c, err := New(gens)
	if err != nil {
		t.Fatalf("New(%+v) unexpected error: %v", gens, err)
	}
	return c
}

// TestClassify is the seven-row table, shaped identically to the bead's real
// observed-mapping table (see internal/verdict package doc and the bead
// description) but using only the synthetic markers/patterns declared above.
// Every Findings value (clean, problems, unknown) and every Authority value
// (approved, withheld, pending, absent -- absent is covered by the separate
// no-marker tests below, since it can never arise from a body that DOES
// carry the marker) that CAN arise from a markered body appears at least
// once here.
func TestClassify(t *testing.T) {
	c := mustClassifier(t, testGenerations())

	cases := []struct {
		name string
		body string
		want Result
	}{
		{
			// Analog of the hourglass "review in progress" placeholder: the
			// marker is present (it IS a verdict-bearing comment) but no
			// generation's grammar recognizes anything in it.
			name: "in-progress placeholder: marker present, no grammar match",
			body: testMarker + "\nGEN-PENDING-PLACEHOLDER",
			want: Result{Findings: FindingsUnknown, Authority: Pending},
		},
		{
			name: "gen2 clean + approved",
			body: testMarker + "\nGEN2-CLEAN\nGEN2-AUTHORITY-GRANTED",
			want: Result{MatchedGeneration: "v2", Findings: Clean, Authority: Approved},
		},
		{
			name: "gen2 clean + withheld",
			body: testMarker + "\nGEN2-CLEAN\nGEN2-AUTHORITY-BLOCKED",
			want: Result{MatchedGeneration: "v2", Findings: Clean, Authority: Withheld},
		},
		{
			name: "gen2 problems, no authority line at all",
			body: testMarker + "\nGEN2-PROBLEMS",
			want: Result{MatchedGeneration: "v2", Findings: Problems, Authority: Withheld},
		},
		{
			name: "gen1 bare approved",
			body: testMarker + "\nGEN1-APPROVED",
			want: Result{MatchedGeneration: "v1", Findings: Clean, Authority: Approved},
		},
		{
			name: "gen1 partial-clean + formal-blocked",
			body: testMarker + "\nGEN1-APPROVED-PARTIAL\nGEN1-FORMAL-BLOCKED",
			want: Result{MatchedGeneration: "v1", Findings: Clean, Authority: Withheld},
		},
		{
			name: "gen1 not-approved",
			body: testMarker + "\nGEN1-NOT-APPROVED",
			want: Result{MatchedGeneration: "v1", Findings: Problems, Authority: Withheld},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Classify(tc.body)
			if got != tc.want {
				t.Errorf("Classify(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyNoMarker pins Authority Absent: a body with verdict-shaped
// pattern text but WITHOUT the anchor marker is not a verdict at all and
// must be ignored entirely, never misclassified from the pattern text alone.
func TestClassifyNoMarker(t *testing.T) {
	c := mustClassifier(t, testGenerations())

	body := "GEN2-CLEAN\nGEN2-AUTHORITY-GRANTED" // verdict-shaped, no marker
	want := Result{Findings: FindingsUnknown, Authority: Absent}
	if got := c.Classify(body); got != want {
		t.Errorf("Classify(%q) = %+v, want %+v", body, got, want)
	}
}

// TestClassifyEmptyAndWhitespaceBody pins that empty/whitespace-only bodies
// are treated the same as any other marker-free body: Absent, no panic.
func TestClassifyEmptyAndWhitespaceBody(t *testing.T) {
	c := mustClassifier(t, testGenerations())
	want := Result{Findings: FindingsUnknown, Authority: Absent}

	for _, body := range []string{"", "   \n\t  "} {
		if got := c.Classify(body); got != want {
			t.Errorf("Classify(%q) = %+v, want %+v", body, got, want)
		}
	}
}

// TestClassifyMarkerSplitAcrossLineBoundary documents and pins that the
// BodyMarker match is a plain contiguous substring check: a marker whose
// text is split across a line boundary must NOT be treated as present.
func TestClassifyMarkerSplitAcrossLineBoundary(t *testing.T) {
	c := mustClassifier(t, testGenerations())

	// testMarker is "X-TEST-MARKER"; split it as "X-TEST" + "\n" + "-MARKER"
	// so the literal contiguous substring never appears, even though a
	// naive "does the body contain both halves" check would be fooled.
	body := "X-TEST\n-MARKER\nGEN2-CLEAN\nGEN2-AUTHORITY-GRANTED"
	want := Result{Findings: FindingsUnknown, Authority: Absent}
	if got := c.Classify(body); got != want {
		t.Errorf("Classify(%q) = %+v, want %+v (marker must be contiguous)", body, got, want)
	}
}

// TestClassifyContradictions asserts the two grammar-contradiction
// invariants as REJECTIONS, not silent precedence resolutions. Both are
// structural analogs of the two real correlation invariants named in the
// bead description.
func TestClassifyContradictions(t *testing.T) {
	c := mustClassifier(t, testGenerations())

	t.Run("problems body also carries an authority-granting line", func(t *testing.T) {
		body := testMarker + "\nGEN2-PROBLEMS\nGEN2-AUTHORITY-GRANTED"
		got := c.Classify(body)
		if !got.Contradiction {
			t.Fatalf("Classify(%q) = %+v, want Contradiction=true", body, got)
		}
		if got.Authority == Approved {
			t.Errorf("Classify(%q).Authority = %v, a contradiction must never resolve to Approved", body, got.Authority)
		}
		if got.Findings != Problems || got.MatchedGeneration != "v2" {
			t.Errorf("Classify(%q) = %+v, want Findings=Problems MatchedGeneration=v2", body, got)
		}
		if got.ContradictionReason == "" {
			t.Errorf("Classify(%q).ContradictionReason is empty, want a non-empty explanation", body)
		}
	})

	t.Run("partial-clean body has no corresponding blocked line", func(t *testing.T) {
		body := testMarker + "\nGEN1-APPROVED-PARTIAL" // no GEN1-FORMAL-BLOCKED
		got := c.Classify(body)
		if !got.Contradiction {
			t.Fatalf("Classify(%q) = %+v, want Contradiction=true", body, got)
		}
		if got.Authority == Approved {
			t.Errorf("Classify(%q).Authority = %v, a contradiction must never resolve to Approved", body, got.Authority)
		}
		if got.Findings != Clean || got.MatchedGeneration != "v1" {
			t.Errorf("Classify(%q) = %+v, want Findings=Clean MatchedGeneration=v1", body, got)
		}
		if got.ContradictionReason == "" {
			t.Errorf("Classify(%q).ContradictionReason is empty, want a non-empty explanation", body)
		}
	})
}

// TestClassifyTwoGenerationMatchTieBreak pins that a body matching two
// generations' grammars resolves deterministically to the HIGHEST DECLARED
// generation (v2, declared after v1) with no panic.
func TestClassifyTwoGenerationMatchTieBreak(t *testing.T) {
	c := mustClassifier(t, testGenerations())

	// Satisfies v1's clean+approved grammar (GEN1-APPROVED) AND v2's
	// problems grammar (GEN2-PROBLEMS) simultaneously.
	body := testMarker + "\nGEN1-APPROVED\nGEN2-PROBLEMS"
	want := Result{MatchedGeneration: "v2", Findings: Problems, Authority: Withheld}
	if got := c.Classify(body); got != want {
		t.Errorf("Classify(%q) = %+v, want %+v (v2, declared later, must win)", body, got, want)
	}
}

// TestClassifyExtensibility proves a third generation can be added purely
// via test-local config data, with no change to any non-test package code.
func TestClassifyExtensibility(t *testing.T) {
	gens := testGenerations()
	gens = append(gens, Generation{
		ID:                "v3",
		BodyMarker:        "X-TEST-MARKER-V3",
		FindingsPatterns:  []string{`(?im)^GEN3-ALL-CLEAR$`},
		AuthorityPatterns: []string{`(?im)^GEN3-SHIP-IT$`},
	})
	c := mustClassifier(t, gens)

	body := "X-TEST-MARKER-V3\nGEN3-ALL-CLEAR\nGEN3-SHIP-IT"
	want := Result{MatchedGeneration: "v3", Findings: Clean, Authority: Approved}
	if got := c.Classify(body); got != want {
		t.Errorf("Classify(%q) = %+v, want %+v", body, got, want)
	}

	// The original two generations still classify correctly on the same
	// Classifier instance, unaffected by the addition.
	origBody := testMarker + "\nGEN1-APPROVED"
	origWant := Result{MatchedGeneration: "v1", Findings: Clean, Authority: Approved}
	if got := c.Classify(origBody); got != origWant {
		t.Errorf("Classify(%q) = %+v, want %+v", origBody, got, origWant)
	}
}

// TestNewBadPattern pins that an uncompilable regex fails New with an error
// naming both the generation ID and which field failed to compile.
func TestNewBadPattern(t *testing.T) {
	t.Run("bad findings pattern", func(t *testing.T) {
		_, err := New([]Generation{{
			ID:               "bad-findings-gen",
			BodyMarker:       "X",
			FindingsPatterns: []string{"("}, // uncompilable
		}})
		if err == nil {
			t.Fatal("New: want error for uncompilable findings_patterns entry, got nil")
		}
		if !strings.Contains(err.Error(), "bad-findings-gen") {
			t.Errorf("New error %q does not name the generation ID", err.Error())
		}
		if !strings.Contains(err.Error(), "findings_patterns") {
			t.Errorf("New error %q does not name findings_patterns", err.Error())
		}
	})

	t.Run("bad authority pattern", func(t *testing.T) {
		_, err := New([]Generation{{
			ID:                "bad-authority-gen",
			BodyMarker:        "X",
			AuthorityPatterns: []string{"("}, // uncompilable
		}})
		if err == nil {
			t.Fatal("New: want error for uncompilable authority_patterns entry, got nil")
		}
		if !strings.Contains(err.Error(), "bad-authority-gen") {
			t.Errorf("New error %q does not name the generation ID", err.Error())
		}
		if !strings.Contains(err.Error(), "authority_patterns") {
			t.Errorf("New error %q does not name authority_patterns", err.Error())
		}
	})
}

// TestNewZeroGenerations pins that an empty/nil generations slice is valid
// (no error) and classifies every body Unknown/Absent.
func TestNewZeroGenerations(t *testing.T) {
	for _, gens := range [][]Generation{nil, {}} {
		c := mustClassifier(t, gens)
		want := Result{Findings: FindingsUnknown, Authority: Absent}
		if got := c.Classify(testMarker + "\nGEN2-CLEAN\nGEN2-AUTHORITY-GRANTED"); got != want {
			t.Errorf("Classify with zero generations = %+v, want %+v", got, want)
		}
	}
}
