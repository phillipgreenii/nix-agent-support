package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

type env struct {
	procs  *proc.Fake
	cfg    config.Config
	claude string
	d      *Daemon
	log    *SyncBuffer
}

// newEnv builds a daemon over throwaway directories; start=false leaves it
// opened but not started so a test can drive the watcher by hand.
func newEnv(t *testing.T, ctl bool) *env {
	t.Helper()
	tmp, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	root, err := filepath.EvalSymlinks(tmp) // the daemon resolves its roots too
	if err != nil {
		t.Fatal(err)
	}
	e := &env{claude: filepath.Join(root, "claude"), log: &SyncBuffer{}, procs: proc.NewFake()}
	for _, d := range []string{filepath.Join(e.claude, "projects", "example-proj"), filepath.Join(e.claude, "sessions")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = config.Defaults(func(string) string { return "" })
	e.cfg.ClaudeDir = e.claude
	e.cfg.StateDir = filepath.Join(root, "state")
	e.cfg.Socket = filepath.Join(root, "r.sock")
	e.cfg.CopyThrottle = 0
	// The default floor is a production value (20 GiB); a test must not depend on the host's free space.
	e.cfg.MinFreeBytes = 1
	if ctl {
		e.cfg.ControlStateDir = filepath.Join(root, "control")
		if err := os.MkdirAll(e.cfg.EndsDir(), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) open(t *testing.T) *Daemon {
	t.Helper()
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	return d
}

func (e *env) transcript(name, content string, mtime time.Time) string {
	p := filepath.Join(e.claude, "projects", "example-proj", name)
	_ = os.WriteFile(p, []byte(content), 0o600)
	_ = os.Chtimes(p, mtime, mtime)
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestFixtureAppendAppearsInCoverageWithCopyOffsetEqualToSize(t *testing.T) {
	e := newEnv(t, false)
	p := e.transcript("a.jsonl", "{\"n\":1}\n", time.Now())
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	c := client.New(e.cfg.Socket, "example-check")
	waitFor(t, "caught up", func() bool {
		var cov wire.Coverage
		return c.Post(context.Background(), "coverage", wire.CoverageRequest{View: "files"}, &cov) == nil && cov.CaughtUp
	})
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{\"n\":2}\n")
	_ = f.Close()
	d.Watcher().Wake(p)
	var cov wire.Coverage
	if err := c.Post(context.Background(), "coverage", wire.CoverageRequest{View: "files"}, &cov); err != nil {
		t.Fatal(err)
	}
	if len(cov.FileList) != 1 {
		t.Fatalf("files %+v", cov.FileList)
	}
	fc := cov.FileList[0]
	if fc.Size != 16 || fc.CopyOffset != fc.Size || fc.CopyLagBytes != 0 || fc.Path != p || cov.Degraded {
		t.Fatalf("coverage %+v", fc)
	}
	if cov.Files != 1 || cov.Worst != nil || cov.CopyLagBytesMax != 0 {
		t.Fatalf("summary %+v", cov.CoverageSummary)
	}
	// The copy itself is byte-exact.
	st, _ := d.Store().AllFiles()
	cp, _ := os.ReadFile(d.Store().CopyPath(st[0].ID, st[0].CurGen))
	if string(cp) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Fatalf("copy %q", cp)
	}
}

func TestLagIsReportedWhenTheOriginalGrowsBeforeTheCopy(t *testing.T) {
	e := newEnv(t, false)
	p := e.transcript("a.jsonl", "12345\n", time.Now().Add(-90*time.Second))
	d := e.open(t)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("more bytes\n")
	_ = f.Close()
	old := time.Now().Add(-90 * time.Second)
	_ = os.Chtimes(p, old, old)
	defer func() { _ = d.Close() }()
	cov, err := d.coverage("summary")
	if err != nil {
		t.Fatal(err)
	}
	if cov.CopyLagBytesMax != 11 || cov.CopyLagSecondsMax < 89 || cov.Worst == nil || cov.Worst.Path != p {
		t.Fatalf("%+v", cov.CoverageSummary)
	}
	body, werr := d.opStatus(context.Background(), nil)
	if werr != nil {
		t.Fatal(werr)
	}
	st := body.(wire.Status)
	if st.Healthy || st.Short != "behind" || !st.Degraded || st.CopyLagS < 89 {
		t.Fatalf("status %+v", st)
	}
}

func TestFirstRunCopiesNewestFirstAndAnswersDegradedWhileBehind(t *testing.T) {
	e := newEnv(t, false)
	now := time.Now()
	e.transcript("old.jsonl", "old\n", now.Add(-48*time.Hour))
	e.transcript("mid.jsonl", "mid\n", now.Add(-24*time.Hour))
	newest := e.transcript("new.jsonl", "new\n", now.Add(-time.Hour))
	d := e.open(t)
	defer func() { _ = d.Close() }()
	cov, _ := d.coverage("summary")
	if !cov.Degraded || cov.CaughtUp {
		t.Fatalf("before the first sweep: %+v", cov)
	}
	st, _ := d.opStatus(context.Background(), nil)
	if s := st.(wire.Status); s.Healthy || s.Short != "behind" || !strings.HasPrefix(s.Verdict, "behind:") {
		t.Fatalf("status %+v", s)
	}
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov, _ = d.coverage("files")
	if !cov.CaughtUp || cov.Degraded || cov.Files != 3 || cov.Backfill.Copied != 3 || cov.Backfill.Total != 3 {
		t.Fatalf("after: %+v", cov)
	}
	// File ids are assigned in ingest order, so newest first means id 1.
	for _, f := range cov.FileList {
		if f.Path == newest && f.FileID != 1 {
			t.Fatalf("newest file got id %d, want 1", f.FileID)
		}
	}
	if v, _ := d.Obs().Reg.Gauge(obs.MCaughtUp, nil); v != 1 {
		t.Fatalf("caught_up gauge %v", v)
	}
}

func TestEndsRootIsWatchedAndReportedInCoverage(t *testing.T) {
	e := newEnv(t, true)
	if err := os.WriteFile(filepath.Join(e.cfg.EndsDir(), "s1.json"), []byte(`{"reason":"example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.transcript("a.jsonl", "x\n", time.Now())
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov, _ := d.coverage("summary")
	if len(cov.Roots) != 2 || cov.Roots[1].Path != e.cfg.EndsDir() || !cov.Roots[1].Present || cov.Roots[1].Files != 1 || cov.Roots[0].Files != 1 {
		t.Fatalf("roots %+v", cov.Roots)
	}
}

func TestBlockListAdditionsAreNotIngested(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.BlockList = []string{"*.private.jsonl"}
	e.transcript("keep.jsonl", "k\n", time.Now())
	e.transcript("x.private.jsonl", "secret\n", time.Now())
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov, _ := d.coverage("summary")
	if cov.Files != 1 {
		t.Fatalf("files %d, want 1", cov.Files)
	}
}

func TestStatusOverTheWireHasTheStableShape(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "x\n", time.Now())
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	c := client.New(e.cfg.Socket, "cli")
	waitFor(t, "caught up", func() bool {
		var st wire.Status
		return c.Post(context.Background(), "status", nil, &st) == nil && st.CaughtUp
	})
	var raw map[string]json.RawMessage
	if err := c.Post(context.Background(), "status", nil, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"healthy", "verdict", "copy_lag_s", "parse_lag_s", "held", "free_bytes", "stage_counts", "parser_versions", "schema_version", "protocol_version"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("status lacks %q", k)
		}
	}
	var st wire.Status
	_ = c.Post(context.Background(), "status", nil, &st)
	if !st.Healthy || st.Short != "ok" || st.SchemaVersion != 1 || st.ProtocolVer != 1 || st.FreeBytes <= 0 {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(e.log.String(), `"client":"cli"`) {
		t.Fatalf("request log lacks the client name:\n%s", e.log.String())
	}
}

func TestMetricsExporterAndGauges(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "x\n", time.Now())
	e.cfg.MetricsAddr = "127.0.0.1:0"
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	waitFor(t, "first sweep", func() bool { return !d.Watcher().LastSweep().IsZero() })
	d.refreshGauges()
	reg := d.Obs().Reg
	if v, ok := reg.Gauge(obs.MHeartbeat, nil); !ok || v <= 0 {
		t.Fatalf("heartbeat %v", v)
	}
	if v, _ := reg.Gauge(obs.MStateFreeBytes, nil); v <= 0 {
		t.Fatalf("free bytes %v", v)
	}
	if v, _ := reg.Gauge(obs.MStateUsedBytes, nil); v <= 0 {
		t.Fatalf("used bytes %v", v)
	}
	if d.MetricsAddr() == "" {
		t.Fatal("exporter not started")
	}
}

func TestCheckpointRoundTripsAndCoalesces(t *testing.T) {
	dir := t.TempDir()
	c := newFileCheckpoint(dir)
	if st, err := c.Load(); err != nil || st.EventID != 0 {
		t.Fatalf("empty load %+v %v", st, err)
	}
	now := time.Now().Truncate(time.Second)
	if err := c.Save(watcher.State{EventID: 5, EventTime: now, VolumeUUID: "u", LastSweep: now}); err != nil {
		t.Fatal(err)
	}
	// A second save inside minGap is held back until Flush.
	if err := c.Save(watcher.State{EventID: 9, EventTime: now, VolumeUUID: "u", LastSweep: now}); err != nil {
		t.Fatal(err)
	}
	if st, _ := newFileCheckpoint(dir).Load(); st.EventID != 5 {
		t.Fatalf("coalesced write landed early: %+v", st)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	st, err := newFileCheckpoint(dir).Load()
	if err != nil || st.EventID != 9 || st.VolumeUUID != "u" || !st.LastSweep.Equal(now) {
		t.Fatalf("after flush %+v %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(dir, checkpointName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newFileCheckpoint(dir).Load(); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}

func TestVerdictTable(t *testing.T) {
	ok := wire.Status{CaughtUp: true, FreeBytes: 100}
	for name, tc := range map[string]struct {
		mut     func(*wire.Status)
		healthy bool
		short   string
	}{
		"healthy":  {func(*wire.Status) {}, true, "ok"},
		"backfill": {func(s *wire.Status) { s.CaughtUp = false }, false, "behind"},
		"copy lag": {func(s *wire.Status) { s.CopyLagS = 61 }, false, "behind"},
		"parse":    {func(s *wire.Status) { s.ParseLagS = 60 }, false, "behind"},
		"held":     {func(s *wire.Status) { s.Held = 2 }, false, "held"},
		"low disk": {func(s *wire.Status) { s.FreeBytes = 9 }, false, "behind"},
	} {
		s := ok
		tc.mut(&s)
		h, v, short := Verdict(s, 50)
		if h != tc.healthy || short != tc.short || v == "" {
			t.Errorf("%s: %v %q %q", name, h, v, short)
		}
	}
}

func TestRestartRecoversAndKeepsTheCopy(t *testing.T) {
	e := newEnv(t, false)
	p := e.transcript("a.jsonl", "one\n", time.Now())
	d := e.open(t)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("two\n")
	_ = f.Close()
	d2 := e.open(t)
	defer func() { _ = d2.Close() }()
	if err := d2.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov, _ := d2.coverage("files")
	if cov.Files != 1 || cov.FileList[0].CopyOffset != 8 || cov.FileList[0].CopyLagBytes != 0 {
		t.Fatalf("%+v", cov.FileList)
	}
}

func TestParsedRecordsAnswerQueryAndSearchThroughTheDaemonOps(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("s1.jsonl", `{"type":"assistant","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":7},"content":[{"type":"text","text":"the registry file was deleted"}]}}`+"\n{broken\n", time.Now())
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().Drain(); err != nil {
		t.Fatal(err)
	}
	ans, werr := d.opQuery(context.Background(), []byte(`{"where":{"field":"type","eq":"assistant"},"group":["model"],"aggregate":[{"fn":"sum","field":"usage.output_tokens","distinct":"message.id","as":"output_tokens"}]}`))
	if werr != nil {
		t.Fatal(werr)
	}
	b, _ := json.Marshal(ans)
	if !strings.Contains(string(b), `"rows":[["example-model-a",7]]`) || !strings.Contains(string(b), `"as_of"`) {
		t.Fatalf("query answer %s", b)
	}
	hits, werr := d.opSearch(context.Background(), []byte(`{"q":"registry deleted"}`))
	if werr != nil {
		t.Fatal(werr)
	}
	b, _ = json.Marshal(hits)
	if !strings.Contains(string(b), `[registry]`) {
		t.Fatalf("search answer %s", b)
	}
	cov, err := d.coverage("files")
	if err != nil {
		t.Fatal(err)
	}
	if cov.BadLines != 1 || cov.ParseLagBytesMax != 0 || len(cov.FileList) != 1 || cov.FileList[0].ParseOffset != cov.FileList[0].CopyOffset {
		t.Fatalf("coverage %+v", cov)
	}
	d.Parser().UpdateGauges()
	if v, ok := d.Obs().Reg.Gauge("parse_progress", nil); !ok || v != 1 {
		t.Fatalf("parse_progress %v %v", v, ok)
	}
	for _, g := range []string{"parse_lag_seconds", "parse_lag_bytes", "unknown_kinds"} {
		if _, ok := d.Obs().Reg.Gauge(g, nil); !ok {
			t.Fatalf("gauge %s not exported", g)
		}
	}
	if v, _ := d.Obs().Reg.Counter("bad_lines_total", nil); v != 1 {
		t.Fatalf("bad_lines_total %v", v)
	}
	if _, werr := d.opQuery(context.Background(), []byte(`{"where":{"field":"nope","exists":true}}`)); werr == nil || werr.Name != wire.ErrBadRequest {
		t.Fatalf("bad field: %v", werr)
	}
}
