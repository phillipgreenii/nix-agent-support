package copier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const tx = "projects/example-project/example-session.jsonl"

func TestByteModeCopiesFixtureTreeByteExact(t *testing.T) {
	e := newEnv(t)
	files := map[string]string{
		tx: "{\"n\":1}\n{\"n\":2}\n",
		"projects/example-project/example-session.status.jsonl":                                 "{\"s\":1}\n",
		"projects/example-project/0f8fad5b-d9cb-469f-a165-70867728950e/subagents/agent-1.jsonl": "{\"a\":1}\n",
		"history.jsonl": "{\"h\":1}\n",
	}
	ids := map[string]int64{}
	for rel, c := range files {
		p := e.write(rel, c)
		ids[rel] = e.ingest(p).File
	}
	for rel, c := range files {
		e.mustEqual(ids[rel], c)
		if f := e.file(ids[rel]); f.Mode != "append" {
			t.Errorf("%s mode %s", rel, f.Mode)
		}
	}
	if m, _ := e.cp.Method(); m != "bytes" {
		t.Fatalf("method %s", m)
	}
}

func TestCopyMethodLoggedAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := newEnv(t, func(e *env) { e.log = log; e.clone.reason = "volume reports no clone capability" })
	e.ingest(e.write(tx, "a\n"))
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "msg=copy_method") ||
		!strings.Contains(out, "method=bytes") || !strings.Contains(out, "reason=") {
		t.Fatalf("copy_method not logged at info with method and reason:\n%s", out)
	}
}

func TestCloneModeSeedsThenAppendsBytes(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true; e.clone.reason = "same volume, clone capable"; e.clone.private = 1234 })
	p := e.write(tx, "line1\nline2\n")
	r := e.ingest(p)
	g := e.gen(r.File, 1)
	if g.Method != "clone" || g.CopyOffset != int64(len("line1\nline2\n")) {
		t.Fatalf("gen = %+v", g)
	}
	e.mustEqual(r.File, "line1\nline2\n")
	e.appendTo(tx, "line3\n")
	e.ingest(p)
	e.mustEqual(r.File, "line1\nline2\nline3\n")
	if got := e.eventAttr(obs.EvCopyMethod, "method"); !contains(got, "clone") {
		t.Fatalf("copy_method events %v", got)
	}
	if v, ok := e.obs.Reg.Gauge(obs.MClonePrivateMax, nil); !ok || v != 1234 {
		t.Fatalf("private size max gauge = %v %v", v, ok)
	}
	if got := e.eventAttr(obs.EvClonePrivate, "bytes"); len(got) != 1 || got[0] != "1234" {
		t.Fatalf("clone_private bytes = %v", got)
	}
}

func TestPrivateCloneSeriesAreBounded(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true; e.clone.reason = "same volume, clone capable" })
	sizes := []int64{300, 900, 500}
	for i, sz := range sizes {
		e.clone.private = sz
		e.ingest(e.write(fmt.Sprintf("projects/example-project/s%d.jsonl", i), "line\n"))
	}
	if v, ok := e.obs.Reg.Gauge(obs.MClonePrivateMax, nil); !ok || v != 900 {
		t.Fatalf("max = %v %v, want 900", v, ok)
	}
	if v, ok := e.obs.Reg.Gauge(obs.MClonePrivateTotal, nil); !ok || v != 1700 {
		t.Fatalf("total = %v %v, want 1700", v, ok)
	}
	if v, ok := e.obs.Reg.Counter(obs.MClonePrivateFiles, nil); !ok || v != 3 {
		t.Fatalf("files = %v %v, want 3", v, ok)
	}
	if got := e.eventAttr(obs.EvClonePrivate, "file"); len(got) != 3 || got[0] == "" {
		t.Fatalf("clone_private files = %v", got)
	}
	counters, gauges, _ := e.obs.Reg.Snapshot()
	for _, m := range []map[string]float64{counters, gauges} {
		for k := range m {
			if strings.Contains(k, "file=") {
				t.Errorf("series %q carries a file label", k)
			}
			if k == "clone_private_size_bytes" {
				t.Errorf("retired series %q is still written", k)
			}
		}
	}
}

func TestCloneOrderTempFsyncRenameDirFsyncCommit(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	p := e.write(tx, "x\n")
	e.rec.Reset()
	r := e.ingest(p)
	err := fsx.AssertOrder(e.rec.Events(),
		fsx.Step{Op: "sync", PathContains: ".tmp"}, fsx.Step{Op: "rename", PathContains: ".tmp"},
		fsx.Step{Op: "syncdir", PathContains: "copies"}, fsx.Step{Op: "mark", PathContains: "commit"})
	if err != nil {
		t.Fatal(err)
	}
	_ = r
}

func TestENOTSUPAndEXDEVSelectByteModeWithoutError(t *testing.T) {
	for name, errno := range map[string]error{"ENOTSUP": unix.ENOTSUP, "EXDEV": unix.EXDEV} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, func(e *env) { e.clone.ok = true; e.clone.err = &os.LinkError{Op: "clone", Err: errno} })
			r := e.ingest(e.write(tx, "abc\n"))
			e.mustEqual(r.File, "abc\n")
			if g := e.gen(r.File, 1); g.Method != "bytes" {
				t.Fatalf("method %s", g.Method)
			}
			if got := e.eventAttr(obs.EvCopyMethod, "reason"); !contains(got, name) {
				t.Fatalf("reason %s not logged: %v", name, got)
			}
		})
	}
}

func TestOtherCloneErrnoIsAFault(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true; e.clone.err = unix.EIO })
	if _, err := e.cp.Ingest(e.write(tx, "abc\n")); err == nil || !errors.Is(err, unix.EIO) {
		t.Fatalf("err = %v, want EIO fault", err)
	}
}

func TestAppendResumesFromCommittedOffset(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "one\n")
	r := e.ingest(p)
	e.appendTo(tx, "two\n")
	if res := e.ingest(p); res.Bytes != 4 {
		t.Fatalf("copied %d bytes, want only the new 4", res.Bytes)
	}
	e.mustEqual(r.File, "one\ntwo\n")
	if res := e.ingest(p); res.Action != "unchanged" {
		t.Fatalf("action %s", res.Action)
	}
}

func TestTornLineIsCopiedAtOnce(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "whole\npart")
	r := e.ingest(p)
	e.mustEqual(r.File, "whole\npart")
	e.appendTo(tx, "ial\n")
	e.ingest(p)
	e.mustEqual(r.File, "whole\npartial\n")
}

func TestCopyOrderAppendFsyncCommit(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "one\n")
	r := e.ingest(p)
	e.appendTo(tx, "two\n")
	e.rec.Reset()
	e.ingest(p)
	cp := e.st.CopyPath(r.File, 1)
	if err := fsx.AssertOrder(e.rec.Events(), fsx.CopierAppendOrder(cp)...); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAppenderGivesByteIdenticalCopyWithoutDuplicates(t *testing.T) {
	e := newEnv(t, func(e *env) { e.cfgMod = func(c *Config) { c.Chunk = 64 } })
	p := e.write(tx, "")
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		defer func() { _ = f.Close() }()
		for i := 0; i < 300; i++ {
			_, _ = fmt.Fprintf(f, "{\"line\":%d,\"pad\":\"%s\"}\n", i, strings.Repeat("x", i%40))
		}
	}()
	for ctx.Err() == nil {
		e.ingest(p)
	}
	wg.Wait()
	id := e.ingest(p).File
	want, _ := os.ReadFile(p)
	e.mustEqual(id, string(want))
	if n := strings.Count(e.copyOf(id), "\n"); n != 300 {
		t.Fatalf("%d lines, want 300 (no duplicate or lost line)", n)
	}
}

func TestShrinkStartsNewGenerationKeepingTheOld(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "aaaa\nbbbb\ncccc\n")
	r := e.ingest(p)
	if err := os.WriteFile(p, []byte("zz\n"), 0o644); err != nil { // same inode, shorter
		t.Fatal(err)
	}
	res := e.ingest(p)
	if res.Action != "new-generation" || res.File != r.File {
		t.Fatalf("res %+v", res)
	}
	f := e.file(r.File)
	if f.CurGen != 2 {
		t.Fatalf("gen %d", f.CurGen)
	}
	e.mustEqual(r.File, "zz\n")
	old, _ := os.ReadFile(e.st.CopyPath(r.File, 1))
	if string(old) != "aaaa\nbbbb\ncccc\n" {
		t.Fatalf("old generation lost: %q", old)
	}
	resets, _ := e.st.Resets(true)
	if len(resets) != 1 || resets[0].ToGen != 2 || resets[0].Reason != "shrink" || !resets[0].Pinned {
		t.Fatalf("resets %+v", resets)
	}
	if got := e.eventAttr(obs.EvGenerationStart, "reason"); !contains(got, "shrink") {
		t.Fatalf("events %v", got)
	}
}

func TestSameSizeInPlaceEditInTailStartsNewGeneration(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "hello world\n") // shorter than 4,096 bytes: the whole copy is the tail
	r := e.ingest(p)
	if err := os.WriteFile(p, []byte("hellO world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(p); res.Action != "new-generation" {
		t.Fatalf("res %+v", res)
	}
	e.mustEqual(r.File, "hellO world\n")
	if g := e.gen(r.File, 2); g.Reason != "tail_mismatch" {
		t.Fatalf("reason %s", g.Reason)
	}
}

func TestLargeFileTailIs4096BytesAndMismatchDetected(t *testing.T) {
	e := newEnv(t)
	body := strings.Repeat("0123456789", 1000) // 10,000 bytes
	p := e.write(tx, body)
	r := e.ingest(p)
	if g := e.gen(r.File, 1); g.TailLen != 4096 {
		t.Fatalf("tail len %d", g.TailLen)
	}
	edited := []byte(body)
	edited[len(edited)-5] = 'X'
	if err := os.WriteFile(p, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if e.file(r.File).CurGen != 2 {
		t.Fatal("tail edit did not start a new generation")
	}
}

func TestLostTailSidecarIsRebuiltFromTheCopy(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "alpha\n")
	r := e.ingest(p)
	if err := os.Remove(e.st.TailPath(r.File)); err != nil {
		t.Fatal(err)
	}
	e.appendTo(tx, "beta\n")
	if res := e.ingest(p); res.Action != "copied" {
		t.Fatalf("res %+v", res)
	}
	if e.file(r.File).CurGen != 1 {
		t.Fatal("a lost sidecar must not start a new generation")
	}
	e.mustEqual(r.File, "alpha\nbeta\n")
	if _, err := os.Stat(e.st.TailPath(r.File)); err != nil {
		t.Fatal("sidecar not rewritten")
	}
}

func TestRenameKeepsCopyByInodeAndBirthTime(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "one\n")
	r := e.ingest(p)
	np := e.abs("projects/example-project/renamed.jsonl")
	if err := os.Rename(p, np); err != nil {
		t.Fatal(err)
	}
	e.appendTo("projects/example-project/renamed.jsonl", "two\n")
	r2 := e.ingest(np)
	if r2.File != r.File || e.file(r.File).CurGen != 1 {
		t.Fatalf("rename created a new file or generation: %+v", r2)
	}
	e.mustEqual(r.File, "one\ntwo\n")
	cur, _ := e.st.CurrentPaths(r.File)
	if len(cur) != 1 || cur[0] != np {
		t.Fatalf("current paths %v", cur)
	}
	hist, _ := e.st.PathHistory(r.File)
	if len(hist) != 2 {
		t.Fatalf("history %v", hist)
	}
}

func TestDirectoryRenameKeepsEveryCopy(t *testing.T) {
	e := newEnv(t)
	a := e.write("projects/example-a/s1.jsonl", "s1\n")
	b := e.write("projects/example-a/s2.jsonl", "s2\n")
	ra, rb := e.ingest(a), e.ingest(b)
	if err := os.Rename(e.abs("projects/example-a"), e.abs("projects/example-b")); err != nil {
		t.Fatal(err)
	}
	na, nb := e.abs("projects/example-b/s1.jsonl"), e.abs("projects/example-b/s2.jsonl")
	if e.ingest(na).File != ra.File || e.ingest(nb).File != rb.File {
		t.Fatal("directory rename created new files")
	}
	if e.file(ra.File).CurGen != 1 || e.file(rb.File).CurGen != 1 {
		t.Fatal("directory rename started a generation")
	}
	found := map[Identity]bool{}
	for _, p := range []string{na, nb} {
		fi, _ := os.Lstat(p)
		id, _ := identityOf(p, fi)
		found[id] = true
	}
	if err := e.cp.SweepMissing(found); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(ra.File); len(ts) != 0 {
		t.Fatal("renamed directory produced a deletion")
	}
	cur, _ := e.st.CurrentPaths(ra.File)
	if len(cur) != 1 || cur[0] != na {
		t.Fatalf("current %v", cur)
	}
}

func TestHardLinkIsOneFileWithTwoPaths(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "shared\n")
	l := e.abs("projects/example-project/link.jsonl")
	if err := os.Link(p, l); err != nil {
		t.Fatal(err)
	}
	r1, r2 := e.ingest(p), e.ingest(l)
	if r1.File != r2.File {
		t.Fatalf("hard link made two files: %d %d", r1.File, r2.File)
	}
	if cur, _ := e.st.CurrentPaths(r1.File); len(cur) != 2 {
		t.Fatalf("paths %v", cur)
	}
	e.mustEqual(r1.File, "shared\n")
}

func TestSymbolicLinkIsRecordedAndNeverFollowed(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(t.TempDir(), "outside-secret.jsonl")
	if err := os.WriteFile(outside, []byte("OUTSIDE-CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := e.write(tx, "inside\n")
	for name, target := range map[string]string{"out.jsonl": outside, "in.jsonl": p} {
		ln := e.abs("projects/example-project/" + name)
		if err := os.Symlink(target, ln); err != nil {
			t.Fatal(err)
		}
		r := e.ingest(ln)
		if r.Action != "link" {
			t.Fatalf("%s: %+v", name, r)
		}
		f := e.file(r.File)
		if f.Mode != "link" || f.Link != target {
			t.Fatalf("%s: %+v", name, f)
		}
		if gens, _ := e.st.Generations(r.File); len(gens) != 0 {
			t.Fatalf("%s: a link got a copy", name)
		}
		if again := e.ingest(ln); again.Action != "unchanged" {
			t.Fatalf("%s again: %+v", name, again)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(e.state, "copies"))
	for _, de := range ents {
		b, _ := os.ReadFile(filepath.Join(e.state, "copies", de.Name()))
		if bytes.Contains(b, []byte("OUTSIDE-CONTENT")) {
			t.Fatal("a symbolic link was followed")
		}
	}
}

func TestRenameOverLikeSessionModeKeepsSnapshotVersions(t *testing.T) {
	e := newEnv(t)
	rel := "projects/example-project/example-session.session-mode.json"
	var id int64
	var inodes []uint64
	for i, content := range []string{`{"mode":"a"}`, `{"mode":"b"}`, `{"mode":"c"}`} {
		tmp := e.abs("projects/example-project/swap.pending")
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, e.abs(rel)); err != nil { // a new inode every write
			t.Fatal(err)
		}
		fi, _ := os.Lstat(e.abs(rel))
		ident, _ := identityOf("", fi)
		inodes = append(inodes, ident.Ino)
		r := e.ingest(e.abs(rel))
		if i == 0 {
			id = r.File
		} else if r.File != id {
			t.Fatalf("snapshot keyed by inode: file %d then %d", id, r.File)
		}
	}
	snaps, _ := e.st.Snapshots(id)
	if len(snaps) != 3 {
		t.Fatalf("versions %d", len(snaps))
	}
	b, _ := os.ReadFile(e.st.SnapshotPath(id, 2))
	if string(b) != `{"mode":"b"}` {
		t.Fatalf("version 2 = %q", b)
	}
	if res := e.ingest(e.abs(rel)); res.Action != "unchanged" {
		t.Fatalf("same hash stored again: %+v", res)
	}
	_ = inodes
}

func TestAppendFileReplacedByRenameOverIsANewFile(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "old content\n")
	r1 := e.ingest(p)
	tmp := e.abs("projects/example-project/swap.pending")
	if err := os.WriteFile(tmp, []byte("new content that differs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	r2 := e.ingest(p)
	if r2.File == r1.File {
		t.Fatal("a path that now holds a different file must be a new file")
	}
	e.mustEqual(r2.File, "new content that differs\n")
	if old, _ := e.st.CurrentPaths(r1.File); len(old) != 0 {
		t.Fatalf("old file keeps the path: %v", old)
	}
	e.mustEqual(r1.File, "old content\n")
}

func TestSnapshotRewrittenInPlaceAndTombstone(t *testing.T) {
	e := newEnv(t)
	p := e.write("sessions/1234.json", `{"pid":1234,"procStart":"t1"}`)
	r := e.ingest(p)
	if err := os.WriteFile(p, []byte(`{"pid":1234,"procStart":"t1","name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if snaps, _ := e.st.Snapshots(r.File); len(snaps) != 2 {
		t.Fatalf("versions %d", len(snaps))
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	if res := e.ingest(p); res.Action != "tombstone" {
		t.Fatalf("res %+v", res)
	}
	ts, _ := e.st.Tombstones(r.File)
	if len(ts) != 1 || ts[0] != e.clk.Now().UnixNano() {
		t.Fatalf("tombstones %v", ts)
	}
	if f := e.file(r.File); f.State != "deleted" {
		t.Fatalf("state %s", f.State)
	}
	// The path comes back: the same path-keyed file continues.
	e.write("sessions/1234.json", `{"pid":1234,"procStart":"t2"}`)
	if res := e.ingest(p); res.File != r.File || res.Action != "snapshot" {
		t.Fatalf("revive %+v", res)
	}
	if f := e.file(r.File); f.State != "ok" {
		t.Fatalf("state after revive %s", f.State)
	}
}

func TestSnapshotJSONReadAgainUntilValid(t *testing.T) {
	e := newEnv(t, func(e *env) {
		e.cfgMod = func(c *Config) {
			c.Clock = &hookClock{Fake: e.clk, onSleep: func() { _ = os.WriteFile(e.abs("sessions/77.json"), []byte(`{"pid":77}`), 0o644) }}
		}
	})
	p := e.write("sessions/77.json", `{"pid":7`) // torn
	r := e.ingest(p)
	b, _ := os.ReadFile(e.st.SnapshotPath(r.File, 1))
	if string(b) != `{"pid":77}` {
		t.Fatalf("stored %q", b)
	}
}

func TestSnapshotThatStaysInvalidIsNotStored(t *testing.T) {
	e := newEnv(t)
	p := e.write("sessions/78.json", `{"pid":`)
	if _, err := e.cp.Ingest(p); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("err %v", err)
	}
	if files, _ := e.st.AllFiles(); len(files) != 1 {
		// the file row exists (identity), but no version
		t.Logf("files %d", len(files))
	}
	for _, f := range mustFiles(t, e) {
		if snaps, _ := e.st.Snapshots(f.ID); len(snaps) != 0 {
			t.Fatal("invalid JSON stored")
		}
	}
}

func TestSnapshotVersionMissedIsCounted(t *testing.T) {
	e := newEnv(t, func(e *env) {
		e.cfgMod = func(c *Config) {
			c.InferMissed = func(_ string, prev, cur []byte) int {
				if strings.Contains(string(prev), `"v":1`) && strings.Contains(string(cur), `"v":4`) {
					return 2
				}
				return 0
			}
		}
	})
	p := e.write("sessions/90.json", `{"v":1}`)
	e.ingest(p)
	e.write("sessions/90.json", `{"v":4}`)
	e.ingest(p)
	if v, _ := e.obs.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonSnapMissed}); v != 2 {
		t.Fatalf("counter %v", v)
	}
	if len(e.obs.EventsNamed(obs.EvSnapshotMissed)) != 1 {
		t.Fatal("event missing")
	}
}

func TestControlEndsRootOutsideClaudeDirIsIngested(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.ends, "example-session.1730000000000.json")
	if err := os.WriteFile(p, []byte(`{"session":"example-session","reason":"exit"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.ingest(p)
	if r.Action != "snapshot" {
		t.Fatalf("%+v", r)
	}
	if f := e.file(r.File); f.Source != "claude-control-end" {
		t.Fatalf("source %s", f.Source)
	}
	if cur, _ := e.st.CurrentPaths(r.File); len(cur) != 1 || cur[0] != p {
		t.Fatalf("paths %v", cur)
	}
}

func TestBlockListAndOutOfScopeAreNotIngested(t *testing.T) {
	e := newEnv(t)
	blocked := map[string]string{
		"sessions/abc.key": "session_key",
		"projects/example-project/.session-mode.tmp.42":  "atomic_tmp",
		"projects/example-project/x.tmp.9":               "atomic_tmp",
		"projects/.DS_Store":                             "ds_store",
		"projects/example-project/w/node_modules/p/i.js": "node_modules",
		"file-history/x":                                 "outside_roots",
		"settings.json":                                  "outside_roots",
	}
	for rel, why := range blocked {
		p := e.write(rel, "secret\n")
		r := e.ingest(p)
		if r.Action != "skipped" || r.Reason != why {
			t.Errorf("%s: %+v want skipped/%s", rel, r, why)
		}
	}
	if files, _ := e.st.AllFiles(); len(files) != 0 {
		t.Fatalf("%d files ingested", len(files))
	}
	r := e.ingest(e.write("projects/example-project/other.jsonl", "ok\n"))
	if r.Action == "skipped" {
		t.Fatal("a non-blocked file in the same tree was skipped")
	}
}

func TestFullContentsAreNotInTheDatabaseOrJournal(t *testing.T) {
	e := newEnv(t)
	marker := "EXAMPLE-DISTINCT-MARKER-0123456789"
	body := strings.Repeat(marker+"\n", 500)
	e.ingest(e.write(tx, body))
	e.ingest(e.write("sessions/5.json", `{"note":"`+marker+`"}`))
	e.reopen() // checkpoints the WAL
	for _, name := range []string{"index.db", "index.db-wal", "observations.journal"} {
		b, err := os.ReadFile(filepath.Join(e.state, name))
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(marker)) {
			t.Fatalf("%s holds file contents", name)
		}
	}
}

func TestInodeReuseLoopCopiesWhateverNowHoldsThePath(t *testing.T) {
	e := newEnv(t)
	p := e.abs(tx)
	for i := 0; i < 150; i++ {
		_ = os.Remove(p)
		content := fmt.Sprintf("generation %d of the file\n", i)
		e.write(tx, content)
		r := e.ingest(p)
		e.mustEqual(r.File, content)
		holders, _ := e.st.FilesByCurrentPath(p)
		if len(holders) != 1 || holders[0].ID != r.File {
			t.Fatalf("iteration %d: holders %+v", i, holders)
		}
	}
}

func TestInodeAtNewPathNeedsBirthTimeAndFirstBytes(t *testing.T) {
	e := newEnv(t)
	body := strings.Repeat("the first bytes of the original file.\n", 4)
	p := e.write(tx, body)
	r := e.ingest(p)
	// A different file reusing the identity of a known file at a new path: the
	// first bytes differ, so it is not a rename.
	f := e.file(r.File)
	if f.FirstHash == 0 {
		t.Fatal("first-bytes hash not recorded")
	}
	np := e.abs("projects/example-project/other.jsonl")
	if err := os.Rename(p, np); err != nil {
		t.Fatal(err)
	}
	// Rewrite the first bytes in place, keeping inode, birth time and length.
	rewritten := strings.Repeat("DIFFERENT first bytes entirely....\n", 5)
	if err := os.WriteFile(np, []byte(rewritten[:len(body)]), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := e.ingest(np)
	if r2.File == r.File {
		t.Fatal("same inode at a new path with different first bytes was treated as a rename")
	}
}

func TestMissingPathIsDeletionOnlyAfterFullSweep(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "bye\n")
	r := e.ingest(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(p); res.Action != "missing" {
		t.Fatalf("%+v", res)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 0 {
		t.Fatal("a missing path became a deletion before the sweep")
	}
	if err := e.cp.SweepMissing(map[Identity]bool{}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 1 {
		t.Fatal("sweep did not record the deletion")
	}
	if got := e.obs.EventsNamed(obs.EvFileVanished); len(got) != 1 {
		t.Fatalf("events %v", got)
	}
	e.mustEqual(r.File, "bye\n") // the copy is kept
}

func TestDeletedThenMovedFileIsNotADeletion(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "moving\n")
	r := e.ingest(p)
	np := e.abs("projects/example-project/moved.jsonl")
	if err := os.Rename(p, np); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(np)
	id, _ := identityOf(np, fi)
	if err := e.cp.SweepMissing(map[Identity]bool{id: true}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 0 {
		t.Fatal("moved file recorded as deleted")
	}
	if e.ingest(np).File != r.File {
		t.Fatal("moved file got a new id")
	}
}

func TestVanishedBytesAreCounted(t *testing.T) {
	e := newEnv(t, func(e *env) { e.cfgMod = func(c *Config) { c.Chunk = 4 } })
	p := e.write(tx, "abcdefgh\n")
	r := e.ingest(p)
	f := e.file(r.File)
	g := e.gen(r.File, f.CurGen)
	// The recorder observed 50 bytes more than it had copied when the file vanished
	// (the commit record carries the observed size).
	if err := e.st.Commit(&store.Rec{T: store.TCommit, At: 2, File: r.File, Gen: 1, From: g.CopyOffset, To: g.CopyOffset, Size: g.CopyOffset + 50, Tail: g.TailHash, TailLen: g.TailLen}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := e.cp.SweepMissing(map[Identity]bool{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.obs.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonVanished}); v != 50 {
		t.Fatalf("vanished counter %v", v)
	}
}

func TestENOSPCMidCopyKeepsTheCommittedOffsetAndHeals(t *testing.T) {
	ffs := &enospcFS{}
	e := newEnv(t, func(e *env) { e.cfgMod = func(c *Config) { ffs.FS = e.rec; c.FS = ffs } })
	p := e.write(tx, "first\n")
	r := e.ingest(p)
	e.appendTo(tx, "second line that will hit a full disk\n")
	ffs.armed = true
	if _, err := e.cp.Ingest(p); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err %v", err)
	}
	if g := e.gen(r.File, 1); g.CopyOffset != int64(len("first\n")) {
		t.Fatalf("offset moved despite the failure: %d", g.CopyOffset)
	}
	ffs.armed = false
	e.ingest(p)
	e.mustEqual(r.File, "first\nsecond line that will hit a full disk\n")
	if len(e.obs.EventsNamed(obs.EvCopyTrimmed)) != 1 {
		t.Fatal("the partial bytes were not trimmed")
	}
}

type enospcFS struct {
	fsx.FS
	armed bool
}

func (f *enospcFS) OpenFile(name string, flag int, perm os.FileMode) (fsx.File, error) {
	h, err := f.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &enospcFile{File: h, fs: f}, nil
}

type enospcFile struct {
	fsx.File
	fs *enospcFS
}

func (f *enospcFile) WriteAt(p []byte, off int64) (int, error) {
	if f.fs.armed && strings.HasSuffix(f.Name(), ".1") {
		n, _ := f.File.WriteAt(p[:len(p)/2], off)
		return n, syscall.ENOSPC
	}
	return f.File.WriteAt(p, off)
}

// ---- crash points ----

func TestCrashPointsRecoverOnReopen(t *testing.T) {
	for _, point := range []string{"copy.after-read", "copy.after-append", "copy.after-fsync", "copy.after-commit"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			p := e.write(tx, "alpha\n")
			r := e.ingest(p)
			e.appendTo(tx, "beta\ngamma\n")
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.cp.Ingest(p) })
			disarm()
			if !crashed {
				t.Fatalf("%s never reached", point)
			}
			e.reopen()
			if err := e.cp.Recover(); err != nil {
				t.Fatal(err)
			}
			e.ingest(p)
			e.mustEqual(r.File, "alpha\nbeta\ngamma\n")
			trimmed := len(e.obs.EventsNamed(obs.EvCopyTrimmed))
			wantTrim := point == "copy.after-append" || point == "copy.after-fsync"
			if (trimmed == 1) != wantTrim {
				t.Fatalf("%s: copy_trimmed_on_start events = %d", point, trimmed)
			}
		})
	}
}

func TestCrashBeforeFirstOffsetCommitOfANewFile(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "fresh\n")
	disarm := faults.ArmPanic("copy.after-fsync")
	crashed := faults.Recover(func() { _, _ = e.cp.Ingest(p) })
	disarm()
	if !crashed {
		t.Fatal("not reached")
	}
	e.reopen()
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	files, _ := e.st.AllFiles()
	if len(files) != 1 {
		t.Fatalf("files %d", len(files))
	}
	e.mustEqual(files[0].ID, "fresh\n")
}

func TestSnapshotCrashAfterCopyLeavesNoTempFile(t *testing.T) {
	e := newEnv(t)
	p := e.write("sessions/9.json", `{"pid":9}`)
	disarm := faults.ArmPanic("snapshot.after-copy")
	crashed := faults.Recover(func() { _, _ = e.cp.Ingest(p) })
	disarm()
	if !crashed {
		t.Fatal("not reached")
	}
	e.reopen()
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	r := e.ingest(p)
	if snaps, _ := e.st.Snapshots(r.File); len(snaps) != 1 {
		t.Fatalf("versions %d", len(snaps))
	}
}

// ---- startup recovery ----

func TestRecoverTrimsACopyLongerThanItsOffset(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "keep\n")
	r := e.ingest(p)
	cf, _ := os.OpenFile(e.st.CopyPath(r.File, 1), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = cf.WriteString("junk past the committed offset")
	_ = cf.Close()
	e.reopen()
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	e.mustEqual(r.File, "keep\n")
	if v, _ := e.obs.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonTrimmed}); v != float64(len("junk past the committed offset")) {
		t.Fatalf("trim counter %v", v)
	}
}

func TestRecoverMarksCopyMismatchAndRecopiesFromTheOriginal(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "0123456789\n")
	r := e.ingest(p)
	if err := os.Truncate(e.st.CopyPath(r.File, 1), 3); err != nil { // a power loss lost the tail
		t.Fatal(err)
	}
	e.reopen()
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(e.obs.EventsNamed(obs.EvCopyMismatch)) != 1 {
		t.Fatal("copy_mismatch not logged")
	}
	f := e.file(r.File)
	if f.CurGen != 2 || f.State != "ok" {
		t.Fatalf("file %+v", f)
	}
	e.mustEqual(r.File, "0123456789\n")
}

func TestRecoverCopyMismatchWithoutOriginalStartsNewGeneration(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "0123456789\n")
	r := e.ingest(p)
	if err := os.Truncate(e.st.CopyPath(r.File, 1), 3); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	f := e.file(r.File)
	if f.CurGen != 2 || f.State != "copy_mismatch" {
		t.Fatalf("file %+v", f)
	}
}

func TestRecoverRemovesLeftoverTemporaryFiles(t *testing.T) {
	e := newEnv(t)
	tmp := filepath.Join(e.state, "copies", "99.1.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.cp.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("temporary file left")
	}
}

// ---- seen-times, metrics and the store primitive ----

func TestCopyCommitsAdvanceLastCopyTimeAndKeepNoCommitRows(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "a\n")
	r := e.ingest(p)
	e.clk.Advance(time.Hour)
	e.appendTo(tx, "b\n")
	e.ingest(p)
	var last int64
	if err := e.st.Read().QueryRow(`SELECT last_copy_ns FROM files WHERE id=?`, r.File).Scan(&last); err != nil || last != t0.Add(time.Hour).UnixNano() {
		t.Fatalf("last_copy_ns %d, %v; want the second commit's time", last, err)
	}
	var n int
	if err := e.st.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='commits'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a new database must not hold a commits table (%d, %v)", n, err)
	}
}

func TestCopyLagMetricsAndRemovalRaceRegisteredAtZero(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "lag\n")
	if err := os.Chtimes(p, t0.Add(-5*time.Second), t0.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if v, ok := e.obs.Reg.Gauge(obs.MCopyLagSeconds, nil); !ok || v != 5 {
		t.Fatalf("lag seconds %v %v", v, ok)
	}
	if v, ok := e.obs.Reg.Gauge(obs.MCopyLagBytes, nil); !ok || v != 0 {
		t.Fatalf("lag bytes %v %v", v, ok)
	}
	if h := e.obs.Reg.Hist(obs.MCopyLagHistogram); h == nil || h.Samples != 1 {
		t.Fatalf("hist %+v", h)
	}
	if v, ok := e.obs.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonRemovalRace}); !ok || v != 0 {
		t.Fatalf("removal_race %v %v", v, ok)
	}
}

func TestGenerationRestartCountsDroppedBytes(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "0123456789\n")
	e.ingest(p)
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if len(e.obs.EventsNamed(obs.EvGenerationStart)) != 2 { // first and shrink
		t.Fatalf("events %d", len(e.obs.EventsNamed(obs.EvGenerationStart)))
	}
}

func TestRecordedCopyDoesNotExceedParseOffsetBound(t *testing.T) {
	e := newEnv(t)
	r := e.ingest(e.write(tx, "abc\n"))
	if err := e.st.SetParseOffset(r.File, 1, 4); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetParseOffset(r.File, 1, 5); err == nil {
		t.Fatal("parse offset beyond the copy offset accepted")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func mustFiles(t *testing.T, e *env) []store.File {
	t.Helper()
	fs, err := e.st.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// A path that an older scope classified as unparsed snapshot (workflow
// subagent files) is ingested under its new source: the old file row keeps its
// stored versions but loses the path, and a new file row takes it over.
func TestSourceChangeStartsANewFileAndClosesTheOldPath(t *testing.T) {
	const wf = "projects/example-project/11111111-1111-4111-8111-111111111111/subagents/workflows/wf_1/"
	for _, c := range []struct {
		name, file, src, mode string
	}{
		{"transcript", "agent-a1.jsonl", scope.SrcTranscript, "append"},
		{"meta", "agent-a1.meta.json", scope.SrcSubagentMD, "snapshot"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			content := `{"agentType":"general-purpose"}` + "\n"
			p := e.write(wf+c.file, content)
			fi, _ := os.Lstat(p)
			ident, _ := identityOf(p, fi)
			// The state an older recorder left: the same path as unparsed snapshot.
			old, err := e.cp.createFile(p, ident, scope.SrcUnparsed, string(scope.Snapshot), "", 0)
			if err != nil {
				t.Fatal(err)
			}
			r := e.ingest(p)
			if r.File == old.ID {
				t.Fatal("the old unparsed row was continued")
			}
			nf := e.file(r.File)
			if nf.Source != c.src || nf.Mode != c.mode {
				t.Fatalf("new row %s/%s, want %s/%s", nf.Source, nf.Mode, c.src, c.mode)
			}
			if of := e.file(old.ID); of.Source != scope.SrcUnparsed {
				t.Fatalf("old row source %s", of.Source)
			}
			if cur, _ := e.st.CurrentPaths(old.ID); len(cur) != 0 {
				t.Fatalf("old row still holds %v", cur)
			}
			if cur, _ := e.st.CurrentPaths(r.File); len(cur) != 1 || cur[0] != p {
				t.Fatalf("new row paths %v", cur)
			}
			if again := e.ingest(p); again.File != r.File {
				t.Fatalf("second ingest moved to file %d", again.File)
			}
		})
	}
}
