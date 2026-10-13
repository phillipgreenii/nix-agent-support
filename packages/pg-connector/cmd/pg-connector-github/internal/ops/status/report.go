// Package status is the `status` op of both instances and the report behind
// the binary's own `pg-connector-github status` subcommand.
//
// # The document
//
// Report is the one JSON document. The daemon answers the op with it (the
// op's result), the subcommand prints it with --json, and the text form
// (Render) is a rendering of the same values, so the two never disagree. The
// field names are the contract; testdata/status_daemon.golden.json and
// testdata/status_local.golden.json are the golden documents. Later phases
// ADD fields (the change-log head seq, pending settle rows and the settle
// timeouts of the last hour, per-consumer positions); an existing field is
// never renamed or removed, and every added one is omitted when empty.
//
//	health           "healthy", "degraded" or "down"; the exit code follows it
//	                 (0, 2, 3; INV-EXIT-1)
//	reasons          why health is not healthy, one line each (absent when healthy)
//	source           "daemon" (the daemon answered) or "local" (the daemon did not,
//	                 so this binary built the degraded local report)
//	version          the answering binary's stamped version
//	build_id         its build id, the value sent as client_build/daemon_build on
//	                 the socket
//	client_build_id  the build id of the binary that asked (subcommand only)
//	build_differs    true when client_build_id and build_id differ (subcommand
//	                 only); a difference inside the protocol window is not an error
//	started_at       when the daemon started (daemon only)
//	uptime_seconds   seconds since started_at (daemon only)
//	state_dir        the state directory
//	socket           the socket path
//	store            {path, present, size_bytes, modified_at, age_seconds}: the
//	                 store file's own size and age (present is false when the file
//	                 does not exist)
//	logs             {events, daemon, client}: the log paths, the state directory
//	                 joined with the config package's file-name constants
//	supervisor       {kind, label, state}: kind "launchd" or "none", the job
//	                 label, and the probe's answer (running, registered,
//	                 not_registered, none or unknown)
//	instances        [{name, instance}]: which registry name is served by which
//	                 instance
//	budget           [{bucket, cap, background_cap, reserve, window_seconds,
//	                 spent, background_spent, in_flight, remaining, limit,
//	                 reset_at, paused, pause_reason, paused_until}] per bucket
//	                 (daemon only; remaining, limit and reset_at only while the
//	                 origin's reading is in force; pause fields only while paused)
//	queue            {running, classes: [{class, name, depth,
//	                 oldest_overdue_seconds}]} (daemon only)
//	queries          [{name, last_success_at, last_error}] per configured query,
//	                 sorted by name (daemon only)
//	auth             {state, detail, viewer, unauthenticated}: the auth_status
//	                 answer, the cached viewer login and whether GitHub rejects
//	                 the token (daemon only)
//
// # What makes the report degraded
//
// The report is "down" when no daemon answered. Otherwise it is "degraded"
// (exit 2) when ANY of these holds, and "healthy" (exit 0) when none does:
//
//   - a budget bucket is paused (a secondary limit holds the background);
//   - the daemon is unauthenticated (GitHub rejects the token), or the viewer
//     was never resolved and the last refresh failed;
//   - a configured query's last_error is set;
//   - the daemon answered, but with an error (for example it is migrating).
//
// A build-id difference, a queue that is overdue and a missing store file do
// not make it degraded: they are reported, not judged.
package status

import (
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
)

// Health is the report's verdict.
type Health string

const (
	Healthy  Health = "healthy"
	Degraded Health = "degraded"
	Down     Health = "down"
)

// Exit codes of the status subcommand (INV-EXIT-1).
const (
	ExitHealthy  = 0
	ExitDegraded = 2
	ExitDown     = 3
)

// ExitCode is the process exit code of the verdict.
func (h Health) ExitCode() int {
	switch h {
	case Healthy:
		return ExitHealthy
	case Degraded:
		return ExitDegraded
	default:
		return ExitDown
	}
}

// Source values of Report.Source.
const (
	SourceDaemon = "daemon"
	SourceLocal  = "local"
)

// Supervisor kinds.
const (
	KindLaunchd = "launchd"
	KindNone    = "none"
)

// Registry names served by the instances (the design's "Registration"): a
// name no longer names a binary, it labels the daemon's side.
const (
	RegistryNamePR = "pg-connector-pr-github"
	RegistryNameCI = "pg-connector-ci-github-actions"
)

// Report is the status document. See the package comment for every field.
type Report struct {
	Health  Health   `json:"health"`
	Reasons []string `json:"reasons,omitempty"`
	Source  string   `json:"source"`

	Version       string         `json:"version"`
	BuildID       string         `json:"build_id"`
	ClientBuildID string         `json:"client_build_id,omitempty"`
	BuildDiffers  bool           `json:"build_differs,omitempty"`
	StartedAt     *Time          `json:"started_at,omitempty"`
	UptimeSeconds *int64         `json:"uptime_seconds,omitempty"`
	StateDir      string         `json:"state_dir"`
	Socket        string         `json:"socket"`
	Store         Store          `json:"store"`
	Logs          Logs           `json:"logs"`
	Supervisor    Supervisor     `json:"supervisor"`
	Instances     []InstanceName `json:"instances"`

	Budget  []Bucket `json:"budget,omitempty"`
	Queue   *Queue   `json:"queue,omitempty"`
	Queries []Query  `json:"queries,omitempty"`
	Auth    *Auth    `json:"auth,omitempty"`
}

// Time is a UTC instant that marshals as RFC 3339 with whole seconds.
type Time = time.Time

// Store describes the store file.
type Store struct {
	Path       string `json:"path"`
	Present    bool   `json:"present"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt *Time  `json:"modified_at,omitempty"`
	AgeSeconds int64  `json:"age_seconds"`
}

// Logs are the log paths.
type Logs struct {
	Events string `json:"events"`
	Daemon string `json:"daemon"`
	Client string `json:"client"`
}

// Supervisor is the process supervisor and the probe's answer.
type Supervisor struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	State string `json:"state"`
}

// InstanceName maps a registry name to the instance that serves it.
type InstanceName struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
}

// Bucket is one budget bucket.
type Bucket struct {
	Bucket          string `json:"bucket"`
	Cap             int64  `json:"cap"`
	BackgroundCap   int64  `json:"background_cap"`
	Reserve         int64  `json:"reserve"`
	WindowSeconds   int64  `json:"window_seconds"`
	Spent           int64  `json:"spent"`
	BackgroundSpent int64  `json:"background_spent"`
	InFlight        int64  `json:"in_flight"`
	Remaining       *int64 `json:"remaining,omitempty"`
	Limit           *int64 `json:"limit,omitempty"`
	ResetAt         *Time  `json:"reset_at,omitempty"`
	Paused          bool   `json:"paused"`
	PauseReason     string `json:"pause_reason,omitempty"`
	PausedUntil     *Time  `json:"paused_until,omitempty"`
}

// Queue is the scheduler's depth.
type Queue struct {
	Running int          `json:"running"`
	Classes []QueueClass `json:"classes"`
}

// QueueClass is one priority class's queue.
type QueueClass struct {
	Class                int    `json:"class"`
	Name                 string `json:"name"`
	Depth                int    `json:"depth"`
	OldestOverdueSeconds int64  `json:"oldest_overdue_seconds"`
}

// Query is one configured query's freshness.
type Query struct {
	Name          string `json:"name"`
	LastSuccessAt *Time  `json:"last_success_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// Auth is the authentication state and the acting identity.
type Auth struct {
	State           string `json:"state"`
	Detail          string `json:"detail,omitempty"`
	Viewer          string `json:"viewer,omitempty"`
	Unauthenticated bool   `json:"unauthenticated"`
}

// Static is what a report knows without a daemon: the build, the paths, the
// supervisor and the configured query names.
type Static struct {
	Version string
	BuildID string

	StateDir   string
	SocketPath string
	// StoreFile, EventLog, DaemonLog and ClientLog are full paths.
	StoreFile string
	EventLog  string
	DaemonLog string
	ClientLog string

	SupervisorLabel string
	// ConfigPath is the config file the daemon was started with; the restart
	// hint names it where the platform needs it.
	ConfigPath string
	// SupervisorKind is "launchd" or "none"; empty means the platform's.
	SupervisorKind string
	// Probe answers the supervisor probe; nil uses proc.SupervisorProbe.
	Probe func(label string) proc.SupervisorState

	// QueryNames are the configured query names (pr.queries).
	QueryNames []string
}

// StaticFromConfig derives the Static of a loaded config. The log and store
// paths are the state directory joined with the config package's file-name
// constants; the build id is the version.
func StaticFromConfig(cfg *config.Config, version string) Static {
	names := make([]string, 0, len(cfg.PR.Queries))
	for name := range cfg.PR.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return Static{
		Version:         version,
		BuildID:         version,
		StateDir:        cfg.StateDir,
		SocketPath:      cfg.SocketPath,
		StoreFile:       filepath.Join(cfg.StateDir, config.StoreFileName),
		EventLog:        filepath.Join(cfg.StateDir, config.EventLogFileName),
		DaemonLog:       filepath.Join(cfg.StateDir, config.DaemonLogFileName),
		ClientLog:       filepath.Join(cfg.StateDir, config.ClientLogFileName),
		SupervisorLabel: cfg.SupervisorLabel,
		ConfigPath:      cfg.Path,
		QueryNames:      names,
	}
}

// platformKind is the supervisor kind of this platform: launchd on darwin,
// none elsewhere (Linux has no supervisor integration yet).
func platformKind() string {
	if runtime.GOOS == "darwin" {
		return KindLaunchd
	}
	return KindNone
}

// InstanceNames is the registry-name to instance mapping the report prints.
func InstanceNames() []InstanceName {
	return []InstanceName{
		{Name: RegistryNamePR, Instance: "pr"},
		{Name: RegistryNameCI, Instance: "ci"},
	}
}

// base fills what every report carries, daemon or not: the build, the paths,
// the store file's size and age, the supervisor and the instance mapping. The
// health starts as down with source local; the caller overrides.
func (s Static) base(now time.Time) Report {
	r := Report{
		Health:    Down,
		Source:    SourceLocal,
		Version:   s.Version,
		BuildID:   s.BuildID,
		StateDir:  s.StateDir,
		Socket:    s.SocketPath,
		Store:     storeInfo(s.StoreFile, now),
		Logs:      Logs{Events: s.EventLog, Daemon: s.DaemonLog, Client: s.ClientLog},
		Instances: InstanceNames(),
	}
	kind := s.SupervisorKind
	if kind == "" {
		kind = platformKind()
	}
	probe := s.Probe
	if probe == nil {
		probe = proc.SupervisorProbe
	}
	r.Supervisor = Supervisor{Kind: kind, Label: s.SupervisorLabel, State: probe(s.SupervisorLabel).String()}
	return r
}

// Local is the degraded local report for a daemon that did not answer: the
// supervisor probe's answer, the socket path, the store file's size and age
// and the log paths. cause, when non-empty, is the reason the daemon did not
// answer. Its health is Down.
func Local(s Static, now time.Time, cause string) Report {
	r := s.base(now)
	r.Health = Down
	if cause != "" {
		r.Reasons = []string{cause}
	}
	return r
}

// Unhealthy is a report for a daemon that answered with an error instead of a
// report (it is migrating, or its protocol is outside the client's window):
// the local fields plus the reason, health Degraded.
func Unhealthy(s Static, now time.Time, cause string) Report {
	r := s.base(now)
	r.Health = Degraded
	r.Reasons = []string{cause}
	return r
}

// className is a class's name without its number ("query" for "3 query").
func className(c governor.Class) string {
	_, name, _ := strings.Cut(c.String(), " ")
	return name
}

// assess sets Health and Reasons of a daemon-built report by the rule in the
// package comment.
func assess(r *Report) {
	var reasons []string
	for _, b := range r.Budget {
		if b.Paused {
			reasons = append(reasons, "budget bucket "+b.Bucket+" is paused: "+b.PauseReason)
		}
	}
	if r.Auth != nil {
		switch {
		case r.Auth.Unauthenticated:
			reasons = append(reasons, "unauthenticated: "+r.Auth.Detail)
		case r.Auth.State != "OK" && r.Auth.Viewer == "" && r.Auth.Detail != "" && !strings.Contains(r.Auth.Detail, "not resolved yet"):
			reasons = append(reasons, "viewer not resolved: "+r.Auth.Detail)
		}
	}
	for _, q := range r.Queries {
		if q.LastError != "" {
			reasons = append(reasons, "query "+q.Name+" last_error: "+q.LastError)
		}
	}
	r.Reasons = reasons
	r.Health = Healthy
	if len(reasons) > 0 {
		r.Health = Degraded
	}
}

// sortedCopy returns names sorted without altering the input.
func sortedCopy(names []string) []string {
	out := slices.Clone(names)
	sort.Strings(out)
	return out
}
