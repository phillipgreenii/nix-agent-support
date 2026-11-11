package corpus

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// foldRig is a bootstrapped reader over n transcripts of two priced lines each.
func foldRig(t *testing.T, n int) (*reader, *client.Client, *corpustest.Env) {
	t.Helper()
	e := corpustest.Start(t)
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("s%d", i)
		e.Register(100+i, sid, cwd, "", true)
		e.Put(corpustest.TranscriptRel(cwd, sid),
			assistantLine(sid, time.Duration(i)*time.Minute, "claude-sonnet-4", 100, 10),
			assistantLine(sid, time.Duration(i)*time.Minute+time.Second, "claude-sonnet-4", 200, 20))
	}
	r := newReader(e.Facts)
	ctx := context.Background()
	c, err := e.Facts.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bootstrap(ctx, c, t0.Add(-48*time.Hour), nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := r.resumeInventory(ctx, c); err != nil {
		t.Fatal(err)
	}
	return r, c, e
}

func recordCounts(r *reader) []string {
	var out []string
	for ref, fs := range r.files {
		if fs.kind == kindTranscript {
			out = append(out, fmt.Sprintf("%s:%d:dirty=%v", ref, len(fs.records), fs.dirty))
		}
	}
	sort.Strings(out)
	return out
}

// An expired context during foldDirty must leave the file dirty, and a later
// healthy fold must read all its lines.
func TestFoldDirty_ExpiredContextThenHealthy(t *testing.T) {
	r, c, _ := foldRig(t, 1)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.foldDirty(expired, c); err == nil {
		t.Fatal("want the context error")
	}
	for _, fs := range r.files {
		if fs.kind == kindTranscript && (!fs.dirty || len(fs.records) != 0) {
			t.Fatalf("after expiry: dirty=%v records=%d, want dirty and none", fs.dirty, len(fs.records))
		}
	}
	if r.dirtyFiles() == 0 {
		t.Fatal("dirtyFiles must count the file")
	}
	if err := r.foldDirty(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, fs := range r.files {
		if fs.kind == kindTranscript && (fs.dirty || len(fs.records) != 2) {
			t.Fatalf("after healthy fold: dirty=%v records=%d, want clean and 2", fs.dirty, len(fs.records))
		}
	}
}

// A deadline that expires after the first file stops the pass; the next pass
// reads the rest and converges to what a single healthy pass folds.
func TestFoldDirty_DeadlineMidScanConverges(t *testing.T) {
	r, c, e := foldRig(t, 4)
	reads := 0
	r.readGate = func(*fileState) error {
		reads++
		if reads == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := r.foldDirty(context.Background(), c); err == nil {
		t.Fatal("want the deadline error")
	}
	if reads != 2 {
		t.Fatalf("pass must stop at the first expiry, reads=%d", reads)
	}
	if got := r.dirtyFiles(); got != 3 {
		t.Fatalf("dirty after expiry = %d, want 3", got)
	}
	r.readGate = nil
	if err := r.foldDirty(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ref, _, _ := foldRigFresh(t, e)
	if fmt.Sprint(recordCounts(r)) != fmt.Sprint(recordCounts(ref)) {
		t.Fatalf("converged %v != single healthy %v", recordCounts(r), recordCounts(ref))
	}
}

// foldRigFresh folds the same recorder with a new reader in one healthy pass.
func foldRigFresh(t *testing.T, e *corpustest.Env) (*reader, *client.Client, *corpustest.Env) {
	t.Helper()
	r := newReader(e.Facts)
	ctx := context.Background()
	c, err := e.Facts.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bootstrap(ctx, c, t0.Add(-48*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.resumeInventory(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := r.foldDirty(ctx, c); err != nil {
		t.Fatal(err)
	}
	return r, c, e
}

// A query timeout or a busy recorder keeps the file dirty, stops the pass, and
// the next pass folds it.
func TestFoldDirty_RetryableErrorsKeepFilesDirty(t *testing.T) {
	for name, injected := range map[string]error{"query-timeout": client.ErrQueryTimeout, "busy": client.ErrBusy} {
		t.Run(name, func(t *testing.T) {
			r, c, _ := foldRig(t, 3)
			reads := 0
			r.readGate = func(*fileState) error {
				reads++
				return injected
			}
			if err := r.foldDirty(context.Background(), c); err == nil {
				t.Fatal("want the error")
			}
			if reads != 1 || r.dirtyFiles() != 3 {
				t.Fatalf("reads=%d dirty=%d, want 1 and 3", reads, r.dirtyFiles())
			}
			r.readGate = nil
			if err := r.foldDirty(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if r.dirtyFiles() != 0 {
				t.Fatalf("still dirty: %v", recordCounts(r))
			}
			for _, fs := range r.files {
				if fs.kind == kindTranscript && len(fs.records) != 2 {
					t.Fatalf("records=%d, want 2", len(fs.records))
				}
			}
		})
	}
}
