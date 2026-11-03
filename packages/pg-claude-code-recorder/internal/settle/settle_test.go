package settle

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func TestSessionOf(t *testing.T) {
	for rel, want := range map[string]string{
		"projects/p/" + sess + ".jsonl":                   sess,
		"projects/p/" + sess + "/subagents/agent-1.jsonl": sess,
		"projects/p/" + sess + "/tool-results/x.txt":      sess,
		"projects/p/" + sess + ".status.jsonl":            sess,
		"projects/p/memory/notes.md":                      "",
		"history.jsonl":                                   "",
		"sessions/123.json":                               "",
		"projects/p/sessions-index.json":                  "",
		"projects/p/not-a-uuid.jsonl":                     "",
	} {
		got, ok := SessionOf(rel)
		if got != want || ok != (want != "") {
			t.Errorf("SessionOf(%q) = %q,%v want %q", rel, got, ok, want)
		}
	}
}

// The boundary: 23:59 of quiet settles nothing, 24:00 does. The timestamp read
// is the modification time of the original file (the newest among the files).
func TestByteSettleBoundaryAndIdenticalReads(t *testing.T) {
	e := newEnv(t, false)
	content := e.seed(5, 24*time.Hour-time.Minute) // mtime = now - 23:59
	e.tick()
	if got := e.stage(); got != StageQuiet {
		t.Fatalf("23:59 quiet: stage %s", got)
	}
	file := e.fileID()
	before := e.raw(file, len(content))
	e.clk.Advance(time.Minute) // now 24:00 after the mtime
	e.tick()
	if got := e.stage(); got != StageSettled {
		t.Fatalf("24:00 quiet: stage %s", got)
	}
	f, _ := e.st.File(file)
	if _, err := os.Stat(e.st.CopyPath(file, f.CurGen)); !os.IsNotExist(err) {
		t.Fatalf("uncompressed copy bytes remain: %v", err)
	}
	if after := e.raw(file, len(content)); after != before || after != content {
		t.Fatal("raw bytes differ after settle")
	}
	m, ok, _ := e.st.SettledOf(sess)
	if !ok || len(m.Manifest) == 0 || !strings.Contains(string(m.Manifest), sess) {
		t.Fatalf("no manifest: %v %s", ok, m.Manifest)
	}
	if _, err := os.Stat(e.st.SegDir(sess) + "/manifest.json"); err != nil {
		t.Fatal(err)
	}
}

func TestLiveProcessBlocks(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.live[sess] = true
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatal("a live Process must block settling")
	}
	e.live[sess] = false
	e.tick()
	if e.stage() != StageSettled {
		t.Fatal("not settled after the Process left")
	}
}

func TestCheckClausesFailOnTheirOwn(t *testing.T) {
	cases := map[string]struct {
		prep   func(e *env)
		reason string
	}{
		"copy_offset": {func(e *env) {
			e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true) // not ingested
		}, ReasonCopyOffset},
		"parse_behind": {func(e *env) {
			e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true)
			if _, err := e.cp.Ingest(e.path(e.transcript())); err != nil { // copied, not parsed
				t.Fatal(err)
			}
		}, ReasonParseBehind},
		"copy_tail": {func(e *env) {
			p := e.path(e.transcript())
			b, _ := os.ReadFile(p)
			b[len(b)-3] ^= 0x01 // same size, same newline, different tail
			_ = os.WriteFile(p, b, 0o644)
			_ = os.Chtimes(p, e.clk.Now().Add(-48*time.Hour), e.clk.Now().Add(-48*time.Hour))
		}, ReasonCopyTail},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, false)
			e.skipInv = name == "copy_tail"
			e.seed(3, 48*time.Hour)
			c.prep(e)
			e.tick()
			if got := e.heldReason(); got != c.reason {
				t.Fatalf("held reason %s, want %s", got, c.reason)
			}
		})
	}
}

func TestUnparsedFileHolds(t *testing.T) {
	e := newEnv(t, false)
	e.write(e.transcript(), line(1), e.clk.Now().Add(-48*time.Hour), false)
	e.ingest(e.transcript())
	e.prs.Drain() //nolint:errcheck
	// Drain parsed it; drop the parse record to model a never-parsed file.
	if err := e.st.WithWrite("", func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM parse_state`); return err }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.heldReason(); got != ReasonUnparsed {
		t.Fatalf("held reason %s", got)
	}
}

func TestHeldRecheckIntervalAndRelease(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true)
	e.tick()
	e.heldReason()
	// Fixed: the copier catches up and the parse follows. Not checked until held.recheck.
	e.ingest(e.transcript())
	e.clk.Advance(30 * time.Minute)
	e.tick()
	if e.stage() != StageHeld {
		t.Fatal("rechecked before held.recheck")
	}
	e.clk.Advance(31 * time.Minute)
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage after recheck = %s", e.stage())
	}
	// Settled to Live.
	e.live[sess] = true
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatal("a returning Process must clear the settled stage")
	}
	events := e.obs.EventsNamed(obs.EvStageChanged)
	if len(events) == 0 {
		t.Fatal("no stage_changed events")
	}
}

func TestOnDemandRecheck(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true)
	e.tick()
	e.heldReason()
	e.ingest(e.transcript())
	out, err := e.sett.Recheck(sess)
	if err != nil || out.Stage != StageSettled {
		t.Fatalf("recheck: %v %+v", err, out)
	}
}

func TestHeldToLive(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true)
	e.tick()
	e.heldReason()
	e.live[sess] = true
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatal("held stage survived a live Process")
	}
}

func TestNewWritesReturnToQuietAndTailIsAnotherSegment(t *testing.T) {
	e := newEnv(t, false)
	first := e.seed(4, 48*time.Hour)
	e.tick()
	if e.stage() != StageSettled {
		t.Fatal("not settled")
	}
	file := e.fileID()
	extra := line(7) + line(8)
	e.write(e.transcript(), extra, e.clk.Now(), true)
	e.ingest(e.transcript())
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatalf("stage after new writes = %s", e.stage())
	}
	if got := e.raw(file, len(first+extra)); got != first+extra {
		t.Fatal("bytes differ with an uncompressed tail after the archived part")
	}
	e.clk.Advance(25 * time.Hour)
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage = %s", e.stage())
	}
	if got := e.raw(file, len(first+extra)); got != first+extra {
		t.Fatal("bytes differ after the second segment")
	}
	rs, _ := e.st.Ranges(file, 1)
	if len(rs) != 2 || rs[0].Seg == rs[1].Seg {
		t.Fatalf("want two segments, got %+v", rs)
	}
	m, _, _ := e.st.SettledOf(sess)
	if m.Seq != 2 {
		t.Fatalf("seq %d", m.Seq)
	}
}

func TestTornTail(t *testing.T) {
	torn := `{"type":"user","uuid":"t1","sessionId":"` + sess + `","timestamp":"2026-10-29T20:00:30Z","message":{"role":"user","content":"torn but whole"}}`
	t.Run("quiet parses a valid tail", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(2, 48*time.Hour)
		e.write(e.transcript(), torn, e.clk.Now().Add(-48*time.Hour), true)
		e.ingest(e.transcript())
		e.tick()
		if e.stage() != StageSettled {
			t.Fatalf("stage %s", e.stage())
		}
		if n, _ := e.st.TornTails(); len(n) != 0 {
			t.Fatal("a parsed tail left a fact")
		}
	})
	t.Run("quiet invalid tail records a fact and the check passes", func(t *testing.T) {
		e := newEnv(t, false)
		first := e.seed(2, 48*time.Hour)
		partial := `{"type":"user","uuid":"t2","sess`
		e.write(e.transcript(), partial, e.clk.Now().Add(-48*time.Hour), true)
		e.ingest(e.transcript())
		e.tick()
		if e.stage() != StageSettled {
			t.Fatalf("stage %s", e.stage())
		}
		facts, _ := e.st.TornTails()
		if len(facts) != 1 || facts[0].Len != int64(len(partial)) || facts[0].Off != int64(len(first)) {
			t.Fatalf("facts %+v", facts)
		}
		if got := e.raw(e.fileID(), len(first+partial)); got != first+partial {
			t.Fatal("tail bytes not kept in the archive")
		}
		if len(e.obs.EventsNamed(obs.EvTornTailPending)) == 0 {
			t.Fatal("no torn_tail_pending event")
		}
		// Later growth (a newline) invalidates the fact and parses.
		e.write(e.transcript(), "\n", e.clk.Now(), true)
		e.ingest(e.transcript())
		e.clk.Advance(25 * time.Hour)
		e.tick()
		if f, _ := e.st.TornTails(); len(f) != 0 {
			t.Fatalf("fact survived growth: %+v", f)
		}
	})
	t.Run("at maxLineBytes holds", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(2, 48*time.Hour)
		e.write(e.transcript(), strings.Repeat("x", 1<<20), e.clk.Now().Add(-48*time.Hour), true)
		e.ingest(e.transcript())
		e.tick()
		if got := e.heldReason(); got != ReasonTornTooLong {
			t.Fatalf("reason %s", got)
		}
	})
	t.Run("not yet quiet records no fact", func(t *testing.T) {
		e := newEnv(t, false)
		e.seed(2, 48*time.Hour)
		e.write(e.transcript(), `{"half`, e.clk.Now().Add(-time.Hour), true)
		e.ingest(e.transcript())
		e.tick()
		if f, _ := e.st.TornTails(); len(f) != 0 || e.stage() != StageQuiet {
			t.Fatalf("facts %+v stage %s", f, e.stage())
		}
	})
}

func TestSettleOrderOnTheRecordingWrapper(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	f, _ := e.st.File(e.fileID())
	cp := e.st.CopyPath(f.ID, f.CurGen)
	e.rec.Reset()
	e.tick()
	err := fsx.AssertOrder(
		e.rec.Events(),
		fsx.Step{Op: "write", PathContains: ".seg.tmp"},
		fsx.Step{Op: "fullsync", PathContains: ".seg.tmp"},
		fsx.Step{Op: "mark", PathContains: "readback"},
		fsx.Step{Op: "fullsyncdir", PathContains: "archives"},
		fsx.Step{Op: "rename", PathContains: ".seg.tmp->"},
		fsx.Step{Op: "fullsync", PathContains: "observations.journal"},
		fsx.Step{Op: "mark", PathContains: "commit"},
		fsx.Step{Op: "remove", PathContains: cp},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCrashPointsConverge(t *testing.T) {
	for _, point := range []string{"settle.after-temp", "settle.after-rename", "settle.after-commit", "settle.partial-delete"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t, false)
			content := e.seed(6, 48*time.Hour)
			file := e.fileID()
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.sett.Tick() })
			disarm()
			if !crashed {
				t.Fatalf("%s not reached", point)
			}
			e.crash()
			e.tick()
			if e.stage() != StageSettled {
				t.Fatalf("stage %s after recovery", e.stage())
			}
			if got := e.raw(file, len(content)); got != content {
				t.Fatal("bytes lost")
			}
			f, _ := e.st.File(file)
			if _, err := os.Stat(e.st.CopyPath(file, f.CurGen)); !os.IsNotExist(err) {
				t.Fatal("covered copy bytes remain after recovery")
			}
		})
	}
}

func TestLeftoverTemporaryArchiveIsRemoved(t *testing.T) {
	e := newEnv(t, false)
	e.seed(2, time.Hour)
	dir := e.st.SegDir(sess)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(dir+"/000001.seg.tmp", []byte("partial"), 0o600)
	e.crash()
	if _, err := os.Stat(dir + "/000001.seg.tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary archive left")
	}
}

func TestSafetyDefaultsDeleteNothingInClaudeDir(t *testing.T) {
	e := newEnv(t, false)
	e.seed(4, 48*time.Hour)
	e.write("history.jsonl", `{"display":"x"}`+"\n", e.clk.Now().Add(-48*time.Hour), false)
	e.before = nil
	e.ingest("history.jsonl")
	e.before = snap(t, e.claude)
	e.tick()
	e.tick()
	if d := e.before.Diff(snap(t, e.claude)); d != "" {
		t.Fatalf("Claude directory changed: %s", d)
	}
	if _, err := os.Stat(e.path("history.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestLowSpaceSettlesEarlyAndNeverRemoves(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, time.Minute)
	e.tick()
	if e.stage() != StageQuiet || e.sett.LowSpace() {
		t.Fatal("settled early without low space")
	}
	e.free = 1
	e.tick()
	if e.stage() != StageSettled || !e.sett.LowSpace() {
		t.Fatalf("low space did not settle early: %s", e.stage())
	}
	if len(e.obs.EventsNamed(obs.EvStateDirLow)) != 1 {
		t.Fatal("no state_dir_low event")
	}
	if _, err := os.Stat(e.path(e.transcript())); err != nil {
		t.Fatal("original removed")
	}
}

func TestCloneModeReclonesAndKeepsCopy(t *testing.T) {
	e := newEnv(t, true)
	content := e.seed(4, 48*time.Hour)
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s", e.stage())
	}
	if e.clone.calls < 2 { // the copier's own clone plus the settle re-clone
		t.Fatalf("clone calls %d", e.clone.calls)
	}
	file := e.fileID()
	f, _ := e.st.File(file)
	b, err := os.ReadFile(e.st.CopyPath(file, f.CurGen))
	if err != nil || string(b) != content {
		t.Fatalf("copy %v", err)
	}
	if m, ok, _ := e.st.SettledOf(sess); !ok || !strings.Contains(string(m.Manifest), `"method":"clone"`) {
		t.Fatalf("manifest %s", m.Manifest)
	}
}

func TestCloneWriteBetweenCheckAndCloneDiscards(t *testing.T) {
	e := newEnv(t, true)
	e.seed(4, 48*time.Hour)
	e.hooks.BeforeClone = func(int64) {
		e.write(e.transcript(), line(50), e.clk.Now().Add(-48*time.Hour), true)
	}
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatalf("stage %s", e.stage())
	}
	if len(e.obs.EventsNamed(obs.EvSettleDiscarded)) != 1 {
		t.Fatal("clone not discarded")
	}
	if ents, _ := os.ReadDir(e.st.Dir() + "/copies"); func() bool {
		for _, x := range ents {
			if strings.HasSuffix(x.Name(), ".tmp") {
				return true
			}
		}
		return false
	}() {
		t.Fatal("discarded clone left behind")
	}
}

func TestCloneFullHashCatchesSameSizeSameTailEdit(t *testing.T) {
	e := newEnv(t, true)
	e.seed(40, 48*time.Hour) // far more than the 4096-byte tail
	p := e.path(e.transcript())
	b, _ := os.ReadFile(p)
	b[10] ^= 0x01 // inside the first line: size and tail unchanged
	_ = os.WriteFile(p, b, 0o644)
	_ = os.Chtimes(p, e.clk.Now().Add(-48*time.Hour), e.clk.Now().Add(-48*time.Hour))
	file := e.fileID()
	e.tick()
	f, _ := e.st.File(file)
	if f.CurGen != 2 || e.stage() != StageQuiet {
		t.Fatalf("gen %d stage %s", f.CurGen, e.stage())
	}
	if rs, _ := e.st.Resets(true); len(rs) == 0 {
		t.Fatal("no reset entry")
	}
}

func TestGauges(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	e.write(e.transcript(), line(9), e.clk.Now().Add(-48*time.Hour), true)
	e.tick()
	reg := e.obs.Reg
	if v, _ := reg.Gauge(obs.MConversationsByStage, map[string]string{"stage": "held"}); v != 1 {
		t.Fatalf("held stage gauge %v", v)
	}
	if v, _ := reg.Gauge(obs.MHeldConversations, map[string]string{"reason": ReasonCopyOffset}); v != 1 {
		t.Fatalf("held reason gauge %v", v)
	}
	if v, _ := reg.Gauge(obs.MBytesPendingSettle, nil); v <= 0 {
		t.Fatalf("pending %v", v)
	}
	e.clk.Advance(2 * time.Hour)
	e.tick()
	if v, _ := reg.Gauge(obs.MOldestHeldAge, nil); v < 3600 {
		t.Fatalf("oldest held age %v", v)
	}
	if len(e.obs.EventsNamed(obs.EvSessionHeld)) == 0 || len(e.obs.EventsNamed(obs.EvArchiveWritten)) != 0 {
		t.Fatal("events")
	}
}

// Registry versions are keyed by session and go into that session's archive; the
// registry file's own bytes stay where they are.
func TestRegistryVersionsGoIntoTheSessionArchive(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	reg := `{"pid":4242,"sessionId":"` + sess + `","cwd":"/example/work","procStart":"Wed Oct 14 07:51:50 2026","status":"idle"}`
	e.write("sessions/4242.json", reg, e.clk.Now().Add(-48*time.Hour), false)
	e.ingest("sessions/4242.json")
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s", e.stage())
	}
	m, _, _ := e.st.SettledOf(sess)
	if !strings.Contains(string(m.Manifest), `"registry_versions"`) {
		t.Fatalf("manifest lacks registry versions: %s", m.Manifest)
	}
	files, _ := e.st.FilesByCurrentPath(e.path("sessions/4242.json"))
	rs, _ := e.st.Ranges(files[0].ID, 1)
	if len(rs) != 1 || rs[0].Session != sess {
		t.Fatalf("ranges %+v", rs)
	}
	if b, err := e.st.ReadPointer(files[0].ID, 1, 0, len(reg)); err != nil || string(b) != reg {
		t.Fatalf("archived registry bytes: %v %q", err, b)
	}
	if _, err := os.Stat(e.path("sessions/4242.json")); err != nil {
		t.Fatal(err)
	}
}

// seedSession writes n lines for one session id, quiet since age, and ingests it.
func (e *env) seedSession(id string, n int, age time.Duration) {
	e.t.Helper()
	var s string
	for i := 1; i <= n; i++ {
		s += strings.ReplaceAll(line(i), sess, id)
	}
	rel := "projects/example-project/" + id + ".jsonl"
	e.write(rel, s, e.clk.Now().Add(-age), false)
	e.ingest(rel)
	e.parseAll()
}

func TestTickReturnsStats(t *testing.T) {
	e := newEnv(t, false)
	id := func(n int) string { return fmt.Sprintf("0a1b2c3d-0000-4000-8000-00000000010%d", n) }
	for i := 1; i <= 3; i++ {
		e.seedSession(id(i), 4, 48*time.Hour)
	}
	first, err := e.sett.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickStats{Examined: 3, SettledNow: 3, Transitions: 3}); first != want {
		t.Fatalf("first tick %+v, want %+v", first, want)
	}
	e.seedSession(id(4), 4, time.Minute)
	e.live[id(4)] = true
	e.seedSession(id(5), 4, 48*time.Hour)
	got, err := e.sett.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if want := (TickStats{Examined: 5, Unchanged: 3, SettledNow: 1, Transitions: 1}); got != want {
		t.Fatalf("second tick %+v, want %+v", got, want)
	}
}
