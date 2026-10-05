package item

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFromFile(t *testing.T) {
	it, err := Read(filepath.Join("..", "..", "testdata", "routed_item.json"))
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "item-1" || it.Type != "pr.changed" || it.Title != "acme/widgets#42 changed" {
		t.Fatalf("top-level: %+v", it)
	}
	m := it.Metadata
	if m.EntityType != "pr" || m.EntityID != "acme/widgets#42" || m.Kind != "changed" ||
		m.Seq != 17 || m.Version != 7 || m.Origin != "derived:poll" ||
		len(m.DegradedSources) != 1 || m.DegradedSources[0] != "checks" {
		t.Fatalf("metadata: %+v", m)
	}
}

func TestReadDashReadsStdin(t *testing.T) {
	orig := stdin
	t.Cleanup(func() { stdin = orig })
	stdin = strings.NewReader(`{"id":"i","type":"issue.updated","title":"t","metadata":{"entity_type":"issue","entity_id":"x","kind":"updated","seq":3}}`)
	it, err := Read("-")
	if err != nil {
		t.Fatal(err)
	}
	if it.Metadata.Seq != 3 || it.Metadata.EntityID != "x" {
		t.Fatalf("got %+v", it)
	}
}

func TestReadToleratesMissingMetadataAndUnknownMembers(t *testing.T) {
	orig := stdin
	t.Cleanup(func() { stdin = orig })
	stdin = strings.NewReader(`{"id":"i","type":"pr.changed","title":"t","extra":1}`)
	it, err := Read("-")
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "i" || it.Metadata.Seq != 0 {
		t.Fatalf("got %+v", it)
	}
}

func TestReadErrors(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bad); err == nil {
		t.Fatal("expected error for unparseable item")
	}
}
