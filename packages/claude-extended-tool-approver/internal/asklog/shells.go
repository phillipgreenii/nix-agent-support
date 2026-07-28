package asklog

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// RegisterBackgroundShell records shellID as an agent-owned background shell.
// It upserts, so a re-used shell id refreshes its session/timestamp rather than
// erroring. Follows the same persistence store as the ask-log (no parallel
// mechanism) — see internal/asklog/store.go migration 6.
func (s *Store) RegisterBackgroundShell(shellID, sessionID string) error {
	_, err := s.db.Exec(
		`INSERT INTO background_shells (shell_id, session_id, creator, created_at)
		 VALUES (?, ?, 'agent', ?)
		 ON CONFLICT(shell_id) DO UPDATE SET session_id = excluded.session_id, created_at = excluded.created_at`,
		shellID, nilIfEmpty(sessionID), nowISO(),
	)
	return err
}

// ShellOwner returns the recorded creator of shellID and whether a record
// exists. It satisfies the killshell.ShellStore interface.
func (s *Store) ShellOwner(shellID string) (string, bool) {
	var creator string
	if err := s.db.QueryRow(
		`SELECT creator FROM background_shells WHERE shell_id = ?`, shellID,
	).Scan(&creator); err != nil {
		return "", false
	}
	return creator, true
}

// backgroundBashInput is the subset of a Bash tool_input needed to detect a
// backgrounded shell launch.
type backgroundBashInput struct {
	RunInBackground bool `json:"run_in_background"`
}

// backgroundBashResponse is the subset of a Bash PostToolUse tool_response that
// carries the launched background shell's id.
type backgroundBashResponse struct {
	ShellID string `json:"shell_id"`
}

// RegisterBackgroundShellFromPost inspects a PostToolUse event and, when it is a
// Bash call launched with run_in_background whose response carries a shell id,
// records that shell as agent-owned. It is a no-op (nil error) for any other
// event, so callers can invoke it unconditionally on PostToolUse.
func RegisterBackgroundShellFromPost(s *Store, input *hookio.HookInput) error {
	if input.ToolName != "Bash" || len(input.ToolInput) == 0 || len(input.ToolResponse) == 0 {
		return nil
	}
	var ti backgroundBashInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil || !ti.RunInBackground {
		return nil
	}
	var tr backgroundBashResponse
	if err := json.Unmarshal(input.ToolResponse, &tr); err != nil || tr.ShellID == "" {
		return nil
	}
	return s.RegisterBackgroundShell(tr.ShellID, input.SessionID)
}
