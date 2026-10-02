package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
)

// Tests for the TUI's queue-log compaction action (bead pg2-maxn1, logcompact.go):
// c = dry run first, y = confirmed compaction.

// compactPoller is a Poller that can also compact: it records each CompactLog call.
type compactPoller struct {
	stubPoller
	calls []bool // dryRun of each call, in order
	view  func(dryRun bool) (core.LogCompactView, error)
}

func (p *compactPoller) CompactLog(_ context.Context, dryRun bool) (core.LogCompactView, error) {
	p.calls = append(p.calls, dryRun)
	return p.view(dryRun)
}

func f64(v float64) *float64 { return &v }

func shrinkingView(dryRun bool) (core.LogCompactView, error) {
	return core.LogCompactView{
		SchemaVersion: "1", DryRun: dryRun, Compacted: !dryRun, Via: core.ViaDaemon,
		BytesBefore: 33282518, BytesAfter: 4096, RecordsBefore: 182157, RecordsAfter: 9,
		EventsKept: 2, EventsDropped: 14106, GatesKept: 1,
		LimitBytes: 67108864, PercentBefore: f64(49.6), PercentAfter: f64(0),
	}, nil
}

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// runCmd executes cmd and feeds its message back into m.Update.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	_, next := m.Update(cmd())
	_ = next
}

func modalText(m *Model) string {
	m.width, m.height = 120, 40
	return m.View()
}

// c opens the modal and runs a DRY RUN only; nothing is compacted until y.
func TestLogCompact_COpensModalAndDryRuns(t *testing.T) {
	p := &compactPoller{view: shrinkingView}
	m := newTestModel(p)
	m.screen = screenMain

	_, cmd := m.Update(keyRunes("c"))
	if m.screen != screenModal || m.activeModal != ModalLogCompact || m.logCompact.phase != lcPlanning {
		t.Fatalf("after c: screen=%v modal=%v phase=%v", m.screen, m.activeModal, m.logCompact.phase)
	}
	if !strings.Contains(modalText(m), "asking the core what a compaction would do (nothing is changed)") {
		t.Fatalf("planning view:\n%s", modalText(m))
	}
	runCmd(t, m, cmd)
	if len(p.calls) != 1 || !p.calls[0] {
		t.Fatalf("calls = %v, want exactly one DRY-RUN call", p.calls)
	}
	if m.logCompact.phase != lcReady {
		t.Fatalf("phase = %v, want ready", m.logCompact.phase)
	}
	out := modalText(m)
	for _, want := range []string{
		"Compact queue log", "31.7 MiB (49.6% of 64.0 MiB) · 182157 records", "4.0 KiB (0.0% of 64.0 MiB) · 9 records",
		"2 kept · 14106 dropped (evicted) · gates kept 1", "dry run only: nothing has been changed yet", "[y] compact now", "none since the core started",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ready view lacks %q:\n%s", want, out)
		}
	}
}

// y runs the real compaction only from the ready state; the result is shown and
// flashed.
func TestLogCompact_YConfirmsAfterDryRun(t *testing.T) {
	p := &compactPoller{view: shrinkingView}
	m := newTestModel(p)
	_, cmd := m.Update(keyRunes("c"))
	runCmd(t, m, cmd)

	_, cmd = m.Update(keyRunes("y"))
	if m.logCompact.phase != lcRunning {
		t.Fatalf("phase after y = %v, want running", m.logCompact.phase)
	}
	runCmd(t, m, cmd)
	if len(p.calls) != 2 || p.calls[0] != true || p.calls[1] != false {
		t.Fatalf("calls = %v, want [dry-run, real]", p.calls)
	}
	if m.logCompact.phase != lcDone {
		t.Fatalf("phase = %v, want done", m.logCompact.phase)
	}
	if !strings.Contains(m.flash, "log compacted: 31.7 MiB -> 4.0 KiB") || m.flashLevel != FlashInfo {
		t.Fatalf("flash = %q (%v)", m.flash, m.flashLevel)
	}
	if out := modalText(m); !strings.Contains(out, "compacted") || strings.Contains(out, "[y] compact now") {
		t.Fatalf("done view:\n%s", out)
	}
	// A second y is inert: the compaction is done.
	if _, cmd := m.Update(keyRunes("y")); cmd != nil {
		t.Fatal("y after the compaction ran must do nothing")
	}
}

// y is inert anywhere but the ready Compact modal: outside any modal, in another
// modal, while planning, and when the dry run found nothing to do.
func TestLogCompact_YIsInertUnlessReady(t *testing.T) {
	newReady := func() (*Model, *compactPoller) {
		p := &compactPoller{view: shrinkingView}
		m := newTestModel(p)
		_, cmd := m.Update(keyRunes("c"))
		runCmd(t, m, cmd)
		p.calls = nil
		return m, p
	}
	for _, tc := range []struct {
		name string
		prep func(m *Model)
	}{
		{"no modal", func(m *Model) { m.activeModal = ModalNone }},
		{"gates modal", func(m *Model) { m.activeModal = ModalGates }},
		{"still planning", func(m *Model) { m.logCompact.phase = lcPlanning }},
		{"already running", func(m *Model) { m.logCompact.phase = lcRunning }},
		{"nothing to do", func(m *Model) { m.logCompact.plan.NoProgress = true }},
		{"failed", func(m *Model) { m.logCompact.phase = lcFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, p := newReady()
			tc.prep(m)
			if cmd := handleConfirmLogCompact(m); cmd != nil {
				t.Fatal("y returned a command")
			}
			if len(p.calls) != 0 {
				t.Fatalf("y triggered a call: %v", p.calls)
			}
		})
	}
}

// A dry run that finds no progress possible says so and offers no confirmation.
func TestLogCompact_NothingToDoOffersNoConfirm(t *testing.T) {
	p := &compactPoller{view: func(dry bool) (core.LogCompactView, error) {
		return core.LogCompactView{SchemaVersion: "1", DryRun: dry, NoProgress: true, Via: core.ViaDaemon, BytesBefore: 900, BytesAfter: 900, RecordsBefore: 4, RecordsAfter: 4, EventsKept: 3}, nil
	}}
	m := newTestModel(p)
	_, cmd := m.Update(keyRunes("c"))
	runCmd(t, m, cmd)
	out := modalText(m)
	if !strings.Contains(out, "nothing to do: a compaction would not shrink the log") || strings.Contains(out, "[y] compact now") {
		t.Fatalf("view:\n%s", out)
	}
}

// A failed call, an unavailable Poller, and a stale reply.
func TestLogCompact_FailureUnavailableAndStale(t *testing.T) {
	// Failure: shown in the modal and flashed as a warning.
	p := &compactPoller{view: func(bool) (core.LogCompactView, error) {
		return core.LogCompactView{}, errors.New("core refused: log is busy")
	}}
	m := newTestModel(p)
	_, cmd := m.Update(keyRunes("c"))
	runCmd(t, m, cmd)
	if m.logCompact.phase != lcFailed || m.flashLevel != FlashWarn || !strings.Contains(m.flash, "log is busy") {
		t.Fatalf("phase=%v flash=%q level=%v", m.logCompact.phase, m.flash, m.flashLevel)
	}
	if out := modalText(m); !strings.Contains(out, "core refused: log is busy") {
		t.Fatalf("failure view:\n%s", out)
	}

	// A Poller with no CompactLog (every older double) fails the modal at once.
	m = newTestModel(&stubPoller{})
	if _, cmd := m.Update(keyRunes("c")); cmd != nil {
		t.Fatal("an unavailable poller must not return a command")
	}
	if out := modalText(m); !strings.Contains(out, "unavailable: no running core to ask") {
		t.Fatalf("unavailable view:\n%s", out)
	}
	m = newTestModel(nil)
	if _, cmd := m.Update(keyRunes("c")); cmd != nil || m.logCompact.phase != lcFailed {
		t.Fatal("a nil poller must fail the modal without a command")
	}

	// Stale: a reply for a modal the operator left and re-opened is ignored.
	p = &compactPoller{view: shrinkingView}
	m = newTestModel(p)
	_, first := m.Update(keyRunes("c"))
	stale := first() // the first dry run's reply, delivered late
	m.Update(keyRunes("c"))
	m.Update(stale)
	if m.logCompact.phase != lcPlanning {
		t.Fatalf("a stale reply changed the phase to %v", m.logCompact.phase)
	}
}

// esc closes the modal; opening it again starts over with a fresh dry run.
func TestLogCompact_EscClosesAndReopenResets(t *testing.T) {
	p := &compactPoller{view: shrinkingView}
	m := newTestModel(p)
	m.screen = screenMain
	_, cmd := m.Update(keyRunes("c"))
	runCmd(t, m, cmd)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenMain || m.activeModal != ModalNone {
		t.Fatalf("after esc: screen=%v modal=%v", m.screen, m.activeModal)
	}
	_, cmd = m.Update(keyRunes("c"))
	if m.logCompact.phase != lcPlanning {
		t.Fatalf("reopen: phase = %v, want planning (a fresh dry run)", m.logCompact.phase)
	}
	runCmd(t, m, cmd)
	if len(p.calls) != 2 || !p.calls[0] || !p.calls[1] {
		t.Fatalf("calls = %v, want two dry runs and no real one", p.calls)
	}
}

// The modal shows the core's last compaction, and the keys are in the help.
func TestLogCompact_ShowsLastCompactionAndIsInHelp(t *testing.T) {
	m := newTestModel(&compactPoller{view: shrinkingView})
	m.reply.QueueLog.LastCompaction = &LastCompaction{
		At: time.Date(2026, 10, 2, 13, 4, 5, 0, time.UTC), Trigger: "threshold",
		BytesBefore: 8650752, BytesAfter: 3808, RecordsBefore: 40213, RecordsAfter: 9, DurationMs: 41,
	}
	_, cmd := m.Update(keyRunes("c"))
	runCmd(t, m, cmd)
	out := modalText(m)
	for _, want := range []string{"(threshold): 8.2 MiB -> 3.7 KiB in 41ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("modal lacks %q:\n%s", want, out)
		}
	}
	help := ""
	for _, r := range bindingsToHelpRows() {
		help += r.Keys + " " + r.Description + "\n"
	}
	for _, want := range []string{"c Compact the queue log", "y Confirm the compaction"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
}

// The status reply's queueLog.lastCompaction decodes into QueueLog.
func TestStatusReply_DecodesLastCompaction(t *testing.T) {
	var r StatusReply
	if err := json.Unmarshal([]byte(`{"queueLog":{"bytes":1,"lastCompaction":{"at":"2026-09-01T00:00:00Z","trigger":"manual","bytesBefore":9,"bytesAfter":3,"recordsBefore":5,"recordsAfter":2,"durationMs":7}}}`), &r); err != nil {
		t.Fatal(err)
	}
	lc := r.QueueLog.LastCompaction
	if lc == nil || lc.Trigger != "manual" || lc.BytesAfter != 3 || lc.DurationMs != 7 {
		t.Fatalf("lastCompaction = %+v", lc)
	}
}

// SocketPoller.CompactLog speaks the log-compact verb: a dry run sends dryRun,
// a real run does not, and the reply decodes into the shared view.
func TestSocketPoller_CompactLog(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	var gotDry []bool
	startFakeCore(t, socketPath, func(subcommand string, payload json.RawMessage) ([]byte, int) {
		if subcommand != core.SubcommandLogCompact {
			return []byte(`{"schemaVersion":"1","error":"unexpected subcommand"}`), conformance.ExitError
		}
		var req struct {
			DryRun bool `json:"dryRun"`
		}
		_ = json.Unmarshal(payload, &req)
		gotDry = append(gotDry, req.DryRun)
		if req.DryRun {
			return []byte(`{"schemaVersion":"1","dryRun":true,"compacted":false,"noProgress":false,"via":"daemon","bytesBefore":900,"bytesAfter":100,"recordsBefore":30,"recordsAfter":3,"eventsKept":1,"eventsDropped":9,"gatesKept":1}`), conformance.ExitOK
		}
		return []byte(`{"schemaVersion":"1","dryRun":false,"compacted":true,"noProgress":false,"via":"daemon","bytesBefore":900,"bytesAfter":100,"recordsBefore":30,"recordsAfter":3,"eventsKept":1,"eventsDropped":9,"gatesKept":1,"durationMs":4}`), conformance.ExitOK
	})
	writeDiscoveryRecord(t, logDir, socketPath, "tok")
	p := NewSocketPoller(logDir, core.Ref{})

	dry, err := p.CompactLog(context.Background(), true)
	if err != nil || !dry.DryRun || dry.BytesAfter != 100 || dry.EventsDropped != 9 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	real, err := p.CompactLog(context.Background(), false)
	if err != nil || !real.Compacted || real.DurationMs == nil || *real.DurationMs != 4 {
		t.Fatalf("real run = %+v, %v", real, err)
	}
	if len(gotDry) != 2 || !gotDry[0] || gotDry[1] {
		t.Fatalf("dryRun sent as %v, want [true false]", gotDry)
	}

	// A failure does not touch Snapshot's backoff ladder.
	bad := NewSocketPoller(shortTestDir(t), core.Ref{})
	if _, err := bad.CompactLog(context.Background(), true); err == nil {
		t.Fatal("CompactLog with no core discoverable returned nil error")
	}
	bad.mu.Lock()
	backoff := bad.backoff
	bad.mu.Unlock()
	if backoff != 0 {
		t.Fatalf("backoff = %v after a CompactLog failure, want 0", backoff)
	}
}
