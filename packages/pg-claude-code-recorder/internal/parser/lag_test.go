package parser_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// The maintained lag state against the oracle (Parser.RebuildLag) on one real
// store: lag bytes and progress are equal after every step, compared exactly.
// Lag seconds is not compared (its origin is in memory only; see
// lag_internal_test.go). unknown_kinds is compared by the unknown-kinds
// work, not here.
//
// Every statement `rg -n 'copy_offset|parse_offset' internal` finds that
// changes an offset, and the case that drives it:
//
//	store.go apply TCommit (copier apply)      every append / new generation step
//	parser.go runBatches UPDATE (parse batch)  the parse step (ParseAll)
//	parser.go parseGen UPDATE (ReparseGen)     the reparse step, TestLagReparseGen
//	tail.go ParseTail UPDATE (tail mode)       the tail step, TestLagTailMode
//	query.go Store.SetParseOffset              TestLagSetParseOffset
//	journal replay at open (startup recovery)  TestLagStartupRecovery (the seed)
//
// Settler paths change no offset: the settle records only stage and remove
// copies; the invariants and manifest readers only read the columns.
// Negative controls (hand-applied, recorded on the packet): skipping the hook
// at any one of the five write sites turns this file red.

const lagLine = `{"type":"user","uuid":"u%d","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"line %d"}}` + "\n"

// lagNow is far past every fixture commit time, so lag seconds is positive.
var lagNow = time.Unix(0, 1_700_000_000_000_000_000+3_600_000_000_000)

func newLagKit(t *testing.T) *testkit.Kit {
	t.Helper()
	return testkit.NewWithClock(t, clock.NewFake(lagNow))
}

// lagGauges returns the oracle's two gauges (it has no lag seconds: that origin
// is in memory only).
func lagGauges(k *testkit.Kit) (b, p float64) {
	r, _ := k.Parser.RebuildLag()
	return float64(r.LagBytes), r.Progress
}

func lagEqual(t *testing.T, k *testkit.Kit, l *parser.Lag, label string) {
	t.Helper()
	wb, wp := lagGauges(k)
	gb, _, gp := l.Gauges(lagNow)
	if float64(gb) != wb || gp != wp {
		t.Fatalf("%s: maintained (bytes %d, progress %v) != oracle (%v, %v)", label, gb, gp, wb, wp)
	}
}

func startLag(t *testing.T, k *testkit.Kit) *parser.Lag {
	t.Helper()
	l, err := k.Parser.StartLag()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// addNoParser adds an append file whose source has no parser, with one commit.
func addNoParser(t *testing.T, k *testkit.Kit, at int64, size int64) int64 {
	t.Helper()
	id := k.Store.NewFileID()
	if err := k.Store.Commit(
		&store.Rec{T: store.TFile, At: at, File: id, Source: "no-such-source", Mode: "append", Dev: 1, Ino: uint64(id)},
		&store.Rec{T: store.TPath, At: at, File: id, Path: fmt.Sprintf("/claude/other/%d", id)},
		&store.Rec{T: store.TGen, At: at, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&store.Rec{T: store.TCommit, At: at + 1, File: id, Gen: 1, From: 0, To: size, Size: size, Tail: 1, TailLen: 1},
	); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLagPartialParseKeepsTheOrigin(t *testing.T) {
	k := newLagKit(t)
	l := startLag(t, k)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1))
	k.Append(a, fmt.Sprintf(lagLine, 2, 2)) // second commit: unparsed
	k.Append(a, `{"type":"user","uu`)       // third commit: a torn tail
	lagEqual(t, k, l, "nothing parsed")
	k.Parse() // parses the two whole lines; the torn tail stays
	lagEqual(t, k, l, "partial parse")
	gs := l.FileGenerations(a)
	if len(gs) != 1 || gs[0].ParseOffset >= gs[0].CopyOffset || gs[0].UnparsedSince.IsZero() {
		t.Fatalf("generation snapshot after a partial parse: %+v", gs)
	}
	_, secs, _ := l.Gauges(lagNow)
	if want := float64(lagNow.UnixNano()-gs[0].UnparsedSince.UnixNano()) / 1e9; secs != want {
		t.Fatalf("lag seconds %v is not measured from the unparsed-since origin (%v)", secs, want)
	}
}

func TestLagReparseGen(t *testing.T) {
	k := newLagKit(t)
	l := startLag(t, k)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1))
	k.Parse()
	k.Append(a, fmt.Sprintf(lagLine, 2, 2))
	lagEqual(t, k, l, "before")
	if err := k.Parser.ReparseGen(a, 1); err != nil {
		t.Fatal(err)
	}
	lagEqual(t, k, l, "after reparse")
	if _, s, _ := l.Gauges(lagNow); s != 0 {
		t.Fatalf("a reparsed generation still lags: %v s", s)
	}
}

func TestLagTailMode(t *testing.T) {
	k := newLagKit(t)
	l := startLag(t, k)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1))
	whole := fmt.Sprintf(lagLine, 2, 2)
	k.Append(a, strings.TrimSuffix(whole, "\n")) // a complete final line, no newline
	k.Parse()
	if b, _, _ := l.Gauges(lagNow); b == 0 {
		t.Fatal("the unterminated line should still lag after a normal parse")
	}
	lagEqual(t, k, l, "before tail")
	ok, err := k.Parser.ParseTail(a, 1)
	if err != nil || !ok {
		t.Fatalf("ParseTail = %v, %v", ok, err)
	}
	lagEqual(t, k, l, "after tail")
	if b, _, _ := l.Gauges(lagNow); b != 0 {
		t.Fatalf("lag after the tail parse = %d", b)
	}
}

func TestLagSetParseOffset(t *testing.T) {
	k := newLagKit(t)
	l := startLag(t, k)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1)+fmt.Sprintf(lagLine, 2, 2))
	lagEqual(t, k, l, "before")
	g, err := k.Store.Generation(a, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Store.SetParseOffset(a, 1, g.CopyOffset/2); err != nil {
		t.Fatal(err)
	}
	lagEqual(t, k, l, "set")
	// A failed update (beyond the copy offset) is rejected and changes nothing.
	before := l.Generations()
	if err := k.Store.SetParseOffset(a, 1, g.CopyOffset+1); err == nil {
		t.Fatal("SetParseOffset beyond the copy offset must fail")
	}
	lagEqual(t, k, l, "after a rejected update")
	after := l.Generations()
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("a rejected offset update changed the state: %v -> %v", before, after)
	}
}

func TestLagOldGenerationAndDeletedFileStayCounted(t *testing.T) {
	k := newLagKit(t)
	l := startLag(t, k)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1))
	k.NewGeneration(a, fmt.Sprintf(lagLine, 2, 2)+fmt.Sprintf(lagLine, 3, 3))
	lagEqual(t, k, l, "restart")
	if gs := l.FileGenerations(a); len(gs) != 2 {
		t.Fatalf("both generations are tracked, got %+v", gs)
	}
	if err := k.Store.Commit(&store.Rec{T: store.TTomb, At: 1_700_000_000_000_000_500, File: a, Path: "/claude/gone"}); err != nil {
		t.Fatal(err)
	}
	lagEqual(t, k, l, "deleted with unparsed bytes")
	if b, _, _ := l.Gauges(lagNow); b == 0 {
		t.Fatal("a deleted file with unparsed bytes still counts as lag")
	}
}

func TestLagStartupRecovery(t *testing.T) {
	k := newLagKit(t)
	a := k.Add("example-proj", "example-session-1.jsonl", fmt.Sprintf(lagLine, 1, 1))
	b := k.Add("example-proj", "example-session-2.jsonl", fmt.Sprintf(lagLine, 2, 2))
	k.Parse()
	k.Append(a, fmt.Sprintf(lagLine, 3, 3)) // idle files with unparsed bytes
	k.Append(b, `{"torn`)
	k.NewGeneration(b, fmt.Sprintf(lagLine, 4, 4))
	addNoParser(t, k, 1_700_000_000_000_000_000, 77)
	k.Reopen()
	l := startLag(t, k)
	lagEqual(t, k, l, "seeded after restart")
	// Parse behind copy at start is seeded to "now": the lag bytes are kept and
	// the seconds start from zero (the clock is fixed at lagNow).
	if b, s, _ := l.Gauges(lagNow); b == 0 || s != 0 {
		t.Fatalf("the seed lost the unparsed bytes or did not start from now: bytes %d secs %v", b, s)
	}
	for _, gs := range l.Generations() {
		for _, g := range gs {
			if g.CopyOffset > g.ParseOffset && !g.UnparsedSince.Equal(lagNow) {
				t.Fatalf("seeded origin = %v, want now (%v)", g.UnparsedSince, lagNow)
			}
		}
	}
	// The state keeps tracking after the seed.
	k.Parse()
	lagEqual(t, k, l, "parsed after restart")
}
