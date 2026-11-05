package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrDaemonOffline is what discovery reports while the recorder daemon is not
// answering. There is no fallback to Claude's files: the command fails and says
// the recorder is not available, so it is never mistaken for pa-monitor's own
// daemon being down and never answers with a guess about the thing asked.
var ErrDaemonOffline = errors.New("the recorder is not available")

// Registry is where discovery reads the live registrations and the liveness of
// each Process from: the recorder's Process facts (recorderfacts.Facts). It
// replaces reading ~/.claude/sessions and probing each pid.
type Registry interface {
	// Registrations returns one row per session the recorder knows a Process
	// for, with PidAlive taken from the recorder's Process facts. A recorder that
	// is not answering returns an error matching ErrDaemonOffline.
	Registrations(ctx context.Context) ([]*Session, error)
}

// Discovery is what the corpus Monitor discovers sessions through.
type Discovery interface {
	Discover() ([]*Session, error)
}

// discoverTimeout bounds one discovery read of the recorder.
const discoverTimeout = 20 * time.Second

// Discoverer discovers sessions through the recorder's Process facts and adds
// each process's environment.
type Discoverer struct {
	Registry Registry
	// ReadEnv returns the process environment for the session's pid. Nil falls
	// back to ReadProcessEnv. The session-id is threaded so a caching provider can
	// key WhilePIDAlive env by session-id (reuse-safe). Tests inject a
	// deterministic implementation.
	ReadEnv func(sessionID string, pid int) (map[string]string, error)
}

// Discover returns every session the recorder knows, with Session.PidAlive set
// from the recorder's Process facts. Dead-PID sessions are kept so the poller
// can persist their last-known state until the GC sweeper removes them. While
// the recorder is down it returns an error matching ErrDaemonOffline.
func (d *Discoverer) Discover() ([]*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()
	out, err := d.Registry.Registrations(ctx)
	if err != nil {
		return nil, err
	}
	readEnv := d.ReadEnv
	if readEnv == nil {
		readEnv = func(_ string, pid int) (map[string]string, error) { return ReadProcessEnv(pid) }
	}
	for _, s := range out {
		s.Env, _ = readEnv(s.SessionID, s.PID) // best-effort; empty map on failure
	}
	return out, nil
}

// DefaultPidAlive returns true when the pid is alive (kill -0 semantic). It is
// for callers that probe a pid that is not a Claude Process (the provider
// cache's subshell count); discovery takes liveness from the recorder.
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

// DefaultSessionsDir returns ~/.claude/sessions.
func DefaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}
