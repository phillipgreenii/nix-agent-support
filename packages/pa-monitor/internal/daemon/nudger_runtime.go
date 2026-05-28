package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// WatermarkStore persists nudger watermarks + pending intents inside
// runtime.json. Implements nudger.WatermarkView and nudger.Recorder so
// the nudger package stays free of daemon-specific persistence types.
type WatermarkStore struct {
	mu      sync.Mutex
	path    string
	state   RuntimeState
	emitter *otel.Emitter
}

// Compile-time interface checks.
var _ nudger.WatermarkView = (*WatermarkStore)(nil)
var _ nudger.Recorder = (*WatermarkStore)(nil)

func NewWatermarkStore(path string, emitter *otel.Emitter) (*WatermarkStore, error) {
	s, err := ReadRuntimeState(path)
	if err != nil {
		return nil, err
	}
	if s.Nudger.Sessions == nil {
		s.Nudger.Sessions = map[string]NudgerSessionWatermarks{}
	}
	return &WatermarkStore{path: path, state: s, emitter: emitter}, nil
}

// joinSources returns a comma-joined string of source names, sorted for
// label stability.
func joinSources(sources []nudger.Source) string {
	strs := make([]string, len(sources))
	for i, s := range sources {
		strs[i] = string(s)
	}
	// Sort for label stability (simple insertion sort — sources are tiny).
	for i := 1; i < len(strs); i++ {
		for j := i; j > 0 && strs[j-1] > strs[j]; j-- {
			strs[j-1], strs[j] = strs[j], strs[j-1]
		}
	}
	return strings.Join(strs, ",")
}

// --- nudger.WatermarkView ---

func (w *WatermarkStore) WindowResetFiredFor() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state.Nudger.WindowResetFiredFor
}

func (w *WatermarkStore) SessionWatermark(sid string) nudger.SessionWatermark {
	w.mu.Lock()
	defer w.mu.Unlock()
	x := w.state.Nudger.Sessions[sid]
	return nudger.SessionWatermark{
		LastNudgedAt:        x.LastNudgedAt,
		LastDisruptNudgeAt:  x.LastDisruptNudgeAt,
		LastDisruptNudgeFor: x.LastDisruptNudgeFor,
		DisruptEscalated:    x.DisruptEscalated,
	}
}

// --- nudger.Recorder ---

func (w *WatermarkStore) RecordSuppressed(sid string, sources []nudger.Source, cause string) {
	if w.emitter == nil {
		return
	}
	w.emitter.RecordNudgeSuppressed(map[string]string{
		"session_id": sid,
		"sources":    joinSources(sources),
		"cause":      cause,
	})
}

func (w *WatermarkStore) RecordSent(sid string, sources []nudger.Source, errorKind string, escalated bool) {
	if w.emitter == nil {
		return
	}
	escalatedStr := "false"
	if escalated {
		escalatedStr = "true"
	}
	w.emitter.RecordNudgeSent(map[string]string{
		"session_id":  sid,
		"sources":     joinSources(sources),
		"error_kind":  errorKind,
		"escalated":   escalatedStr,
	})
}

func (w *WatermarkStore) UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wm := w.state.Nudger.Sessions[sid]
	wm.LastNudgedAt = now
	if cause != nil {
		wm.LastDisruptNudgeAt = now
		wm.LastDisruptNudgeFor = cause.At
	}
	wm.DisruptEscalated = escalated
	w.state.Nudger.Sessions[sid] = wm
	_ = WriteRuntimeState(w.path, w.state) // best-effort
}

// --- additional store API used by daemon ---

func (w *WatermarkStore) SetWindowResetFiredFor(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Nudger.WindowResetFiredFor = at
	_ = WriteRuntimeState(w.path, w.state)
}

// AdvanceWindowResetFiredFor implements nudger.Recorder. It delegates to
// SetWindowResetFiredFor so the latch advances only when the dispatcher
// actually fires a SourceWindowReset nudge.
func (w *WatermarkStore) AdvanceWindowResetFiredFor(at time.Time) {
	w.SetWindowResetFiredFor(at)
}

func (w *WatermarkStore) AutoResumeEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state.AutoResumeEnabled
}

func (w *WatermarkStore) SetAutoResumeEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.AutoResumeEnabled = enabled
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) SaveIntents(intents []nudger.NudgeIntent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PersistedIntent, 0, len(intents))
	for _, in := range intents {
		p := PersistedIntent{
			SessionID: in.Key.SessionID,
			Source:    string(in.Key.Source),
			Text:      in.Text,
			EmittedAt: in.EmittedAt,
		}
		if in.Cause != nil {
			p.CauseKind = string(in.Cause.Kind)
			p.CauseAt = in.Cause.At
		}
		out = append(out, p)
	}
	w.state.Nudger.PendingIntents = out
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) LoadIntents() []nudger.NudgeIntent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]nudger.NudgeIntent, 0, len(w.state.Nudger.PendingIntents))
	for _, p := range w.state.Nudger.PendingIntents {
		in := nudger.NudgeIntent{
			Key:       nudger.IntentKey{SessionID: p.SessionID, Source: nudger.Source(p.Source)},
			Text:      p.Text,
			EmittedAt: p.EmittedAt,
		}
		if p.CauseKind != "" {
			in.Cause = &transcript.ErrorRecord{
				Kind: transcript.ErrorKind(p.CauseKind), At: p.CauseAt,
			}
		}
		out = append(out, in)
	}
	return out
}
