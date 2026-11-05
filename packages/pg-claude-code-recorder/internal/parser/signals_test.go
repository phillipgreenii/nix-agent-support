package parser_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

func TestSignalSeriesAreRegisteredAtZeroBeforeAnyLine(t *testing.T) {
	k := testkit.New(t)
	k.Parse()
	counters, _, _ := k.Obs.Reg.Snapshot()
	var bad, unk int
	for key, v := range counters {
		switch {
		case strings.HasPrefix(key, parser.MBadLines+"{source="):
			bad++
			if v != 0 {
				t.Fatalf("%s = %v at start", key, v)
			}
		case strings.HasPrefix(key, parser.MUnknownKeys+"{"):
			unk++
			if !strings.Contains(key, "source=") || !strings.Contains(key, "type=") || v != 0 {
				t.Fatalf("%s = %v at start", key, v)
			}
		case key == parser.MBadLines || key == parser.MUnknownKeys:
			t.Fatalf("unlabelled series %s", key)
		}
	}
	if bad == 0 || unk != bad*(len(parser.KnownRecordTypes)+1) {
		t.Fatalf("registered %d bad_lines and %d unknown_keys series", bad, unk)
	}
}

func TestBadLinesAreCountedBySource(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	counters, _, _ := k.Obs.Reg.Snapshot()
	var total float64
	for key, v := range counters {
		if strings.HasPrefix(key, parser.MBadLines+"{source=") {
			total += v
		}
	}
	if total != 1 {
		t.Fatalf("bad_lines_total sums to %v, want 1 (counters %v)", total, counters)
	}
}

func TestUnknownKeysCountFirstSeenDistinctTuplesOnly(t *testing.T) {
	k := testkit.New(t)
	l := func(extra string) string {
		return `{"type":"user","uuid":"u","sessionId":"example-session-1","message":{"role":"user","content":"x"},` + extra + "}\n"
	}
	sum := func() (user, other float64) {
		counters, _, _ := k.Obs.Reg.Snapshot()
		for key, v := range counters {
			if !strings.HasPrefix(key, parser.MUnknownKeys+"{") {
				continue
			}
			switch {
			case strings.Contains(key, "type=user}"):
				user += v
			case strings.Contains(key, "type=other}"):
				other += v
			}
		}
		return
	}
	// 500 lines repeating one unknown key: one new distinct tuple.
	f := k.Add("example-proj", "example-session-1.jsonl", strings.Repeat(l(`"futureKey":1`), 500))
	k.Parse()
	if u, _ := sum(); u != 1 {
		t.Fatalf("500 occurrences of one key counted %v, want 1", u)
	}
	// The same key again in a later batch adds nothing, but the stored count grows.
	k.Append(f, strings.Repeat(l(`"futureKey":2`), 50))
	k.Parse()
	if u, _ := sum(); u != 1 {
		t.Fatalf("a repeated key moved the counter to %v", u)
	}
	if n := k.Count(`SELECT count FROM unknown_keys_total WHERE path='futureKey'`); n != 550 {
		t.Fatalf("stored occurrences %d, want 550", n)
	}
	// A new key counts once; a record type outside the known set goes to other.
	k.Append(f, l(`"secondKey":1`)+`{"type":"brand-new-kind","uuid":"z","sessionId":"example-session-1","novel":1}`+"\n")
	k.Parse()
	u, o := sum()
	if u != 2 || o != 1 {
		t.Fatalf("user %v other %v, want 2 and 1", u, o)
	}
}
