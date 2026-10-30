package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Ladder rung 9, posted_review: the daemon's per-PR posted-review sidecar. The
// store only creates the table; the daemon's sidecar package owns the accessors.

func TestLadderRung9IsNamed(t *testing.T) {
	const version = 9
	ladder := BuiltinMigrations()
	if len(ladder) < version || SchemaVersion < version {
		t.Fatalf("the ladder has %d rungs and SchemaVersion = %d, want at least %d of each", len(ladder), SchemaVersion, version)
	}
	if rung := ladder[version-1]; rung.Version != version || rung.Name != "posted_review" {
		t.Errorf("rung 9 = %d %q, want 9 %q", rung.Version, rung.Name, "posted_review")
	}
}

// TestPostedReviewTableShape pins the table the sidecar reads and writes: the
// five columns, the composite primary key and WITHOUT ROWID.
func TestPostedReviewTableShape(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if got, want := columnNames(t, s, "posted_review"), []string{"owner", "repo", "number", "state", "updated_at"}; !slices.Equal(got, want) {
		t.Errorf("columns = %v, want %v", got, want)
	}
	var sqlText string
	if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'posted_review'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PRIMARY KEY (owner, repo, number)", "WITHOUT ROWID"} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("table SQL lacks %q:\n%s", want, sqlText)
		}
	}
	insert := `INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES ('o', 'r', 1, '{}', 1)`
	if _, err := s.DB().Exec(insert); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(insert); err == nil {
		t.Error("a second row for the same (owner, repo, number) must be refused")
	}
}

// TestOpenFromOlderFixturesAddsAnEmptyPostedReviewTable: every fixture below
// version 9 migrates to the top with the new table present and empty, and the
// version 9 fixture keeps its sample row.
func TestOpenFromOlderFixturesAddsAnEmptyPostedReviewTable(t *testing.T) {
	exercised := 0
	for v, fx := range fixtures(t) {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			path := dbPath(t)
			copyFile(t, fx, path)
			s, res := mustOpen(t, path, fastOpts())
			if res.ToVersion != SchemaVersion {
				t.Fatalf("result = %+v, want ToVersion %d", res, SchemaVersion)
			}
			var n int
			if err := s.DB().QueryRow(`SELECT count(*) FROM posted_review`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 0
			if v >= 9 {
				want = 1
			}
			if n != want {
				t.Errorf("posted_review rows = %d, want %d", n, want)
			}
		})
		exercised++
	}
	if exercised < 9 {
		t.Fatalf("only %d fixtures were exercised", exercised)
	}
}
