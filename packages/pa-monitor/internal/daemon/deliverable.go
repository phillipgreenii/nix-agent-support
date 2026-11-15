package daemon

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// controlStateDir mirrors the pg-claude-control mod's listen.sh: the state
// directory is $PG_CLAUDE_CONTROL_STATE_DIR, else $XDG_STATE_HOME/pg-claude-control,
// else ~/.local/state/pg-claude-control. Empty when none can be resolved.
func controlStateDir(getenv func(string) string) string {
	if d := getenv("PG_CLAUDE_CONTROL_STATE_DIR"); d != "" {
		return d
	}
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-claude-control")
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "state", "pg-claude-control")
	}
	return ""
}

// inboxIsFIFO reports whether <stateDir>/inbox/<pid>.fifo exists and is a named
// pipe. It lstat's the path (no process is launched); a regular file or a
// symlink at that path does not count.
func inboxIsFIFO(stateDir string, pid int) bool {
	if stateDir == "" || pid <= 0 {
		return false
	}
	fi, err := os.Lstat(filepath.Join(stateDir, "inbox", strconv.Itoa(pid)+".fifo"))
	return err == nil && fi.Mode()&os.ModeNamedPipe != 0
}

// controlDeliverable is the daemon's one answer to "can a nudge reach this
// session": the pid is alive and the control mod has an inbox for it. The
// recorder's control_inbox fact decides when the session carries one; with the
// recorder unavailable (no fact) the inbox file is checked directly. No terminal
// ancestry walk and no subprocess is involved.
func controlDeliverable(sv *aggregate.SessionView, getenv func(string) string) bool {
	if sv == nil || sv.Session == nil || !sv.PidAlive {
		return false
	}
	if sv.ControlInboxKnown {
		return sv.ControlInbox
	}
	return inboxIsFIFO(controlStateDir(getenv), sv.PID)
}

// controlResolver answers the nudger's per-pid HasSurface question for one tick
// from the tick's tree. A pid claimed by two live sessions is deliverable only
// if every claimant is.
type controlResolver struct {
	byPID map[int]bool
}

func newControlResolver(tree *aggregate.Tree, getenv func(string) string) *controlResolver {
	r := &controlResolver{byPID: map[int]bool{}}
	if tree == nil {
		return r
	}
	for _, sv := range tree.Sessions() {
		if sv.Session == nil || !sv.PidAlive {
			continue
		}
		ok := controlDeliverable(sv, getenv)
		if prev, seen := r.byPID[sv.PID]; seen {
			ok = ok && prev
		}
		r.byPID[sv.PID] = ok
	}
	return r
}

// HasSurface reports whether pid has a live session with a control inbox.
func (r *controlResolver) HasSurface(pid int) bool { return r.byPID[pid] }
