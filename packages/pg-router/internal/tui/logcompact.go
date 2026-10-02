package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
	"github.com/phillipgreenii/pg-router/schemas"
)

// This file is the TUI's log-compaction action (bead pg2-maxn1, the TUI half of
// pg2-8e0m6's "run a dry-run and a compaction"). It follows the precedent of
// the P/R keys (gates.go): a key fires a socket RPC through the Poller and the
// result comes back as a message — with the confirmation the operator asked for
// on the destructive step:
//
//	c  opens the Compact modal and immediately runs a DRY RUN against the core,
//	   showing what a compaction would do (nothing has changed yet);
//	y  inside that modal, only once the dry run is shown and a compaction would
//	   make progress, runs the real compaction;
//	esc  closes the modal (as for every modal).
//
// Both calls are the same `log-compact` socket verb `pg-router log compact`
// uses, so the daemon (which owns the log's lock) does the work.
//
// The header's `log:` summary is deliberately NOT widened with the last
// compaction — the banner's third line already carries gates, dispatch, size
// and the config path, and the tiny tier has no room — so the last compaction
// is shown here, in the modal, and in `pg-router status`.

// LogCompactor is the optional Poller extension the compact modal needs, an
// interface of its own (like core's RestoreObserver) so a Poller that predates it
// — every test double — keeps compiling and the modal reports "unavailable".
type LogCompactor interface {
	// CompactLog issues the log-compact socket verb: dryRun reports what a
	// compaction would do and changes nothing; otherwise it compacts.
	CompactLog(ctx context.Context, dryRun bool) (core.LogCompactView, error)
}

// logCompactPlanTimeout bounds the dry run's round trip from the Model's side
// (the same safety-net role gateToggleTimeout plays above the Poller's own
// deadline); logCompactRunTimeout the real compaction's, which does file work and
// so shares the verb's own longer call timeout.
const (
	logCompactPlanTimeout = 30 * time.Second
	logCompactRunTimeout  = core.LogCompactCallTimeout + 15*time.Second
)

// logCompactPhase is where the modal's interaction stands.
type logCompactPhase int

const (
	lcIdle     logCompactPhase = iota
	lcPlanning                 // the dry run is in flight
	lcReady                    // the dry run is shown; waiting for y or esc
	lcRunning                  // the real compaction is in flight
	lcDone                     // the real compaction finished
	lcFailed                   // a call failed
)

// logCompactState is the Compact modal's state.
type logCompactState struct {
	phase logCompactPhase
	// seq numbers the calls in flight: a result whose seq is not the current one
	// belongs to a modal the operator already left (or re-opened) and is ignored.
	seq    int
	plan   core.LogCompactView // the dry run (phase lcReady and later)
	result core.LogCompactView // the real run (phase lcDone)
	err    error               // phase lcFailed
	// unavailable is true when the Poller cannot compact at all.
	unavailable bool
}

// logCompactResultMsg carries one CompactLog call's outcome back to Update.
type logCompactResultMsg struct {
	seq    int
	dryRun bool
	view   core.LogCompactView
	err    error
}

// handleOpenLogCompact implements the "c" key: open the Compact modal and run the
// dry run.
func handleOpenLogCompact(m *Model) tea.Cmd {
	m.openModal(ModalLogCompact)
	m.logCompact.seq++
	m.logCompact = logCompactState{phase: lcPlanning, seq: m.logCompact.seq}
	return m.startLogCompact(true)
}

// handleConfirmLogCompact implements the "y" key: a no-op everywhere except
// inside the Compact modal once its dry run is shown and a compaction would make
// progress (the same "only inside its modal" contract R has for the Gates modal).
func handleConfirmLogCompact(m *Model) tea.Cmd {
	if m.activeModal != ModalLogCompact || m.logCompact.phase != lcReady || m.logCompact.plan.NoProgress {
		return nil
	}
	m.logCompact.seq++
	m.logCompact.phase = lcRunning
	return m.startLogCompact(false)
}

// startLogCompact returns the tea.Cmd that performs one CompactLog call. A Poller
// that cannot compact (or none at all) fails the modal at once instead.
func (m *Model) startLogCompact(dryRun bool) tea.Cmd {
	lc, ok := m.poller.(LogCompactor)
	if !ok {
		m.logCompact.phase = lcFailed
		m.logCompact.unavailable = true
		return nil
	}
	seq := m.logCompact.seq
	timeout := logCompactRunTimeout
	if dryRun {
		timeout = logCompactPlanTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		view, err := lc.CompactLog(ctx, dryRun)
		return logCompactResultMsg{seq: seq, dryRun: dryRun, view: view, err: err}
	}
}

// applyLogCompactResult is Update's logCompactResultMsg handler.
func (m *Model) applyLogCompactResult(msg logCompactResultMsg) tea.Cmd {
	if msg.seq != m.logCompact.seq {
		return nil // a stale call from a modal since left or re-opened
	}
	if msg.err != nil {
		m.logCompact.phase = lcFailed
		m.logCompact.err = msg.err
		m.errorLogger.LogString("log compact failed: " + msg.err.Error())
		m.setFlash("log compaction failed: "+msg.err.Error(), FlashWarn)
		return m.flashClearCmd()
	}
	if msg.dryRun {
		m.logCompact.phase = lcReady
		m.logCompact.plan = msg.view
		return nil
	}
	m.logCompact.phase = lcDone
	m.logCompact.result = msg.view
	text := "log compact: nothing to do"
	if msg.view.Compacted {
		text = fmt.Sprintf("log compacted: %s -> %s", humanBytes(msg.view.BytesBefore), humanBytes(msg.view.BytesAfter))
	}
	m.setFlash(text, FlashInfo)
	return m.flashClearCmd()
}

// renderLogCompactModal renders the Compact modal for the current phase.
func (m *Model) renderLogCompactModal() string {
	st := m.logCompact
	var rows []render.ModalRow
	footer := ""
	row := func(l, r string) { rows = append(rows, render.ModalRow{Left: l, Right: r}) }
	size := func(n int64, pct *float64) string {
		s := humanBytes(n)
		if pct != nil {
			s += fmt.Sprintf(" (%.1f%% of %s)", *pct, humanBytes(st.plan.LimitBytes))
		}
		return s
	}
	switch st.phase {
	case lcPlanning:
		row("status", "asking the core what a compaction would do (nothing is changed)...")
	case lcRunning:
		row("status", "compacting the log...")
	case lcFailed:
		if st.unavailable {
			row("status", "unavailable: no running core to ask (the TUI is not connected)")
		} else {
			row("failed", textsafe.Sanitize(st.err.Error()))
		}
	case lcReady, lcDone:
		v := st.plan
		if st.phase == lcDone {
			v = st.result
		}
		row("before", fmt.Sprintf("%s · %d records", size(v.BytesBefore, v.PercentBefore), v.RecordsBefore))
		if st.phase == lcDone && !v.Compacted {
			row("result", "nothing to do: the log already holds live state only")
		} else {
			label := "after (est.)"
			if st.phase == lcDone {
				label = "after"
			}
			row(label, fmt.Sprintf("%s · %d records", size(v.BytesAfter, v.PercentAfter), v.RecordsAfter))
		}
		row("events", fmt.Sprintf("%d kept · %d dropped (evicted) · gates kept %d", v.EventsKept, v.EventsDropped, v.GatesKept))
		if v.Torn {
			row("warning", "the log has an undecodable line; everything from it on is discarded")
		}
		switch {
		case st.phase == lcDone && v.Compacted:
			row("result", "compacted")
		case st.phase == lcReady && v.NoProgress:
			row("result", "nothing to do: a compaction would not shrink the log")
		case st.phase == lcReady:
			row("result", "dry run only: nothing has been changed yet")
			footer = "[y] compact now   (reclaims dead history only; no queued event is dropped)"
		}
	}
	if lc := m.reply.QueueLog.LastCompaction; lc != nil {
		// Short on purpose: the modal clips a Right value at its box width.
		row("last", fmt.Sprintf("%s (%s): %s -> %s in %s", lc.At.Local().Format("15:04:05"), textsafe.Sanitize(lc.Trigger),
			humanBytes(lc.BytesBefore), humanBytes(lc.BytesAfter),
			(time.Duration(lc.DurationMs)*time.Millisecond).Round(time.Millisecond)))
	} else {
		row("last", "none since the core started")
	}
	return render.Modal("Compact queue log", rows, footer, m.width, m.height, m.modalScrollOffset)
}

// CompactLog implements LogCompactor over the same dial machinery ToggleGate uses:
// its own fresh Discover+Dial+Call+Close cycle, never touching Snapshot's backoff
// ladder.
func (p *SocketPoller) CompactLog(ctx context.Context, dryRun bool) (core.LogCompactView, error) {
	p.mu.Lock()
	client, dialErr := p.dialLocked()
	p.mu.Unlock()
	if dialErr != nil {
		return core.LogCompactView{}, fmt.Errorf("tui: log compact: %w", dialErr)
	}
	defer func() { _ = client.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, core.LogCompactCallTimeout+10*time.Second)
	defer cancel()
	req := map[string]any{"schemaVersion": schemas.SchemaVersion}
	if dryRun {
		req["dryRun"] = true
	}
	payload, err := json.Marshal(req)
	if err != nil { // unreachable: JSON-safe scalars
		return core.LogCompactView{}, fmt.Errorf("tui: log compact: build request: %w", err)
	}
	reply, _, callErr := client.Call(callCtx, core.SubcommandLogCompact, payload, core.CallOptions{CallTimeout: core.LogCompactCallTimeout + 5*time.Second})
	if callErr != nil {
		return core.LogCompactView{}, fmt.Errorf("tui: log compact: %w", callErr)
	}
	var out core.LogCompactView
	if err := core.DiscriminateReply(reply, core.LogCompactReplySchema, &out); err != nil {
		return core.LogCompactView{}, fmt.Errorf("tui: log compact: %w", err)
	}
	return out, nil
}
