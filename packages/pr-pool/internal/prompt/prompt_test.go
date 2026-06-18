package prompt

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

func TestRender_namedFieldsAndMetadata(t *testing.T) {
	tmpl, err := Parse("worker", "bead {{.BeadID}} in {{.WorktreeDir}} by {{.SelfLogin}}; meta {{.Item.Metadata.author}}")
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Item:        item.Item{ID: "pg2-x", Metadata: map[string]any{"author": "phillipg"}},
		WorktreeDir: "/wt",
		SelfLogin:   "phillipg",
	}
	got, err := Render(tmpl, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bead pg2-x in /wt by phillipg; meta phillipg" {
		t.Fatalf("render = %q", got)
	}
}

func TestRender_missingKeyIsError(t *testing.T) {
	tmpl, err := Parse("x", "hi {{.Item.Metadata.nope}}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Render(tmpl, Context{Item: item.Item{Metadata: map[string]any{}}})
	if err == nil {
		t.Fatal("expected error on missing metadata key")
	}
}

func TestAuthorshipPreamble_present(t *testing.T) {
	p := AuthorshipPreamble()
	for _, want := range []string{"phillipg.", "git push --force", "human"} {
		if !strings.Contains(p, want) {
			t.Fatalf("preamble missing %q: %s", want, p)
		}
	}
}
