package settle

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"

	_ "modernc.org/sqlite" // the database driver
)

// TestRegistryQueryPlansStartFromTheSessionIndex guards the three reads that
// filter records by session AND source. records_source_type exists for the
// lifecycle engine's source-only scans; for these a session is far more
// selective, and the planner (the store has no sqlite_stat1) would prefer the
// source index and walk every registry record. The plans must start from
// records_session and never scan records or fields.
func TestRegistryQueryPlansStartFromTheSessionIndex(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sources := []string{"session-registry", "claude-history", "claude-transcript", "other"}
	for i := 1; i <= 2000; i++ {
		if _, err := db.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns)
VALUES(?,1,1,?,0,1,?,'x',1,?,?)`, i, i, sources[i%len(sources)], fmt.Sprintf("s%d", i%40), i); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO field_names(name) VALUES('version')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO fields(rec_id,name_id,s) VALUES(?,(SELECT id FROM field_names WHERE name='version'),'v')`, i); err != nil {
			t.Fatal(err)
		}
	}
	scan := regexp.MustCompile(`^SCAN (r|f|records|fields)\b`)
	for name, q := range map[string]string{
		"claudeVersion":    claudeVersionQuery,
		"registryVersions": registryVersionsQuery,
		"registryNames":    registryNamesQuery,
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := db.Query("EXPLAIN QUERY PLAN "+q, "s1")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var d string
				if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, d)
				if scan.MatchString(d) {
					t.Errorf("plan scans: %q", d)
				}
			}
			if !strings.Contains(strings.Join(plan, "\n"), "SEARCH r USING INDEX records_session") {
				t.Errorf("plan does not start from records_session: %v", plan)
			}
		})
	}
}
