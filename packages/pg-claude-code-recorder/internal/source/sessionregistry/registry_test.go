package sessionregistry

import (
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

func parse(t *testing.T, name string) map[string]source.Field {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := New().Parse(source.Captures{"pid": "73291"}, raw)
	if err != nil || len(recs) != 1 {
		t.Fatalf("parse: %v, %d records", err, len(recs))
	}
	got := map[string]source.Field{}
	for _, f := range recs[0].Fields {
		got[f.Name] = f
	}
	return got
}

func TestJobIDIndexed(t *testing.T) {
	got := parse(t, "with-jobid.json")
	if got["job_id"].S != "d503eec4" {
		t.Fatalf("job_id = %+v", got["job_id"])
	}
	if got["name"].S != "conversation-timestamp-check" {
		t.Fatalf("name = %+v", got["name"])
	}
}

func TestOldFixtureWithoutJobIDStillParses(t *testing.T) {
	got := parse(t, "without-jobid.json")
	if _, ok := got["job_id"]; ok {
		t.Fatal("job_id produced for a file without jobId")
	}
	if got["session_id"].S == "" || got["name"].S == "" {
		t.Fatalf("fields %v", got)
	}
}

func TestCatalogListsJobID(t *testing.T) {
	if New().Catalog()["job_id"] != source.KindKeyword {
		t.Fatal("job_id missing from the catalog")
	}
}

func TestParserVersionIs3(t *testing.T) {
	if New().Version() != 3 {
		t.Fatalf("version %d", New().Version())
	}
}
