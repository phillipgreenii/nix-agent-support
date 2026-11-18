package parser_test

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// A drain queued behind another writer's transaction gives up as soon as stop
// closes, writes nothing, and keeps its files pending for the next pass.
func TestDrainUntilQueuedBehindWriteLockReturnsOnStop(t *testing.T) {
	k := testkit.New(t)
	var ids []int64
	for f := 0; f < 3; f++ {
		ids = append(ids, k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), strings.Repeat(crashLine(f, 0, 0), 3)))
	}
	for _, id := range ids {
		k.Parser.Notify(id)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- k.Store.WithWrite("", func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	stop := make(chan struct{})
	drained := make(chan error, 1)
	go func() { drained <- k.Parser.DrainUntil(stop) }()
	// Give the drain time to queue on the lock; it returns promptly whether it
	// is queued yet or not.
	time.Sleep(100 * time.Millisecond)
	close(stop)

	select {
	case err := <-drained:
		if !errors.Is(err, parser.ErrParseStopped) {
			t.Fatalf("err = %v, want ErrParseStopped", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("DrainUntil still waiting on the write lock one second after stop closed")
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}

	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 0 {
		t.Fatalf("a stopped pass wrote %d records", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM generations WHERE parse_offset>0`); n != 0 {
		t.Fatalf("a stopped pass left %d parse offsets advanced", n)
	}
	// The files are still pending: the next drain parses them whole.
	if err := k.Parser.Drain(); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 18 {
		t.Fatalf("records after the next drain = %d, want 18", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM generations WHERE parse_offset<copy_offset`); n != 0 {
		t.Fatalf("%d generations left unparsed", n)
	}
}

// The per-file preparation loop checks stop: a stop that closed before a file
// is visited ends the pass with nothing parsed and no offset moved.
func TestParseStoredStopsBetweenFiles(t *testing.T) {
	k := testkit.New(t)
	var files []store.File
	for f := 0; f < 20; f++ {
		id := k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), strings.Repeat(crashLine(f, 0, 0), 3))
		file, err := k.Store.File(id)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	stop := make(chan struct{})
	close(stop)
	n, err := k.Parser.ParseStoredForTest(files, stop)
	if !errors.Is(err, parser.ErrParseStopped) {
		t.Fatalf("err = %v, want ErrParseStopped", err)
	}
	if n != 0 {
		t.Fatalf("parsed %d records after stop", n)
	}
	if c := k.Count(`SELECT COUNT(*) FROM generations WHERE parse_offset>0`); c != 0 {
		t.Fatalf("%d parse offsets advanced by a stopped pass", c)
	}
}
