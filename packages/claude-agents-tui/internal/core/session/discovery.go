package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type rawSession struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
	StartedAt  int64  `json:"startedAt"` // ms epoch
}

type Discoverer struct {
	SessionsDir string
	PidAlive    func(int) bool
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

// Discover reads all session files and returns live sessions only.
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
		body, err := os.ReadFile(filepath.Join(d.SessionsDir, e.Name()))
		if err != nil {
			continue
		}
		var r rawSession
		if err := json.Unmarshal(body, &r); err != nil {
			continue
		}
		if d.PidAlive != nil && !d.PidAlive(r.PID) {
			continue
		}
		out = append(out, &Session{
			PID:        r.PID,
			SessionID:  r.SessionID,
			Cwd:        r.Cwd,
			Kind:       r.Kind,
			Entrypoint: r.Entrypoint,
			Name:       r.Name,
			StartedAt:  time.UnixMilli(r.StartedAt),
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
