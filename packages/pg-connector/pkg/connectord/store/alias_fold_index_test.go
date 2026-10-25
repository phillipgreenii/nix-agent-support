package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ladder rung 8, alias folded-ref index: an index on lower(ref), so the
// case-insensitive alias lookups (AliasByRefFold, AliasesByRefFold) search the
// index instead of scanning the table.

const aliasFoldVersion = 8

// queryPlan returns the detail column of EXPLAIN QUERY PLAN for query, joined by
// newlines.
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

// assertNoAliasScan fails when the plan reads the alias table by a full scan or
// does not name the folded-ref index.
func assertNoAliasScan(t *testing.T, plan string) {
	t.Helper()
	if !strings.Contains(plan, "USING INDEX alias_ref_fold") {
		t.Errorf("plan does not search alias_ref_fold:\n%s", plan)
	}
	for _, line := range strings.Split(plan, "\n") {
		if strings.HasPrefix(line, "SCAN alias") || strings.HasPrefix(line, "SCAN TABLE alias") {
			t.Errorf("plan scans the alias table (%q):\n%s", line, plan)
		}
	}
}

func TestLadderRung8IsNamedAndLast(t *testing.T) {
	ladder := BuiltinMigrations()
	if len(ladder) != aliasFoldVersion || SchemaVersion != aliasFoldVersion {
		t.Fatalf("the ladder has %d rungs and SchemaVersion = %d, want %d of each", len(ladder), SchemaVersion, aliasFoldVersion)
	}
	if last := ladder[aliasFoldVersion-1]; last.Version != aliasFoldVersion || last.Name != "alias folded-ref index" {
		t.Errorf("last rung = %d %q, want %d %q", last.Version, last.Name, aliasFoldVersion, "alias folded-ref index")
	}
	if want := "CREATE INDEX alias_ref_fold ON alias (lower(ref));"; !strings.Contains(schemaV8, want) {
		t.Errorf("schemaV8 lacks %q", want)
	}
}

// TestAliasFoldLookupsSearchTheIndexAndDoNotScan is the regression guard for
// pg2-266yl: the SQL the two fold lookups really run is explained against a
// populated alias table, and neither plan scans it.
func TestAliasFoldLookupsSearchTheIndexAndDoNotScan(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	for i := range 200 {
		if err := s.PutAlias(ctx, AliasRow{
			NodeID: fmt.Sprintf("N%d", i), Ref: fmt.Sprintf("Example/Item#%d", i), SeenAt: time.UnixMilli(int64(i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertNoAliasScan(t, queryPlan(t, s, aliasGetSQL(aliasFoldedRefExpr), "example/item#7"))
	for _, n := range []int{1, 2, 100, aliasFoldChunk} {
		args := make([]any, n)
		for i := range args {
			args[i] = fmt.Sprintf("example/item#%d", i)
		}
		t.Run(fmt.Sprintf("in-list-of-%d", n), func(t *testing.T) {
			assertNoAliasScan(t, queryPlan(t, s, aliasesByFoldedRefsSQL(n), args...))
		})
	}
}

// TestAliasFoldPlanWithoutTheIndexScans is the negative control: with the index
// dropped the same statements scan the table, so the plan check above can fail.
func TestAliasFoldPlanWithoutTheIndexScans(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if _, err := s.DB().Exec(`DROP INDEX alias_ref_fold`); err != nil {
		t.Fatal(err)
	}
	plan := queryPlan(t, s, aliasesByFoldedRefsSQL(3), "a", "b", "c")
	if !strings.Contains(plan, "SCAN") || strings.Contains(plan, "alias_ref_fold") {
		t.Errorf("plan without the index = %q, want a scan", plan)
	}
}

// TestOpenFromOlderFixturesAddsTheAliasFoldIndexAndKeepsRows: every fixture below
// version 8 migrates to the top with the index present, the alias rows
// unchanged, and the fold lookups still answering case-insensitively.
func TestOpenFromOlderFixturesAddsTheAliasFoldIndexAndKeepsRows(t *testing.T) {
	ctx := context.Background()
	ran := 0
	for v, fx := range fixtures(t) {
		if v >= aliasFoldVersion {
			continue
		}
		ran++
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			path := dbPath(t)
			copyFile(t, fx, path)
			s, res := mustOpen(t, path, fastOpts())
			if res.Outcome != OutcomeMigrated || res.ToVersion != SchemaVersion {
				t.Fatalf("result = %+v, want migrated to %d", res, SchemaVersion)
			}
			var idx int
			if err := s.DB().QueryRow(
				`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'alias_ref_fold'`,
			).Scan(&idx); err != nil || idx != 1 {
				t.Errorf("alias_ref_fold index count = %d err=%v, want 1", idx, err)
			}
			// The fixture's one alias row, read raw, is what both fold lookups must
			// answer for the upper-cased reference.
			var node, ref string
			if err := s.DB().QueryRow(`SELECT node_id, ref FROM alias`).Scan(&node, &ref); err != nil {
				t.Fatal(err)
			}
			a, ok, err := s.AliasByRefFold(ctx, strings.ToUpper(ref))
			if err != nil || !ok || a.NodeID != node || a.Ref != ref {
				t.Errorf("AliasByRefFold = %+v ok=%v err=%v, want node %q as %q", a, ok, err, node, ref)
			}
			all, err := s.AliasesByRefFold(ctx, []string{strings.ToUpper(ref), "absent/item#0"})
			if err != nil || len(all) != 1 || all[strings.ToLower(ref)].NodeID != node {
				t.Errorf("AliasesByRefFold = %+v err=%v, want only node %q", all, err, node)
			}
			assertNoAliasScan(t, queryPlan(t, s, aliasesByFoldedRefsSQL(2), "a", "b"))
		})
	}
	if ran == 0 {
		t.Fatal("no fixture below version 8 was exercised")
	}
}

// BenchmarkAliasesByRefFold times the batched fold lookup over an alias table of
// aliasBenchRows rows with an aliasBenchBatch-reference batch (the shape
// groupsvc.members sends), with the alias_ref_fold index in place and with it
// dropped (the scan the lookup did before rung 8):
//
//	go test ./pkg/connectord/store -run '^$' -bench AliasesByRefFold
func BenchmarkAliasesByRefFold(b *testing.B) {
	const aliasBenchRows, aliasBenchBatch = 2000, 100
	for _, indexed := range []bool{true, false} {
		name := "indexed"
		if !indexed {
			name = "scan"
		}
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			s, _, err := Open(filepath.Join(b.TempDir(), "store.db"), fastOpts())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close() })
			for i := range aliasBenchRows {
				if err := s.PutAlias(ctx, AliasRow{
					NodeID: fmt.Sprintf("N%d", i), Ref: fmt.Sprintf("Example/Item#%d", i), SeenAt: time.UnixMilli(int64(i)),
				}); err != nil {
					b.Fatal(err)
				}
			}
			if !indexed {
				if _, err := s.DB().Exec(`DROP INDEX alias_ref_fold`); err != nil {
					b.Fatal(err)
				}
			}
			refs := make([]string, aliasBenchBatch)
			for i := range refs {
				refs[i] = fmt.Sprintf("EXAMPLE/ITEM#%d", i*(aliasBenchRows/aliasBenchBatch))
			}
			b.ResetTimer()
			for range b.N {
				got, err := s.AliasesByRefFold(ctx, refs)
				if err != nil || len(got) != aliasBenchBatch {
					b.Fatalf("got %d aliases, err %v, want %d", len(got), err, aliasBenchBatch)
				}
			}
		})
	}
}
