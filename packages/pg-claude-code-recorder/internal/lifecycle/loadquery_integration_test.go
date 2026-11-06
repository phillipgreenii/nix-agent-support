//go:build integration

package lifecycle

import (
	"fmt"
	"strings"
	"testing"
)

// planOf runs EXPLAIN QUERY PLAN on the fixture's database and returns the
// detail column of every plan row.
func (f *cwdFixture) planOf(q string, args ...any) []string {
	f.t.Helper()
	rows, err := f.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// TestLoadQueryPlansUseTheSourceIndex guards the four reads of Engine.load. Each
// filters records by source (and type) and then reads fields by name. The store
// has no index on fields by name for string values (fields_name_n is partial on
// n IS NOT NULL), so without records_source_type the planner scans every fields
// row of the store; on a store of 196 million field rows that took minutes.
func TestLoadQueryPlansUseTheSourceIndex(t *testing.T) {
	f := newCwdFixture(t)
	sources := []string{"session-registry", "claude-history", "claude-control-end", transcript, "other"}
	names := []string{"pid", "proc_start", "cwd", "slash_command", "reason", "name", "status", "noise"}
	for i := 0; i < 3000; i++ {
		src := sources[i%len(sources)]
		var fs [][2]any
		for j, n := range names {
			if (i+j)%2 == 0 {
				fs = append(fs, [2]any{n, fmt.Sprintf("v%d", i%17)})
			}
		}
		f.rec(fmt.Sprintf("s%d", i%40), src, int64(i), fs...)
	}
	if _, err := f.db.Exec(`UPDATE records SET type='registry' WHERE source='session-registry' AND id%2=0;
UPDATE records SET type='registry_deleted' WHERE source='session-registry' AND id%2=1`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ name, sql string }{
		{"registry", registryQuery},
		{"registry_deleted", registryDeletedQuery},
		{"slash_command", slashCommandQuery},
		{"end_reason", endReasonQuery},
		{"registry_ref", viaSessionRef(registryQuery)},
		{"slash_command_ref", viaSessionRef(slashCommandQuery)},
		{"end_reason_ref", viaSessionRef(endReasonQuery)},
	} {
		t.Run(q.name, func(t *testing.T) {
			plan := f.planOf(q.sql)
			var sawSource, sawPK bool
			for _, d := range plan {
				t.Log(d)
				if strings.Contains(d, "SCAN") && (strings.Contains(d, " f") || strings.Contains(d, "fields") || strings.Contains(d, " r")) &&
					!strings.Contains(d, "fn") && !strings.Contains(d, "USING") {
					t.Errorf("plan scans records or fields: %q", d)
				}
				if strings.Contains(d, "SEARCH r USING INDEX records_source_type") {
					sawSource = true
				}
				if strings.Contains(d, "SEARCH f USING PRIMARY KEY") {
					sawPK = true
				}
			}
			if !sawSource {
				t.Errorf("plan does not search records by records_source_type: %v", plan)
			}
			if !sawPK {
				t.Errorf("plan does not search fields by its primary key: %v", plan)
			}
		})
	}
}
