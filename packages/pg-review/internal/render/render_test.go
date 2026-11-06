package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

const goldenDir = "../../testdata/review-item"

func goldenItem(t *testing.T) (Item, validate.Document) {
	t.Helper()
	var it Item
	b, err := os.ReadFile(filepath.Join(goldenDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &it); err != nil {
		t.Fatal(err)
	}
	var doc validate.Document
	b, err = os.ReadFile(filepath.Join(goldenDir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	it.Finding = doc.Findings[0]
	return it, doc
}

func TestGoldenItem(t *testing.T) {
	it, _ := goldenItem(t)
	got := Render(it)
	path := filepath.Join(goldenDir, "item.golden")
	if os.Getenv("PG_REVIEW_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("rendered item differs from %s:\n%s", path, got)
	}
}

// TestGoldenItemPassesValidator is the Appendix B check: the findings pass
// the validator, and the rendered text (header aside) has no token that is
// neither in the glossary nor on the allowlist.
func TestGoldenItemPassesValidator(t *testing.T) {
	it, doc := goldenItem(t)
	in, err := validate.Load(goldenDir, filepath.Join(goldenDir, "raw.txt"))
	if err != nil {
		t.Fatal(err)
	}
	in.Doc = doc
	in.Kinds = []string{"wasted-retries"}
	in.MaxFindings = 5
	if err := validate.Validate(in); err != nil {
		t.Fatal(err)
	}
	out := Render(it)
	_, body, _ := strings.Cut(out, "\n\n") // drop the header
	var gl []string
	for _, g := range it.Finding.Glossary {
		gl = append(gl, g.Term)
	}
	gl = append(gl, it.Finding.Evidence...)
	if gaps := validate.GlossaryGaps(body, gl); len(gaps) != 0 {
		t.Errorf("rendered item has undefined terms: %v", gaps)
	}
}

func TestSectionsInOrder(t *testing.T) {
	it, _ := goldenItem(t)
	out := Render(it)
	last := -1
	for i, name := range Sections {
		heading := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(strconv.Itoa(i+1)+". "+name) + `$`)
		loc := heading.FindStringIndex(out)
		if loc == nil {
			t.Fatalf("section %d %q missing", i+1, name)
		}
		if loc[0] <= last {
			t.Fatalf("section %q out of order", name)
		}
		last = loc[0]
	}
	if len(Sections) != 13 {
		t.Fatalf("want 13 sections, have %d", len(Sections))
	}
	head := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"Item 7", "State: Pending", "Pending: 3"} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q lacks %q", head, want)
		}
	}
}

func TestEmptySectionStillRendered(t *testing.T) {
	it, _ := goldenItem(t)
	it.Finding.Impact = ""
	it.Missing = nil
	out := Render(it)
	if !strings.Contains(out, "8. Impact\n(not stated)") || !strings.Contains(out, "11. What information is missing\nNone.") {
		t.Errorf("empty sections not rendered as expected:\n%s", out)
	}
}

func TestStripControlAndANSI(t *testing.T) {
	in := "a\x1b[31mred\x1b[0m\x07b\x00c\u0085d\x1b]0;title\x07e\tf\ng\x7fh"
	got := Strip(in)
	want := "aredbcde\tf\ngh"
	if got != want {
		t.Errorf("Strip = %q, want %q", got, want)
	}
	it, _ := goldenItem(t)
	it.Finding.Title = "Clean\x1b[1m title\x1b[0m?"
	if out := Render(it); strings.Contains(out, "\x1b") || !strings.Contains(out, "Clean title?") {
		t.Errorf("render did not strip the title: %q", out)
	}
}

func TestRenumberRewritesHeaderAndEvidenceHint(t *testing.T) {
	in := "Item 0    State: Pending    Pending: 0\n\n2. Where from\nRaw evidence: pg-review human show 0 --evidence\n"
	got := Renumber(in, 7, "Deferred", 3)
	want := "Item 7    State: Deferred    Pending: 3\n\n2. Where from\nRaw evidence: pg-review human show 7 --evidence\n"
	if got != want {
		t.Errorf("got %q", got)
	}
	if Renumber("no header", 7, "x", 1) != "no header" {
		t.Error("text without a header changed")
	}
}
