// Package sessiontest holds a file-reading session discovery for hermetic unit
// fixtures only. The daemon never uses it: production discovery reads the
// recorder's Process facts (session.Discoverer).
package sessiontest

import (
	"os"
	"path/filepath"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// FileDiscoverer reads registry files from SessionsDir (test fixtures).
type FileDiscoverer struct {
	SessionsDir string
	PidAlive    func(int) bool
	ReadEnv     func(sessionID string, pid int) (map[string]string, error)
	ProcStart   func(pid int) (time.Time, error)
}

// Dir is the registry directory (the corpus Monitor adds it to its watch set).
func (d *FileDiscoverer) Dir() string { return d.SessionsDir }

// Discover reads all registry files and returns every session, dead pids
// included. Malformed files are skipped.
func (d *FileDiscoverer) Discover() ([]*session.Session, error) {
	entries, err := os.ReadDir(d.SessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*session.Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		regPath := filepath.Join(d.SessionsDir, e.Name())
		r, err := ct.ReadSessionFile(regPath)
		if err != nil {
			continue
		}
		alive := true
		if d.PidAlive != nil {
			alive = d.PidAlive(r.PID)
		}
		if alive && !ct.ProcStartMatchesWith(r, d.ProcStart) {
			alive = false
		}
		readEnv := d.ReadEnv
		if readEnv == nil {
			readEnv = func(_ string, pid int) (map[string]string, error) { return session.ReadProcessEnv(pid) }
		}
		env, _ := readEnv(r.SessionID, r.PID)
		out = append(out, &session.Session{
			PID: r.PID, SessionID: r.SessionID, Cwd: r.Cwd, Kind: r.Kind, Entrypoint: r.Entrypoint,
			Name: r.Name, StartedAt: r.StartedAt, RegistryStatus: r.Status, WaitingFor: r.WaitingFor,
			StatusUpdatedAt: r.StatusUpdatedAt, RegistryPath: regPath, Env: env, PidAlive: alive,
		})
	}
	return out, nil
}
