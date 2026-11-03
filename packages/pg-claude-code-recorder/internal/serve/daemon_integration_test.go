//go:build integration

package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func TestProcessAndConversationOperationsAnswerErrors(t *testing.T) {
	e := newEnv(t, false)
	d := e.started(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		call func() *wire.Error
		want string
	}{
		"show missing id": {func() *wire.Error { _, w := d.opConversationShow(ctx, []byte(`{}`)); return w }, wire.ErrBadRequest},
		"show unknown":    {func() *wire.Error { _, w := d.opConversationShow(ctx, []byte(`{"session_id":"nope"}`)); return w }, wire.ErrNotFound},
		"list bad json":   {func() *wire.Error { _, w := d.opConversationList(ctx, []byte(`{`)); return w }, wire.ErrBadRequest},
		"list bad since":  {func() *wire.Error { _, w := d.opConversationList(ctx, []byte(`{"since":"yesterday"}`)); return w }, wire.ErrBadRequest},
		"process no pid":  {func() *wire.Error { _, w := d.opProcessShow(ctx, []byte(`{}`)); return w }, wire.ErrBadRequest},
		"process unknown": {func() *wire.Error { _, w := d.opProcessShow(ctx, []byte(`{"pid":99}`)); return w }, wire.ErrNotFound},
	} {
		if w := tc.call(); w == nil || w.Name != tc.want {
			t.Errorf("%s: %v, want %s", name, w, tc.want)
		}
	}
	ans, w := d.opConversationList(ctx, nil)
	if w != nil || len(ans.(lifecycle.ConversationList).Conversations) != 0 {
		t.Fatalf("empty list %v %v", ans, w)
	}
}

func TestARestartRegistersTheWatchAgainAndLastSeenAliveSurvives(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.GraceWindow = 5 * time.Millisecond
	d := e.started(t)
	e.procs.Add(491, t0, nil)
	e.registry(t, 491, regJSON(491, "example-restart", t0, "idle", 1))
	e.settle(t)
	d.Tracker().Tick()
	_ = d.Close()
	before := len(e.procs.Watched)
	d2 := e.open(t)
	if err := d2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	if len(e.procs.Watched) != before+1 {
		t.Fatalf("watches %d, want %d: the watch was not registered again", len(e.procs.Watched), before+1)
	}
	if p := show(t, d2, 491); p.LastSeenAlive == nil || !p.Alive {
		t.Fatalf("process %+v", p)
	}
	e.procs.Exit(491, t0.Add(time.Minute))
	if got := waitExit(t, d2, 491); got.ExitKind != store.ExitObserved {
		t.Fatalf("exit after a restart %+v", got)
	}
}

func TestTheGraceWindowReadsTheRegistryAgainAfterASlowDeletion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		grace       bool
		wantPresent bool
	}{{"on", true, false}, {"off", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, false)
			e.cfg.GraceEnable, e.cfg.GraceWindow = tc.grace, 600*time.Millisecond
			d := e.started(t)
			e.procs.Add(481, t0, nil)
			e.registry(t, 481, regJSON(481, "example-slow", t0, "idle", 1))
			e.settle(t)
			e.procs.Exit(481, t0.Add(time.Minute))
			time.Sleep(150 * time.Millisecond) // the exit was seen with the file still there
			e.removeRegistry(t, 481)           // a slow deletion
			if got := waitExit(t, d, 481); got.RegistryPresent != tc.wantPresent {
				t.Fatalf("grace %v: registry present at exit = %v, want %v", tc.grace, got.RegistryPresent, tc.wantPresent)
			}
		})
	}
}

func TestRequestCountersReadEachLineOnceAndBucketUnknownValues(t *testing.T) {
	e := newEnv(t, true)
	d := e.started(t)
	req := filepath.Join(e.cfg.ControlStateDir, "requests.jsonl")
	write := func(s string) {
		if err := os.WriteFile(req, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l1 := `{"ts":1,"id":"a","verb":"submit","outcome":"ok"}` + "\n"
	l2 := `{"ts":2,"id":"b","verb":"run","outcome":"refused: command not in run.allow: x"}` + "\n"
	l3 := `{"ts":3,"id":null,"verb":"frobnicate","outcome":"weird"}` + "\n"
	write(l1 + l2 + l3)
	d.Tracker().Poll()
	d.Tracker().Poll()
	get := func(verb, outcome string) float64 {
		return counter(d, obs.MControlRequests, map[string]string{"verb": verb, "outcome": outcome})
	}
	if get("submit", "ok") != 1 || get("run", "refused") != 1 || get("other", "other") != 1 {
		t.Fatalf("counters submit/ok=%v run/refused=%v other/other=%v", get("submit", "ok"), get("run", "refused"), get("other", "other"))
	}
	write(l1 + l2 + l3 + `{"ts":4,"id":"d","verb":"ping","outcome":"duplicate"}` + "\n")
	d.Tracker().Poll()
	if get("submit", "ok") != 1 || get("ping", "duplicate") != 1 {
		t.Fatal("a line was read twice or a new one missed")
	}
}

func TestAStaleFifoIsRemovedAtStartupAndWhenItsProcessExits(t *testing.T) {
	e := newEnv(t, true)
	e.cfg.GraceWindow = 5 * time.Millisecond
	inbox := filepath.Join(e.cfg.ControlStateDir, "inbox")
	e.procs.Add(461, t0, nil)                     // live and matching
	e.procs.Add(463, t0.Add(-100*time.Hour), nil) // the pid was reused by another process
	e.registry(t, 461, regJSON(461, "example-f1", t0, "idle", 1))
	e.registry(t, 463, regJSON(463, "example-f3", t0, "idle", 1))
	for _, pid := range []string{"461", "462", "463"} { // 462 is a dead pid nobody knows
		mkfifo(t, filepath.Join(inbox, pid+".fifo"))
	}
	d := e.started(t)
	e.settle(t)
	for pid, want := range map[string]bool{"461": true, "462": false, "463": false} {
		_, err := os.Lstat(filepath.Join(inbox, pid+".fifo"))
		if (err == nil) != want {
			t.Errorf("%s.fifo present=%v, want %v", pid, err == nil, want)
		}
	}
	if got := counter(d, obs.MStaleFifosRemoved, nil); got != 2 {
		t.Fatalf("stale_fifos_removed_total = %v", got)
	}
	e.procs.Exit(461, t0.Add(time.Minute))
	waitExit(t, d, 461)
	// The tracker commits the exit and then removes the FIFO, so the exit row
	// appearing does not mean the FIFO is gone yet.
	waitFor(t, "the FIFO of an exited Process to be removed", func() bool {
		_, err := os.Lstat(filepath.Join(inbox, "461.fifo"))
		return err != nil
	})
}

func TestControlInboxPresenceFeedsTheProcessesColumnAndTheGauges(t *testing.T) {
	e := newEnv(t, true)
	d := e.started(t)
	for _, pid := range []int{451, 452, 453} {
		e.procs.Add(pid, t0, nil)
		e.registry(t, pid, regJSON(pid, fmt.Sprintf("example-i%d", pid), t0, "idle", 1))
	}
	e.settle(t)
	inbox := filepath.Join(e.cfg.ControlStateDir, "inbox")
	mkfifo(t, filepath.Join(inbox, "451.fifo"))
	mkfifo(t, filepath.Join(inbox, "452.fifo"))
	d.Tracker().Tick()
	if !show(t, d, 451).ControlInbox || show(t, d, 453).ControlInbox {
		t.Fatal("control_inbox column wrong")
	}
	gauge := func(state string) float64 {
		v, _ := d.Obs().Reg.Gauge(obs.MControlInbox, map[string]string{"state": state})
		return v
	}
	if gauge("present") != 2 || gauge("gone") != 0 || gauge("never_loaded") != 1 {
		t.Fatalf("gauges present=%v gone=%v never=%v", gauge("present"), gauge("gone"), gauge("never_loaded"))
	}
	_ = os.Remove(filepath.Join(inbox, "452.fifo"))
	d.Tracker().Tick()
	if gauge("present") != 1 || gauge("gone") != 1 || gauge("never_loaded") != 1 {
		t.Fatalf("after a removal: present=%v gone=%v never=%v", gauge("present"), gauge("gone"), gauge("never_loaded"))
	}
	if v, _ := d.Obs().Reg.Gauge(obs.MNoInbox, nil); v != 2 {
		t.Fatalf("processes_without_control_inbox = %v", v)
	}
}

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheHostBindingIsReadOnceFromTheEnvironment(t *testing.T) {
	e := newEnv(t, false)
	d := e.started(t)
	e.procs.Add(441, t0, map[string]string{"CMUX_SURFACE_ID": "surface-1", "CMUX_WORKSPACE_ID": "ws-1", "TERM_PROGRAM": "example-term"})
	e.procs.Add(442, t0, map[string]string{"TMUX": "/tmp/example-sock,123,0", "TMUX_PANE": "%0"})
	e.procs.Add(443, t0, map[string]string{"TERM_PROGRAM": "example-term"})
	for _, pid := range []int{441, 442, 443} {
		e.registry(t, pid, regJSON(pid, fmt.Sprintf("example-h%d", pid), t0, "idle", 1))
	}
	e.settle(t)
	e.procs.Add(441, t0, map[string]string{"TERM_PROGRAM": "changed"}) // a later read would see this
	if h := show(t, d, 441).Host; h == nil || h.Kind != "cmux" || h.CmuxSurface != "surface-1" || h.CmuxWorkspace != "ws-1" {
		t.Fatalf("cmux %+v", h)
	}
	if h := show(t, d, 442).Host; h == nil || h.Kind != "tmux" || h.TmuxSocket != "/tmp/example-sock" || h.TmuxPane != "%0" {
		t.Fatalf("tmux %+v", h)
	}
	if h := show(t, d, 443).Host; h == nil || h.Kind != "other" || h.TermProgram != "example-term" {
		t.Fatalf("other %+v", h)
	}
}

func TestStaleAtDiscoveryIsSetOnceWithItsCounterAndStartupEvent(t *testing.T) {
	e := newEnv(t, false)
	// Two registry files name dead pids and a third names a reused pid (alive, a
	// different start time); one names a live Process.
	e.registry(t, 431, regJSON(431, "example-s1", t0, "idle", 1))
	e.registry(t, 432, regJSON(432, "example-s2", t0, "idle", 1))
	e.registry(t, 433, regJSON(433, "example-s3", t0, "idle", 1))
	e.registry(t, 434, regJSON(434, "example-live", t0, "idle", 1))
	e.procs.Add(433, t0.Add(-100*time.Hour), nil)
	e.procs.Add(434, t0, nil)
	d := e.open(t)
	if got, ok := d.Obs().Reg.Counter(obs.MStaleDiscovery, nil); !ok || got != 0 {
		t.Fatalf("the series is not registered at zero: %v %v", got, ok)
	}
	if _, ok := d.Obs().Reg.Counter(obs.MConversationEnd, map[string]string{"registry_present": "true", "exit_observed": "false", "clear_entry": "true", "exit_entry": "false"}); !ok {
		t.Fatal("conversation_end_total is not registered at zero for every class")
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	e.settle(t)
	waitFor(t, "startup event", func() bool { return strings.Contains(e.log.String(), obs.EvStartupStale) })
	if got := counter(d, obs.MStaleDiscovery, nil); got != 3 {
		t.Fatalf("registry_stale_at_discovery_total = %v, want 3", got)
	}
	if !strings.Contains(e.log.String(), "startup_stale_registries") || !strings.Contains(e.log.String(), `"n":"3"`) {
		t.Fatalf("log %s", e.log.String())
	}
	for pid, want := range map[int64]bool{431: true, 432: true, 433: true, 434: false} {
		if got := show(t, d, pid); got.RegistryStaleAtDiscovery != want {
			t.Errorf("pid %d stale %v, want %v", pid, got.RegistryStaleAtDiscovery, want)
		}
	}
	// A second discovery of the same files sets no new fact.
	_ = d.Close()
	d2 := e.open(t)
	if err := d2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	e.settle(t)
	if got := counter(d2, obs.MStaleDiscovery, nil); got != 0 {
		t.Fatalf("second discovery added %v stale facts", got)
	}
}

func TestEsrchOnRegisteringAWatchIsTheRecordedFact(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.GraceWindow = 5 * time.Millisecond
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: goneOnWatch{e.procs}})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	e.procs.Add(421, t0, nil)
	e.registry(t, 421, regJSON(421, "example-esrch", t0, "idle", 1))
	e.settle(t)
	row := waitExit(t, d, 421)
	e.settle(t)
	if row.ExitKind != store.ExitUnobserved || row.Stale {
		t.Fatalf("row %+v", row)
	}
	c, werr := d.opConversationShow(context.Background(), []byte(`{"session_id":"example-esrch"}`))
	if werr != nil {
		t.Fatal(werr)
	}
	if ev := c.(lifecycle.Conversation).Evidence; lifecycle.EndLabel(ev) != lifecycle.LabelUnknown {
		t.Fatalf("evidence %+v", ev)
	}
}

func TestTheExitWatchRecordsTheInstantAndThePresenceOfTheRegistryFile(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.GraceWindow = 5 * time.Millisecond
	d := e.started(t)
	for _, pid := range []int{411, 412} {
		e.procs.Add(pid, t0, nil)
		e.registry(t, pid, regJSON(pid, fmt.Sprintf("example-x%d", pid), t0, "idle", 1))
	}
	e.settle(t)
	at := t0.Add(90 * time.Second)
	e.removeRegistry(t, 412)
	e.procs.Exit(412, at)
	e.procs.Exit(411, at.Add(time.Second)) // SIGKILL: the file stays
	crashed, closed := waitExit(t, d, 411), waitExit(t, d, 412)
	if !crashed.RegistryPresent || crashed.ExitKind != store.ExitObserved || crashed.ExitNS != at.Add(time.Second).UnixNano() {
		t.Fatalf("crashed %+v", crashed)
	}
	if closed.RegistryPresent || closed.ExitKind != store.ExitObserved || closed.ExitNS != at.UnixNano() {
		t.Fatalf("closed %+v", closed)
	}
	waitFor(t, "conversation_end_total", func() bool {
		return counter(d, obs.MConversationEnd, map[string]string{"registry_present": "true", "exit_observed": "true", "clear_entry": "false", "exit_entry": "false"}) == 1 &&
			counter(d, obs.MConversationEnd, map[string]string{"registry_present": "false", "exit_observed": "true", "clear_entry": "false", "exit_entry": "false"}) == 1
	})
	if !strings.Contains(e.log.String(), "process_exit") {
		t.Fatalf("no process_exit event in %s", e.log.String())
	}
}

func TestRegistryVersionsAndTheDeletionTimeAreRecorded(t *testing.T) {
	e := newEnv(t, false)
	d := e.started(t)
	e.procs.Add(401, t0, nil)
	e.registry(t, 401, regJSON(401, "example-v", t0, "idle", 1))
	e.settle(t)
	e.registry(t, 401, regJSON(401, "example-v", t0, "busy", 2))
	e.settle(t)
	e.removeRegistry(t, 401)
	e.settle(t)
	p := show(t, d, 401)
	if len(p.Versions) != 2 || p.Versions[0].Status != "idle" || p.Versions[1].Status != "busy" {
		t.Fatalf("versions %+v", p.Versions)
	}
	if p.RegistryDeletedAt == nil || !p.Alive || p.SessionID != "example-v" || p.Name != "" {
		t.Fatalf("process %+v", p)
	}
	e.procs.Exit(401, t0.Add(time.Minute))
	waitExit(t, d, 401)
	if p = show(t, d, 401); p.Alive || !p.ExitObserved || p.RegistryPresentAtExit {
		t.Fatalf("after a clean exit %+v", p)
	}
}

func show(t *testing.T, d *Daemon, pid int64) lifecycle.Process {
	t.Helper()
	ans, werr := d.opProcessShow(context.Background(), []byte(fmt.Sprintf(`{"pid":%d}`, pid)))
	if werr != nil {
		t.Fatalf("process.show %d: %v", pid, werr)
	}
	return ans.(lifecycle.Process)
}

func counter(d *Daemon, name string, labels map[string]string) float64 {
	v, _ := d.Obs().Reg.Counter(name, labels)
	return v
}

func waitExit(t *testing.T, d *Daemon, pid int64) store.Proc {
	t.Helper()
	var got store.Proc
	waitFor(t, fmt.Sprintf("exit of %d", pid), func() bool {
		rows, _ := d.Store().Procs()
		for _, r := range rows {
			if r.Pid == pid && r.ExitNS > 0 {
				got = r
				return true
			}
		}
		return false
	})
	return got
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

func TestHelloAndTheDaemonFeedOperations(t *testing.T) {
	e := newEnv(t, false)
	e.write(t, "projects/example-proj/example-session-1.jsonl", transcript)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	e.settle(t)
	h := d.hello().(wire.Hello)
	if h.ProtocolVersion != wire.ProtocolVersion || h.SchemaVersion < 1 || h.Epoch < 1 || h.Service == "" {
		t.Fatalf("hello %+v", h)
	}
	want := []string{"conversation.list", "conversation.show", "coverage", "doctor", "extract", "feed", "held", "hello", "process.list", "process.show", "query", "raw", "remove", "search", "snapshot", "status", "where"}
	if strings.Join(h.Capabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("capabilities %v, want %v", h.Capabilities, want)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !d.backfillDone.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h2 := d.hello().(wire.Hello); !h2.CaughtUp || h2.Degraded {
		t.Fatalf("after backfill: %+v", h2)
	}
}

func TestRemovalOperationsOverTheSocket(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"n\":1}\n", time.Now())
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	c := client.New(e.cfg.Socket, "example-check")
	var rep settle.RemoveReport
	if err := c.Post(context.Background(), "remove", RemoveRequest{DryRun: true}, &rep); err != nil || rep.Enabled || !rep.DryRun {
		t.Fatalf("dry run while disabled: %v %+v", err, rep)
	}
	err := c.Post(context.Background(), "remove", RemoveRequest{Sessions: []string{"0a1b2c3d-0000-4000-8000-000000000001"}}, &rep)
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrRemovalDisabled {
		t.Fatalf("remove while disabled: %v", err)
	}
	var held HeldAnswer
	if err := c.Post(context.Background(), "held", HeldRequest{}, &held); err != nil || len(held.Held) != 0 {
		t.Fatalf("held: %v %+v", err, held)
	}
	err = c.Post(context.Background(), "where", WhereRequest{Session: "0a1b2c3d-0000-4000-8000-000000000001"}, &settle.WhereInfo{})
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrNotFound {
		t.Fatalf("where unknown: %v", err)
	}
	err = c.Post(context.Background(), "extract", ExtractRequest{Session: "0a1b2c3d-0000-4000-8000-000000000001"}, &settle.ExtractResult{})
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrNotFound {
		t.Fatalf("extract unknown: %v", err)
	}
	var doc settle.DoctorResult
	if err := c.Post(context.Background(), "doctor", struct{}{}, &doc); err != nil || len(doc.Mismatches) != 0 {
		t.Fatalf("doctor: %v %+v", err, doc)
	}
}

// doctor runs from a timer inside the daemon (doctor.interval), writes its result
// file and exports it, with no external scheduler.
func TestDoctorRunsFromATimerInsideTheDaemon(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"n\":1}\n", time.Now())
	e.cfg.DoctorInterval = 50 * time.Millisecond
	d := e.open(t)
	if v, ok := d.Obs().Reg.Gauge(obs.MDoctorLastRun, nil); !ok || v != 0 {
		t.Fatalf("doctor_last_run_timestamp not registered at zero: %v %v", v, ok)
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	waitFor(t, "a doctor run", func() bool {
		_, ok := settle.LoadDoctor(e.cfg.StateDir)
		return ok
	})
	waitFor(t, "the doctor gauges", func() bool {
		v, _ := d.Obs().Reg.Gauge(obs.MDoctorLastRun, nil)
		return v > 0
	})
	if v, ok := d.Obs().Reg.Gauge(obs.MDoctorMismatchFiles, nil); !ok || v != 0 {
		t.Fatalf("doctor_mismatch_files %v %v", v, ok)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.StateDir, settle.DoctorFile)); err != nil {
		t.Fatal(err)
	}
}

func (e *env) removeRegistry(t *testing.T, pid int) {
	t.Helper()
	if err := os.Remove(filepath.Join(e.claude, "sessions", fmt.Sprintf("%d.json", pid))); err != nil {
		t.Fatal(err)
	}
}

func (e *env) started(t *testing.T) *Daemon {
	t.Helper()
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// goneOnWatch is a process table whose watch registration fails with ESRCH
// although the process looked alive a moment earlier.
type goneOnWatch struct{ *proc.Fake }

func (goneOnWatch) Watch(int, func(time.Time)) (func(), error) { return nil, proc.ErrGone }

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
