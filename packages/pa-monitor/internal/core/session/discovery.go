package session

import (
	"os"
	"path/filepath"
	"syscall"

	ct "github.com/phillipgreenii/claude-transcript"
)

type Discoverer struct {
	SessionsDir string
	PidAlive    func(int) bool
	// ReadEnv returns the process environment for the session's pid. Nil falls
	// back to ReadProcessEnv. The session-id is threaded so a caching provider can
	// key WhilePIDAlive env by session-id (reuse-safe). Tests inject a
	// deterministic implementation.
	ReadEnv func(sessionID string, pid int) (map[string]string, error)
}

// DefaultPidAlive returns true when the pid is alive (kill -0 semantic).
func DefaultPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// Discover reads all session files and returns every session, with
// Session.PidAlive set from the PidAlive hook. Dead-PID sessions are
// kept so the poller can persist their last-known state until the
// underlying .jsonl file is removed (GC sweeper handles that).
// Malformed files are silently skipped.
func (d *Discoverer) Discover() ([]*Session, error) {
	entries, err := os.ReadDir(d.SessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		// Parse the registry row via the shared library so status/waitingFor/
		// statusUpdatedAt are decoded alongside the existing fields. Malformed
		// files are silently skipped (mirrors prior behavior).
		r, err := ct.ReadSessionFile(filepath.Join(d.SessionsDir, e.Name()))
		if err != nil {
			continue
		}
		alive := true
		if d.PidAlive != nil {
			alive = d.PidAlive(r.PID)
		}
		readEnv := d.ReadEnv
		if readEnv == nil {
			readEnv = func(_ string, pid int) (map[string]string, error) { return ReadProcessEnv(pid) }
		}
		env, _ := readEnv(r.SessionID, r.PID) // best-effort; empty map on failure

		out = append(out, &Session{
			PID:             r.PID,
			SessionID:       r.SessionID,
			Cwd:             r.Cwd,
			Kind:            r.Kind,
			Entrypoint:      r.Entrypoint,
			Name:            r.Name,
			StartedAt:       r.StartedAt,
			RegistryStatus:  r.Status,
			WaitingFor:      r.WaitingFor,
			StatusUpdatedAt: r.StatusUpdatedAt,
			Env:             env,
			PidAlive:        alive,
		})
	}
	return out, nil
}

// DefaultSessionsDir returns ~/.claude/sessions.
func DefaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}
