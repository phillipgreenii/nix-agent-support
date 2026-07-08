package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// RuntimeState is the on-disk shape of $XDG_STATE_HOME/pa-monitor/runtime.json.
// Holds nudger watermarks + pending intents that should survive a daemon
// restart. Persisted via atomic write (write-tmp + rename).
//
// User toggles (caffeinate_on, auto_resume_enabled) are NOT stored here: the
// ToggleStore (SQLite) is their single source of truth since the runtime.json
// -> SQLite migration. Storing them here too caused a split-brain where the
// daemon read a stale file value instead of the DB. The one-shot migration
// still reads the legacy toggle keys from any pre-migration runtime.json — see
// legacyRuntimeToggles in runtime_migration.go.
type RuntimeState struct {
	Nudger NudgerState `json:"nudger,omitempty"`
}

// NudgerState holds all nudger-related fields that must survive a restart.
type NudgerState struct {
	PendingIntents      []PersistedIntent                  `json:"pending_intents,omitempty"`
	Sessions            map[string]NudgerSessionWatermarks `json:"sessions,omitempty"`
	WindowResetFiredFor time.Time                          `json:"window_reset_fired_for,omitempty"`
}

// PersistedIntent is a nudge intent that was queued but not yet delivered.
type PersistedIntent struct {
	SessionID string    `json:"session_id"`
	Source    string    `json:"source"`
	Text      string    `json:"text,omitempty"`
	EmittedAt time.Time `json:"emitted_at"`
	CauseKind string    `json:"cause_kind,omitempty"`
	CauseAt   time.Time `json:"cause_at,omitempty"`
}

// NudgerSessionWatermarks tracks per-session nudge timing to prevent double-nudge after restart.
type NudgerSessionWatermarks struct {
	LastNudgedAt        time.Time `json:"last_nudged_at,omitempty"`
	LastNudgeSources    []string  `json:"last_nudge_sources,omitempty"`
	LastDisruptNudgeAt  time.Time `json:"last_disrupt_nudge_at,omitempty"`
	LastDisruptNudgeFor time.Time `json:"last_disrupt_nudge_for,omitempty"`
	DisruptEscalated    bool      `json:"disrupt_escalated,omitempty"`
	// LastDisruptAttemptAt is the watermark of the most recent disrupt-nudge
	// ATTEMPT (recorded on BOTH success and failure, unlike LastDisruptNudgeAt
	// which records only on success). It is the keep-awake authority for D5:
	// a terminal nudgeable error with zero recorded attempts holds caffeinate
	// awake until the first attempt, then releases. A failed attempt counts.
	LastDisruptAttemptAt time.Time `json:"last_disrupt_attempt_at,omitempty"`
}

// ReadRuntimeState reads the file at path. A missing file is not an
// error; an empty RuntimeState is returned.
func ReadRuntimeState(path string) (RuntimeState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeState{}, nil
		}
		return RuntimeState{}, err
	}
	var s RuntimeState
	if err := json.Unmarshal(b, &s); err != nil {
		return RuntimeState{}, err
	}
	return s, nil
}

// WriteRuntimeState writes s to path atomically (write-to-tmp + rename).
func WriteRuntimeState(path string, s RuntimeState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WatermarkStore persists nudger watermarks + pending intents inside
// runtime.json. Implements nudger.WatermarkView and nudger.Recorder so
// the nudger package stays free of daemon-specific persistence types.
type WatermarkStore struct {
	mu      sync.Mutex
	path    string
	state   RuntimeState
	emitter *otel.Emitter
	// autoResumeEnabled is the live auto-resume toggle. Its persistent home is
	// the ToggleStore (DB), written by the SetAutoResume RPC; this field is the
	// in-memory value seeded from the DB at startup (RunWith ->
	// SetAutoResumeEnabled). NOT persisted to runtime.json.
	autoResumeEnabled bool
}

// Compile-time interface checks.
var (
	_ nudger.WatermarkView = (*WatermarkStore)(nil)
	_ nudger.Recorder      = (*WatermarkStore)(nil)
)

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
	// Defensive copy of the sources slice so callers can't mutate the
	// store's internal state via aliasing.
	var sources []string
	if len(x.LastNudgeSources) > 0 {
		sources = append([]string(nil), x.LastNudgeSources...)
	}
	return nudger.SessionWatermark{
		LastNudgedAt:         x.LastNudgedAt,
		LastNudgeSources:     sources,
		LastDisruptNudgeAt:   x.LastDisruptNudgeAt,
		LastDisruptNudgeFor:  x.LastDisruptNudgeFor,
		DisruptEscalated:     x.DisruptEscalated,
		LastDisruptAttemptAt: x.LastDisruptAttemptAt,
	}
}

// --- nudger.Recorder ---

func (w *WatermarkStore) RecordQueued(sid string, source nudger.Source) {
	if w.emitter == nil {
		return
	}
	w.emitter.RecordNudgeQueued(map[string]string{
		"session_id": sid,
		"source":     string(source),
	})
}

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

// RecordDroppedNoBridge implements nudger.Recorder. It reports a permanent
// give-up on a no-bridge intent group (aged past the dispatcher's
// noBridgeDropWindow with no live cmux-bridge to retry against) by
// incrementing pa_monitor.nudge.dropped_no_bridge_total and emitting the
// nudge.dropped_no_bridge log event — the one delivery outcome distinct from
// sent/suppressed/send-failed, all of which already have counters.
func (w *WatermarkStore) RecordDroppedNoBridge(sid string, sources []nudger.Source) {
	if w.emitter == nil {
		return
	}
	w.emitter.RecordNudgeDroppedNoBridge(map[string]string{
		"session_id": sid,
		"sources":    joinSources(sources),
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
		"session_id": sid,
		"sources":    joinSources(sources),
		"error_kind": errorKind,
		"escalated":  escalatedStr,
	})
}

// RecordSendFailed implements nudger.Recorder. It surfaces a failed nudge
// delivery to OTel (pa_monitor.signal.send_failures_total + nudge.send_failed
// log event) so swallowed Signaler.Send errors stop being invisible.
//
// The counter carries only bounded labels (error_kind + a coarse reason) to
// cap series cardinality; the per-session id and the full error string are
// log-only — putting them on the counter would create a new time series per
// session and per distinct error string.
func (w *WatermarkStore) RecordSendFailed(sid string, sources []nudger.Source, errorKind, errText string) {
	if w.emitter == nil {
		return
	}
	w.emitter.RecordNudgeSendFailed(
		sendFailureCounterAttrs(errorKind, errText),
		map[string]string{
			"session_id": sid,
			"sources":    joinSources(sources),
			"error_kind": errorKind,
			"reason":     classifySendFailure(errText),
			"error":      errText,
		},
	)
}

// sendFailureCounterAttrs returns the BOUNDED label set for the
// send_failures_total counter. It deliberately omits session_id and the raw
// error string (both unbounded) and instead carries a coarse, fixed-cardinality
// reason derived from the error text.
func sendFailureCounterAttrs(errorKind, errText string) map[string]string {
	return map[string]string{
		"error_kind": errorKind,
		"reason":     classifySendFailure(errText),
	}
}

// classifySendFailure maps a Signaler.Send error string onto a small,
// fixed set of reason codes so the send_failures_total counter stays
// bounded. The full error text is preserved on the nudge.send_failed log
// event for debugging.
func classifySendFailure(errText string) string {
	s := strings.ToLower(errText)
	switch {
	case s == "":
		return "unknown"
	case strings.Contains(s, "no cmux surface"),
		strings.Contains(s, "no surface"),
		strings.Contains(s, "surface not found"):
		return "no_surface"
	case strings.Contains(s, "send-key"),
		strings.Contains(s, "send key"):
		return "send_key"
	case strings.Contains(s, "enumerate"):
		return "enumerate"
	case strings.Contains(s, "timeout"),
		strings.Contains(s, "deadline exceeded"),
		// exec.CommandContext SIGKILLs a cmux subprocess whose context
		// deadline expires; the ExitError then renders as "signal: killed".
		// This is the same root cause as "deadline exceeded" (and the more
		// common of the two in practice — see cache/signal-errors.log), so
		// it must land in "timeout" rather than falling through to "other".
		strings.Contains(s, "signal: killed"):
		return "timeout"
	case strings.Contains(s, "connection"),
		strings.Contains(s, "connect"),
		strings.Contains(s, "broken pipe"),
		strings.Contains(s, "refused"):
		return "connection"
	default:
		return "other"
	}
}

func (w *WatermarkStore) UpdateWatermarks(sid string, now time.Time, sources []nudger.Source, cause *transcript.ErrorRecord, escalated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wm := w.state.Nudger.Sessions[sid]
	wm.LastNudgedAt = now
	// Materialize sources as []string for json persistence. Sorted for
	// stability with joinSources (and so disk diffs stay small).
	wm.LastNudgeSources = sortedSourceStrings(sources)
	if cause != nil {
		wm.LastDisruptNudgeAt = now
		wm.LastDisruptNudgeFor = cause.At
	}
	wm.DisruptEscalated = escalated
	w.state.Nudger.Sessions[sid] = wm
	_ = WriteRuntimeState(w.path, w.state) // best-effort
}

// sortedSourceStrings returns a sorted []string copy of sources. Returns nil
// when sources is empty so the json `omitempty` tag can elide the field.
func sortedSourceStrings(sources []nudger.Source) []string {
	if len(sources) == 0 {
		return nil
	}
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = string(s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// SetDisruptEscalated implements nudger.WatermarkView. It persists the
// escalation flag for sid into runtime.json (best-effort).
func (w *WatermarkStore) SetDisruptEscalated(sid string, escalated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Nudger.Sessions == nil {
		w.state.Nudger.Sessions = map[string]NudgerSessionWatermarks{}
	}
	wm := w.state.Nudger.Sessions[sid]
	wm.DisruptEscalated = escalated
	w.state.Nudger.Sessions[sid] = wm
	_ = WriteRuntimeState(w.path, w.state)
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

// RecordDisruptAttempt implements nudger.Recorder. It stamps the
// LastDisruptAttemptAt watermark for sid (best-effort persist). Called by the
// dispatcher on BOTH the success and failure path of a disrupt nudge so the
// D5 error keep-awake releases after the first attempt.
func (w *WatermarkStore) RecordDisruptAttempt(sid string, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Nudger.Sessions == nil {
		w.state.Nudger.Sessions = map[string]NudgerSessionWatermarks{}
	}
	wm := w.state.Nudger.Sessions[sid]
	wm.LastDisruptAttemptAt = at
	w.state.Nudger.Sessions[sid] = wm
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) AutoResumeEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.autoResumeEnabled
}

// SetAutoResumeEnabled updates the in-memory toggle. Persistence is the
// ToggleStore (DB), written by the SetAutoResume RPC; this is NOT persisted to
// runtime.json. Called both to seed the value from the DB at startup and to
// apply a live RPC toggle.
func (w *WatermarkStore) SetAutoResumeEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.autoResumeEnabled = enabled
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
