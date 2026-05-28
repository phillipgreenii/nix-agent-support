package daemon

import (
	"encoding/json"
	"os"
	"time"
)

// RuntimeState is the on-disk shape of $XDG_STATE_HOME/pa-monitor/runtime.json.
// Holds toggles that should survive a daemon restart, e.g. user-requested
// caffeinate. Persisted via atomic write (write-tmp + rename).
type RuntimeState struct {
	CaffeinateOn      bool        `json:"caffeinate_on"`
	AutoResumeEnabled bool        `json:"auto_resume_enabled,omitempty"`
	Nudger            NudgerState `json:"nudger,omitempty"`
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
	LastDisruptNudgeAt  time.Time `json:"last_disrupt_nudge_at,omitempty"`
	LastDisruptNudgeFor time.Time `json:"last_disrupt_nudge_for,omitempty"`
	DisruptEscalated    bool      `json:"disrupt_escalated,omitempty"`
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
