package parser_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// oracleGauges is the parse-lag computation as it was written over every
// generation row (AllGenerations), kept here to pin the aggregate version. It
// has no lag seconds: that figure comes from an in-memory origin.
func oracleGauges(t *testing.T, k *testkit.Kit) (lagBytes, progress float64) {
	t.Helper()
	files, err := k.Store.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	gens, err := k.Store.AllGenerations()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]string{}
	for _, f := range files {
		byID[f.ID] = f.Source
	}
	var lb, copied, parsed int64
	for _, g := range gens {
		src, ok := byID[g.File]
		if !ok || !k.Parser.HasSource(src) {
			continue
		}
		copied += g.CopyOffset
		parsed += g.ParseOffset
		if lag := g.CopyOffset - g.ParseOffset; lag > 0 {
			if lag > lb {
				lb = lag
			}
		}
	}
	prog := 1.0
	if copied > 0 {
		prog = float64(parsed) / float64(copied)
	}
	return float64(lb), prog
}

func TestRebuildLagMatchesTheAllGenerationsComputation(t *testing.T) {
	now := time.Unix(0, 1_700_000_000_000_000_000+3_600_000_000_000)
	k := testkit.NewWithClock(t, clock.NewFake(now))
	a := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Add("example-proj", "history.jsonl", `{"display":"one","timestamp":1761768000000}`+"\n")
	k.Parse()
	// Fully parsed: nothing lags.
	check := func(label string) {
		t.Helper()
		wb, wp := oracleGauges(t, k)
		r, ok := k.Parser.RebuildLag()
		if !ok {
			t.Fatalf("%s: rebuild failed", label)
		}
		for name, c := range map[string][2]float64{
			parser.MParseLagBytes: {float64(r.LagBytes), wb}, parser.MParseProgress: {r.Progress, wp},
		} {
			if c[0] != c[1] {
				t.Errorf("%s: %s = %v, want %v", label, name, c[0], c[1])
			}
		}
	}
	check("parsed")
	// Unparsed tail on one file, a second generation with a lagging tail, and
	// a third file never parsed.
	k.Append(a, transcriptFixture)
	k.NewGeneration(a, transcriptFixture)
	k.Add("example-proj", "other.jsonl", transcriptFixture)
	if got, _ := k.Parser.RebuildLag(); got.LagBytes <= 0 {
		t.Fatalf("fixture has no lag: %v", got)
	}
	check("lagging")
	k.Parse()
	k.Append(a, transcriptFixture)
	check("lagging again")
}
