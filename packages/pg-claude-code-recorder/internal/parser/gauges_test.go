package parser_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// oracleGauges is the parse-lag computation as it was written over every
// generation row (AllGenerations), kept here to pin the aggregate version.
func oracleGauges(t *testing.T, k *testkit.Kit, now int64) (lagBytes, lagSecs, progress float64) {
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
	var ls float64
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
			cc, _ := k.Store.CopyCommits(g.File)
			for i := len(cc) - 1; i >= 0; i-- {
				c := cc[i]
				if c.Gen == g.Gen && c.From <= g.ParseOffset && g.ParseOffset < c.To {
					if d := float64(now-c.SeenNS) / 1e9; d > ls {
						ls = d
					}
					break
				}
			}
		}
	}
	prog := 1.0
	if copied > 0 {
		prog = float64(parsed) / float64(copied)
	}
	return float64(lb), ls, prog
}

func TestUpdateGaugesMatchesTheAllGenerationsComputation(t *testing.T) {
	now := time.Unix(0, 1_700_000_000_000_000_000+3_600_000_000_000)
	k := testkit.NewWithClock(t, clock.NewFake(now))
	a := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Add("example-proj", "history.jsonl", `{"display":"one","timestamp":1761768000000}`+"\n")
	k.Parse()
	// Fully parsed: nothing lags.
	k.Parser.UpdateGauges()
	check := func(label string) {
		t.Helper()
		wb, ws, wp := oracleGauges(t, k, now.UnixNano())
		reg := k.Obs.Reg
		for name, want := range map[string]float64{
			parser.MParseLagBytes: wb, parser.MParseLagSecs: ws, parser.MParseProgress: wp,
		} {
			got, ok := reg.Gauge(name, nil)
			if !ok || got != want {
				t.Errorf("%s: %s = %v (set %v), want %v", label, name, got, ok, want)
			}
		}
	}
	check("parsed")
	// Unparsed tail on one file, a second generation with a lagging tail, and
	// a third file never parsed.
	k.Append(a, transcriptFixture)
	k.NewGeneration(a, transcriptFixture)
	k.Add("example-proj", "other.jsonl", transcriptFixture)
	k.Parser.UpdateGauges()
	if got, _ := k.Obs.Reg.Gauge(parser.MParseLagBytes, nil); got <= 0 {
		t.Fatalf("fixture has no lag: %v", got)
	}
	check("lagging")
	k.Parse()
	k.Append(a, transcriptFixture)
	k.Parser.UpdateGauges()
	check("lagging again")
}
