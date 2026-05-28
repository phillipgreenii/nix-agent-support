package daemon

import (
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

// WatermarkStore persists nudger watermarks + pending intents inside
// runtime.json. Implements nudger.WatermarkView and nudger.Recorder so
// the nudger package stays free of daemon-specific persistence types.
type WatermarkStore struct {
	mu    sync.Mutex
	path  string
	state RuntimeState
}

// Compile-time interface checks.
var _ nudger.WatermarkView = (*WatermarkStore)(nil)
var _ nudger.Recorder = (*WatermarkStore)(nil)

func NewWatermarkStore(path string) (*WatermarkStore, error) {
	s, err := ReadRuntimeState(path)
	if err != nil {
		return nil, err
	}
	if s.Nudger.Sessions == nil {
		s.Nudger.Sessions = map[string]NudgerSessionWatermarks{}
	}
	return &WatermarkStore{path: path, state: s}, nil
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
	// Observability hook; persistence-wise this is a no-op.
	// OTel emission is wired in Phase 9.
}

func (w *WatermarkStore) RecordSent(sid string, sources []nudger.Source, errorKind string, escalated bool) {
	// Observability hook; UpdateWatermarks is the persistence path.
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
