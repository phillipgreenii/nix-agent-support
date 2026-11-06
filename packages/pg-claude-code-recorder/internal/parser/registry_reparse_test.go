package parser_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// A registry version stored by parser v2 is reparsed from its retained bytes,
// replacing (not duplicating) its records and gaining job_id.
func TestRegistryVersionStoredByV2IsReparsed(t *testing.T) {
	raw, err := os.ReadFile("../source/sessionregistry/testdata/with-jobid.json")
	if err != nil {
		t.Fatal(err)
	}
	k := testkit.New(t)
	k.AddRegistry("73291", string(raw))
	k.Parse()
	records := k.Count(`SELECT COUNT(*) FROM records`)
	if records == 0 {
		t.Fatal("nothing parsed")
	}
	jobs := `SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='job_id' AND f.s='d503eec4'`
	// Emulate a v2 parse: stale version and no job_id field.
	err = k.Store.WithWrite("", func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE parse_state SET version=2`); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM fields WHERE name_id IN (SELECT id FROM field_names WHERE name='job_id')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if k.Count(jobs) != 0 {
		t.Fatal("job_id not removed")
	}
	k.Parse()
	if k.Count(jobs) == 0 {
		t.Fatal("stored v2 version was not reparsed")
	}
	if got := k.Count(`SELECT COUNT(*) FROM records`); got != records {
		t.Fatalf("records %d, want %d", got, records)
	}
	if k.Count(`SELECT COUNT(*) FROM parse_state WHERE version=3`) == 0 {
		t.Fatal("parse_state not advanced to 3")
	}
}
