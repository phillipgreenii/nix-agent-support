package parser_test

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const oldGroupBy = `SELECT source,rtype,path,SUM(count) FROM unknown_keys GROUP BY source,rtype,path ORDER BY source,rtype,path`

func oldSummary(t *testing.T, k *testkit.Kit) []parser.UnknownKey {
	t.Helper()
	rows, err := k.Store.Read().QueryContext(context.Background(), oldGroupBy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []parser.UnknownKey
	for rows.Next() {
		var u parser.UnknownKey
		if err := rows.Scan(&u.Source, &u.Type, &u.Path, &u.Count); err != nil {
			t.Fatal(err)
		}
		out = append(out, u)
	}
	return out
}

func requireSummaryMatchesGroupBy(t *testing.T, k *testkit.Kit) []parser.UnknownKey {
	t.Helper()
	sum, err := parser.Summarize(k.Store)
	if err != nil {
		t.Fatal(err)
	}
	want := oldSummary(t, k)
	if len(sum.UnknownKeys) != len(want) {
		t.Fatalf("summary %v, old GROUP BY %v", sum.UnknownKeys, want)
	}
	for i := range want {
		if sum.UnknownKeys[i] != want[i] {
			t.Fatalf("summary %v, old GROUP BY %v", sum.UnknownKeys, want)
		}
	}
	return want
}

func TestSummarizeEqualsGroupByAcrossFilesAndReparse(t *testing.T) {
	k := testkit.New(t)
	f1 := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Add("example-proj", "example-session-2.jsonl", transcriptFixture)
	k.Parse()
	got := requireSummaryMatchesGroupBy(t, k)
	if len(got) != 2 || got[0].Count != 2 || got[1].Count != 2 {
		t.Fatalf("two files should sum each key to 2: %v", got)
	}
	// A reparse replaces one file's rows: the aggregate neither doubles nor loses them.
	if err := k.Parser.ReparseGen(f1, 1); err != nil {
		t.Fatal(err)
	}
	got = requireSummaryMatchesGroupBy(t, k)
	if len(got) != 2 || got[0].Count != 2 || got[1].Count != 2 {
		t.Fatalf("after reparse: %v", got)
	}
}

// Dropping a generation's rows without re-adding them removes a key whose
// every contribution went (no zero rows) and keeps the rest.
func TestClearedGenerationLeavesNoZeroAggregateRows(t *testing.T) {
	k := testkit.New(t)
	f1 := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Add("example-proj", "example-session-2.jsonl", strings.SplitAfter(transcriptFixture, "\n")[0])
	k.Parse()
	// Only the first file carries unknown keys; replace it with a clean body.
	k.NewGeneration(f1, strings.SplitAfter(transcriptFixture, "\n")[0])
	k.Parse()
	requireSummaryMatchesGroupBy(t, k)
	if n := k.Count(`SELECT COUNT(*) FROM unknown_keys_total WHERE count<=0`); n != 0 {
		t.Fatalf("%d non-positive aggregate rows", n)
	}
}

func TestSummarizeDoesNotScanThePerFileTable(t *testing.T) {
	k := testkit.New(t)
	rows, err := k.Store.Read().QueryContext(context.Background(), `EXPLAIN QUERY PLAN SELECT source,rtype,path,count FROM unknown_keys_total ORDER BY source,rtype,path`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if strings.Contains(plan.String(), "unknown_keys ") || strings.HasSuffix(strings.TrimSpace(plan.String()), "unknown_keys") ||
		strings.Contains(plan.String(), "unknown_keys\n") || strings.Contains(plan.String(), "sqlite_autoindex_unknown_keys_1") {
		t.Fatalf("the summary plan touches the per-file table:\n%s", plan.String())
	}
	if !strings.Contains(plan.String(), "unknown_keys_total") {
		t.Fatalf("the summary plan does not read the aggregate:\n%s", plan.String())
	}
}
