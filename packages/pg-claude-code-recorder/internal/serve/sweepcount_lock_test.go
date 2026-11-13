package serve

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// countSweepFiles reads the files table before it takes d.seen.mu: while the
// read is in flight a watcher event's noteSeen still completes.
func TestCountSweepFilesReadsTableWithoutHoldingSeenLock(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	inRead := make(chan struct{})
	release := make(chan struct{})
	d.liveSizes = func() (map[store.FileIdentity]int64, error) {
		close(inRead)
		<-release
		return nil, nil
	}
	done := make(chan struct{})
	go func() {
		d.countSweepFiles([]watcher.FileInfo{{Path: "/x/a.jsonl", Size: 1}})
		close(done)
	}()
	<-inRead
	noted := make(chan struct{})
	go func() {
		d.noteSeen(watcher.Event{Path: "/x/b.jsonl", Info: watcher.FileInfo{Path: "/x/b.jsonl", Size: 2}})
		close(noted)
	}()
	select {
	case <-noted:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("noteSeen blocked while the table read was running")
	}
	close(release)
	<-done
}
