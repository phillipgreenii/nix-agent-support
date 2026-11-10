package settle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func (e *env) cancelled() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.sett.cfg.Ctx = ctx
}

func (e *env) running() { e.sett.cfg.Ctx = context.Background() }

// A cancelled root context stops a Tick before its first Conversation; the same
// Tick with a live context settles it.
func TestTickStopsBetweenConversationsWhenContextDone(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.cancelled()
	e.tick()
	if got := e.stage(); got == StageSettled {
		t.Fatalf("a Tick under a cancelled context still evaluated the Conversation (stage %s)", got)
	}
	e.running()
	e.tick()
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage = %s after an uncancelled Tick, want settled", got)
	}
}

// An event-driven Wake examines nothing once the context is done.
func TestWakeStopsBeforeEvaluatingWhenContextDone(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	e.tick() // reconcile seeds the wake state
	e.sett.TakeDirty()
	e.write(e.transcript(), line(9), e.clk.Now(), true)
	e.ingest(e.transcript())
	e.cancelled()
	st, err := e.sett.Wake(e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Examined != 0 {
		t.Fatalf("Wake examined %d Conversations under a cancelled context, want 0", st.Examined)
	}
}

// Removal checks the context between candidates: a cancelled one removes
// nothing and leaves the original file, and the next pass removes it.
func TestRemoveStopsBetweenCandidatesWhenContextDone(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(3, removeAge+24*time.Hour)
	e.cancelled()
	rep, err := e.sett.Remove(RemoveOptions{Sessions: []string{sess}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 0 || !e.exists(e.transcript()) {
		t.Fatalf("removal ran under a cancelled context: %+v", rep)
	}
	e.running()
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("the next pass removed %v, want the Conversation", rep.Removed)
	}
}

// DoctorTick starts no cycle and runs no slice once the context is done.
func TestDoctorTickStopsWhenContextDone(t *testing.T) {
	e := rollEnv(t, 1)
	e.many(5)
	e.cancelled()
	e.tickDoctor()
	if e.sett.loadCursor().Active {
		t.Fatal("DoctorTick opened a cycle under a cancelled context")
	}
	e.running()
	e.tickDoctor()
	if !e.sett.loadCursor().Active {
		t.Fatal("DoctorTick did not open a cycle under a live context (test premise)")
	}
}

// A stuck lsof is killed when its context ends, and Wait does not hang on a
// descendant that kept the pipe open.
func TestLsofHoldersStopsStuckLsofOnCancel(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 300 &\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(dir, "lsof"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := LsofHoldersCtx(ctx, "/nonexistent")
	if err == nil {
		t.Fatal("a killed lsof reported no error: removal would treat it as 'no holder'")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("LsofHoldersCtx took %v after its context ended", d)
	}
}
