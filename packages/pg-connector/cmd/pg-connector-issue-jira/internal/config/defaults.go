package config

import "time"

// Defaults of the daemon's settings, one named constant per default. Later
// packets MUST use these names (or the loaded Config fields) and never restate
// a number. The group, retention and change defaults start from the values of
// the sibling GitHub daemon's constants of the same meaning; the design fixes
// no number for them.
const (
	// Budget keys. The two rates are the P1 spike output for the burst
	// headroom.
	DefaultRequestRatePerSecond = 2
	DefaultConcurrency          = 2
	// DefaultDefaultPause is the pause after a 429 that states no Retry-After.
	DefaultDefaultPause = 60 * time.Second
	// DefaultRateReserveRequests is the floor: nothing spends while the origin
	// reports (or the daemon estimates) fewer than this many requests left in
	// its one-second burst window. 20 is 20% of the documented limit of 100 per
	// second, the point at which Atlassian itself sets X-RateLimit-NearLimit
	// (and 4 times the daemon's own 5 requests per second cap). 0 holds nothing back.
	DefaultRateReserveRequests = 20

	// Write-notice keys.
	DefaultWriteNoticeTimeout = 500 * time.Millisecond
	DefaultWriteLease         = 30 * time.Second

	// Group and retention keys. The four freshness windows are 5 times, 3
	// times, 3 times and 2.5 times their first values (60 s, 10 min, 30 min and
	// 120 s): Jira consumers do not need the GitHub daemon's one-minute
	// freshness, so the steady demand is a small share of the budget (the
	// arithmetic is in the README, "Refresh cadence and the request budget").
	DefaultSummaryTTL      = 5 * time.Minute
	DefaultDetailTTL       = 30 * time.Minute
	DefaultDetailMaxAge    = 90 * time.Minute
	DefaultQueryTTLDefault = 5 * time.Minute
	// DefaultTerminalTTL: an issue is refreshed until 24 hours after it closes.
	DefaultTerminalTTL = 24 * time.Hour
	// DefaultExpiry: an issue is evicted 7 days after it closes.
	DefaultExpiry = 7 * 24 * time.Hour

	// Change keys.
	DefaultSettleTimeout      = 30 * time.Second
	DefaultChangeLogRetention = 14 * 24 * time.Hour
	DefaultConsumerStaleAfter = 7 * 24 * time.Hour
	DefaultChangeBodyCap      = 4 * 1024 // bytes (4 KiB)
	// DefaultFillHorizon is "one summary_ttl": an unset or zero fill_horizon
	// follows summary_ttl.
	DefaultFillHorizon = DefaultSummaryTTL
	DefaultBatchSize   = 74
	// DefaultWorkers is documentation only: an unset workers follows
	// concurrency, so its effective default is DefaultConcurrency.
	DefaultWorkers = DefaultConcurrency

	// DefaultTrackedSetAlertSize is the P1 spike's tracked-set threshold, five
	// times the 11 Jira entities observed.
	DefaultTrackedSetAlertSize = 55
)

// Non-numeric defaults.
const (
	// DefaultInstance is the registry instance name the daemon reports as the
	// source of its feed answers.
	DefaultInstance = "pg-connector-issue-jira"
	// DefaultSupervisorLabel is the launchd label of the daemon's supervisor
	// entry.
	DefaultSupervisorLabel = "com.phillipg.pg-connector-issue-jira-daemon"
	// DaemonName is the daemon binary name, printed in a daemon-down message.
	DaemonName = "pg-connector-issue-jira"
)

// Log service_names the home-manager module and the alert files read. The
// state directory the module renders is $XDG_STATE_HOME/<StateDirName>, which
// MUST differ from the old stateless backend's
// $XDG_STATE_HOME/pg-connector-issue-jira (that path holds the old backend's
// event log until cleanup).
const (
	StateDirName         = "pg-connector-issue-jira-daemon"
	EventServiceName     = "pg-connector-issue-jira-daemon"
	DaemonLogServiceName = "pg-connector-issue-jira-daemon"
	ClientLogServiceName = "pg-connector-issue-jira-daemon-client"
)

// Names of the files the daemon, the client and the later packets share.
// Everything but SocketFileName lives inside the state directory; the socket
// defaults to <state_dir>/SocketFileName.
const (
	// StoreFileName is the SQLite store (WAL mode, the daemon its only
	// writer).
	StoreFileName = "store.sqlite"
	// SocketFileName is the daemon's unix socket inside the state directory.
	SocketFileName = "sock"
	// LockFileName is the single-instance lock inside the state directory.
	LockFileName = "daemon.lock"
	// EventLogFileName is the event log.
	EventLogFileName = "events.jsonl"
	// DaemonLogFileName is the structured log of state changes.
	DaemonLogFileName = "daemon.log.jsonl"
	// ClientLogFileName is the client log.
	ClientLogFileName = "client.log.jsonl"
)

// Field names under which a config file and an attached backends.<name> block
// carry the stamp. The renderer writes the same hash into the config file
// (FieldConfigHash) and into every umbrella-side backends.<name> block
// (FieldConfigHash plus FieldConfigPath, the path of the rendered config the
// block was rendered with). A block that carries neither is accepted.
const (
	FieldConfigHash = "config_hash"
	FieldConfigPath = "config_path"
)
