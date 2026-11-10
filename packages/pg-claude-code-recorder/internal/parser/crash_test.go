package parser_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

func crashLine(f, r, i int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"u%d-%d-%d","sessionId":"example-session-%d","timestamp":"2026-10-29T20:00:00Z","message":{"id":"m%d-%d-%d","model":"example-model-a","role":"assistant","usage":{"output_tokens":5},"content":[{"type":"text","text":"some words %d"}]}}`+"\n", f, r, i, f, f, r, i, r)
}

// twoPhase adds four files and parses them (phase one, then afterOne), then
// grows each file over two rounds with a parse after each (phase two).
func twoPhase(k *testkit.Kit, afterOne func()) {
	ids := make([]int64, 4)
	for f := range ids {
		ids[f] = k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), crashLine(f, 0, 0))
		k.Parser.Notify(ids[f])
	}
	if err := k.Parser.Drain(); err != nil {
		k.T.Fatal(err)
	}
	if afterOne != nil {
		afterOne()
	}
	for r := 1; r <= 2; r++ {
		for f, id := range ids {
			k.Append(id, crashLine(f, r, 1)+crashLine(f, r, 2))
			k.Parser.Notify(id)
		}
		if err := k.Parser.Drain(); err != nil {
			k.T.Fatal(err)
		}
	}
}

// TestRolledBackIndexTailIsRebuiltFromTheJournalAndTheCopies models what
// synchronous=NORMAL allows after a power loss: the database file is an OLDER
// image (the last commits, capture side and parse side, are gone) while the
// journal and the copies hold everything. Reopening replays the journal, the
// capture-side state is whole again, and a parse resumes from the older stored
// offsets, ending with the same records as a run that lost nothing.
func TestRolledBackIndexTailIsRebuiltFromTheJournalAndTheCopies(t *testing.T) {
	ref := testkit.New(t)
	twoPhase(ref, nil)
	ref.Parse()
	want := indexDump(t, ref)

	k := testkit.New(t)
	var old []byte
	var seqThen int64
	var parsedThen map[int64]int64
	twoPhase(k, func() {
		if err := k.Store.Barrier(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(k.Dir, store.DBName))
		if err != nil {
			t.Fatal(err)
		}
		old, seqThen = b, k.Store.LastSeq()
		parsedThen = map[int64]int64{}
		gens, _ := k.Store.AllGenerations()
		for _, g := range gens {
			parsedThen[g.File] = g.ParseOffset
		}
	})
	dir := k.Dir
	_ = k.Store.Close()
	// The power loss: only the older database image survives next to the journal
	// and the copies.
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dir, store.DBName+suf))
	}
	if err := os.WriteFile(filepath.Join(dir, store.DBName), old, 0o600); err != nil {
		t.Fatal(err)
	}
	k2 := testkit.NewInDir(t, dir, nil)
	if k2.Store.LastSeq() <= seqThen {
		t.Fatalf("replay did not advance last_seq past the old image (%d <= %d)", k2.Store.LastSeq(), seqThen)
	}
	gens, err := k2.Store.AllGenerations()
	if err != nil || len(gens) != 4 {
		t.Fatalf("generations after replay: %d, %v", len(gens), err)
	}
	for _, g := range gens {
		st, err := os.Stat(k2.Store.CopyPath(g.File, g.Gen))
		if err != nil {
			t.Fatal(err)
		}
		if g.CopyOffset != st.Size() {
			t.Fatalf("file %d copy offset %d after replay, copy holds %d bytes", g.File, g.CopyOffset, st.Size())
		}
		if g.ParseOffset != parsedThen[g.File] {
			t.Fatalf("file %d parse offset %d, want the old image's %d (the parse tail is rolled back)", g.File, g.ParseOffset, parsedThen[g.File])
		}
	}
	k2.Parse()
	if got := indexDump(t, k2); got != want {
		t.Fatalf("records after the rolled-back tail differ from a clean run\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestCoalesceWaitsForTheBurstThenReturns(t *testing.T) {
	k := testkit.New(t)
	p := parser.New(parser.Config{Store: k.Store, GroupWindow: 60 * time.Millisecond, GroupMaxWait: 400 * time.Millisecond})
	start := time.Now()
	stop := make(chan struct{})
	done := make(chan bool)
	go func() { done <- p.Coalesce(stop) }()
	// A wake-up every 30 ms keeps the quiet window open until the burst ends.
	for i := 0; i < 5; i++ {
		// 30 ms is half the 60 ms quiet window, so each wake-up lands inside it.
		// Coalesce uses real timers (parser.Config has no clock for them), so
		// these windows are real time and are the spec of the bounds below;
		// left as is (0.66 s plain, 0.73 s -race measured).
		time.Sleep(30 * time.Millisecond)
		p.Notify(int64(i + 1))
	}
	if !<-done {
		t.Fatal("Coalesce reported a stop")
	}
	if el := time.Since(start); el < 150*time.Millisecond || el > 390*time.Millisecond {
		t.Fatalf("Coalesce returned after %v, want after the burst (about 150 ms + the 60 ms window)", el)
	}
	// An endless burst is cut at the maximum wait.
	go func() { done <- p.Coalesce(stop) }()
	start = time.Now()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for ok := false; !ok; {
		select {
		case ok = <-done:
		case <-tick.C:
			p.Notify(1)
		}
	}
	if el := time.Since(start); el < 380*time.Millisecond || el > 900*time.Millisecond {
		t.Fatalf("a burst that never ends was cut after %v, want about the 400 ms maximum", el)
	}
	// Stop wins.
	go func() { done <- p.Coalesce(stop) }()
	close(stop)
	if <-done {
		t.Fatal("Coalesce did not report the stop")
	}
}

func TestGroupCommitParsesManyFilesInOneTransactionAndBumpsParseSeqOnce(t *testing.T) {
	k := testkit.New(t)
	var ids []int64
	for f := 0; f < 10; f++ {
		ids = append(ids, k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), strings.Repeat(crashLine(f, 0, 0), 3)))
	}
	wrote := 0
	k.Store.OnWrite(func() { wrote++ })
	seq := k.Count(`SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'),0)`)
	for _, id := range ids {
		k.Parser.Notify(id)
	}
	if err := k.Parser.Drain(); err != nil {
		t.Fatal(err)
	}
	if wrote != 1 {
		t.Fatalf("a drain of 10 dirty files committed %d transactions, want 1", wrote)
	}
	if got := k.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`); got != seq+1 {
		t.Fatalf("parse_seq moved %d -> %d, want one bump per batch", seq, got)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 60 {
		t.Fatalf("records = %d, want 60 (30 lines, 2 records each)", n)
	}
	// Nothing new: no transaction and no bump.
	for _, id := range ids {
		k.Parser.Notify(id)
	}
	if err := k.Parser.Drain(); err != nil {
		t.Fatal(err)
	}
	if wrote != 1 {
		t.Fatalf("a drain with nothing to parse committed (%d)", wrote)
	}
}

func TestGroupCommitIsBoundedByRecordsAndStillFinishes(t *testing.T) {
	k := testkit.New(t)
	var ids []int64
	for f := 0; f < 10; f++ {
		ids = append(ids, k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), strings.Repeat(crashLine(f, 0, 0), 3)))
	}
	p := parser.New(parser.Config{Store: k.Store, Sources: sources.All(), ClaudeDir: testkit.ClaudeDir, BatchRecords: 5, ChunkBytes: 600})
	wrote := 0
	k.Store.OnWrite(func() { wrote++ })
	for _, id := range ids {
		p.Notify(id)
	}
	if err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	if wrote < 3 || wrote > 30 {
		t.Fatalf("a record budget of 5 over 60 records committed %d transactions, want several but far fewer than one per line", wrote)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 60 {
		t.Fatalf("records = %d, want 60 (30 lines, 2 records each)", n)
	}
	if got := k.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`); got != wrote {
		t.Fatalf("parse_seq = %d, want one bump per transaction (%d)", got, wrote)
	}
}
