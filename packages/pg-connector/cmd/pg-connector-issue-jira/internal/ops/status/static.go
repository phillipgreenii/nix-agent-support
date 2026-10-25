package status

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Env is the machine a report is built on: the supervisor probe and the
// clock. The zero value is the real one.
type Env struct {
	// Probe answers the supervisor probe; nil uses proc.SupervisorProbe.
	Probe func(label string) proc.SupervisorState
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// platformKind is the supervisor kind of this platform: launchd on darwin,
// none elsewhere.
func platformKind() string {
	if runtime.GOOS == "darwin" {
		return KindLaunchd
	}
	return KindNone
}

// base fills what every report carries, daemon or not: the build, the paths,
// the store file's size and age (an os.Stat, the store is never opened) and
// the supervisor. Health starts as down with source local.
func (e Env) base(cfg *config.Config, version string) Report {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	probe := e.Probe
	if probe == nil {
		probe = proc.SupervisorProbe
	}
	r := Report{
		Health:   Down,
		Source:   SourceLocal,
		Version:  version,
		BuildID:  version,
		StateDir: cfg.StateDir,
		Socket:   cfg.SocketPath,
		Store:    storeInfo(filepath.Join(cfg.StateDir, config.StoreFileName), now()),
		Logs: Logs{
			Events: filepath.Join(cfg.StateDir, config.EventLogFileName),
			Daemon: filepath.Join(cfg.StateDir, config.DaemonLogFileName),
			Client: filepath.Join(cfg.StateDir, config.ClientLogFileName),
		},
		Supervisor: Supervisor{
			Kind:        platformKind(),
			Label:       cfg.SupervisorLabel,
			State:       probe(cfg.SupervisorLabel).String(),
			RestartHint: proc.RestartHint(cfg.SupervisorLabel, cfg.Path, config.DaemonName),
		},
	}
	return r
}

// storeInfo stats the store file: its own size (the write-ahead log is not
// counted) and how long ago it was last modified.
func storeInfo(path string, now time.Time) Store {
	s := Store{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return s
	}
	mod := utc(fi.ModTime())
	s.Present, s.SizeBytes, s.ModifiedAt = true, fi.Size(), &mod
	s.AgeSeconds = max(int64(now.Sub(fi.ModTime()).Seconds()), 0)
	return s
}

// Static is the report the client prints while the daemon is down: the
// supervisor probe's answer and restart hint, the socket path, the store
// file's size and age and the three log paths, with health "down". It opens no
// store.
func Static(cfg *config.Config, version string) Report {
	return StaticIn(Env{}, cfg, version)
}

// StaticIn is Static on the given machine.
func StaticIn(env Env, cfg *config.Config, version string) Report {
	return env.base(cfg, version)
}

// Fallback is the client's answer to the status op while the daemon is down:
// the static report as the op's result, with the reason the daemon could not
// be reached. static is a report from Static.
func Fallback(static Report) func(ctx context.Context, req scriptout.Request, cause error) (any, error) {
	return func(_ context.Context, _ scriptout.Request, cause error) (any, error) {
		r := static
		r.Health, r.Source = Down, SourceLocal
		reason := "the daemon is not reachable"
		if cause != nil {
			reason += ": " + cause.Error()
		}
		r.Reasons = append([]string{reason}, static.Reasons...)
		return r, nil
	}
}

// sortedNames is the configured query names, sorted.
func sortedNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Queries))
	for name := range cfg.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
