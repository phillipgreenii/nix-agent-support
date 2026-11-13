// Package seams holds the six hermetic seams of pg-git-lander: Runner, Clock,
// Liveness, GitEnv, ConfigPaths and Host. Each has a production
// implementation here and a test fake in package seamstest (Runner's fake is
// x/cmdruntest.Fake). Code under test takes the seam, never the OS.
package seams

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// Runner runs one external command. Production is cmdrun.Exec; unit tests
// pass cmdruntest.Fake.Runner() for the commands git itself does not need to
// run, and cmdrun.Exec for real git.
type Runner = cmdrun.Runner

// ProductionRunner is the real Runner.
func ProductionRunner() Runner { return cmdrun.Exec }

// Clock supplies wall and monotonic time.
type Clock interface {
	// Now returns the wall-clock time (it carries a monotonic reading in
	// production, so Sub between two Now values is monotonic).
	Now() time.Time
	// Since returns the elapsed time since t on the monotonic clock where t
	// carries one.
	Since(t time.Time) time.Duration
}

type systemClock struct{}

// SystemClock is the production Clock.
func SystemClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time                  { return time.Now() }
func (systemClock) Since(t time.Time) time.Duration { return time.Since(t) }

// Liveness answers whether a process is the same one that was recorded: a
// pid alone can be reused, so the pid and its start time are checked together.
type Liveness interface {
	// StartTime returns an opaque start-time token for pid, or an error when
	// the process does not exist.
	StartTime(pid int) (string, error)
	// Alive reports whether pid exists and, when startTime is non-empty, still
	// has that start time.
	Alive(pid int, startTime string) bool
}

type procLiveness struct{}

// ProcLiveness is the production Liveness: kill(pid, 0) plus the start time
// reported by ps -o lstart= (as Unix seconds, the form recorded in a lock
// record's pid_start). A zombie counts as gone.
func ProcLiveness() Liveness { return procLiveness{} }

func (procLiveness) StartTime(pid int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(out))
	if t, perr := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local); perr == nil {
		return strconv.FormatInt(t.Unix(), 10), nil
	}
	return raw, nil
}

func isZombie(pid int) bool {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func (p procLiveness) Alive(pid int, startTime string) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return false
	}
	if isZombie(pid) {
		return false
	}
	if startTime == "" {
		return true
	}
	got, err := p.StartTime(pid)
	return err == nil && got == startTime
}

// RepoPointingVars are the variables that would point git (and the hooks,
// merge drivers and filters it starts) at another repository. They are the
// only variables removed from the child environment.
var RepoPointingVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_CEILING_DIRECTORIES",
	"GIT_PREFIX",
	"GIT_NAMESPACE",
}

// GitEnv supplies the complete environment for git and every process it
// starts.
type GitEnv interface {
	Environ() []string
}

type filteredEnv struct{ base []string }

// FilteredEnv returns a GitEnv holding base minus RepoPointingVars. It is
// deliberately a deny-list, not an allowlist: a hook or merge driver must
// keep XDG_CONFIG_HOME, TMPDIR, GIT_CONFIG_GLOBAL, GNUPGHOME and the rest.
func FilteredEnv(base []string) GitEnv {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if isRepoPointing(name) {
			continue
		}
		out = append(out, kv)
	}
	return filteredEnv{base: out}
}

// ProductionGitEnv is the tool's own environment minus RepoPointingVars.
func ProductionGitEnv() GitEnv { return FilteredEnv(os.Environ()) }

func (e filteredEnv) Environ() []string { return append([]string{}, e.base...) }

func isRepoPointing(name string) bool {
	for _, v := range RepoPointingVars {
		if name == v {
			return true
		}
	}
	return false
}

// ConfigPaths locates the tool's configuration directory.
type ConfigPaths interface {
	// Dir is the user configuration directory of pg-git-lander.
	Dir() string
}

type xdgPaths struct{ getenv func(string) string }

// XDGConfigPaths is the production ConfigPaths:
// $XDG_CONFIG_HOME/pg-git-lander, falling back to ~/.config/pg-git-lander.
func XDGConfigPaths() ConfigPaths { return xdgPaths{getenv: os.Getenv} }

func (p xdgPaths) Dir() string {
	if x := p.getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pg-git-lander")
	}
	return filepath.Join(p.getenv("HOME"), ".config", "pg-git-lander")
}

// Host supplies the host name recorded in lock records.
type Host interface {
	Name() string
}

type osHost struct{}

// OSHost is the production Host (os.Hostname).
func OSHost() Host { return osHost{} }

func (osHost) Name() string {
	n, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return n
}
