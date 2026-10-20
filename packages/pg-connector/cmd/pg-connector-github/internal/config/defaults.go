package config

import "time"

// Defaults of the daemon's settings, one named constant per row of the
// daemon design's "Defaults" table. Every value is only a
// starting point: each is overridable by the config file, and the shadow run
// checks them. Later packets MUST use these names (or the loaded Config
// fields) and never restate a number.
const (
	// DefaultQueryTTLMine and DefaultQueryTTLTeam are the live flow's
	// cadences for the configured queries named "mine" and "team".
	DefaultQueryTTLMine = 60 * time.Second
	DefaultQueryTTLTeam = 120 * time.Second
	// DefaultQueryNameMine and DefaultQueryNameTeam name the two queries
	// whose query_ttl the table fixes. They are setting keys, not hardcoded
	// queries: the deployment supplies the query text.
	DefaultQueryNameMine = "mine"
	DefaultQueryNameTeam = "team"

	DefaultAdhocQueryTTL = 120 * time.Second
	DefaultSummaryTTL    = 60 * time.Second

	DefaultDetailTTL    = 10 * time.Minute
	DefaultDetailMaxAge = 30 * time.Minute

	DefaultConversationTTL    = 10 * time.Minute
	DefaultConversationMaxAge = 30 * time.Minute

	DefaultPendingTTL = 2 * time.Minute

	DefaultCIActiveTTL = 60 * time.Second
	DefaultCIMaxAge    = 30 * time.Minute
	DefaultCIFailedTTL = 5 * time.Minute

	DefaultTerminalTTL         = 24 * time.Hour
	DefaultSettleTimeout       = 30 * time.Second
	DefaultKeepaliveWindow     = 24 * time.Hour
	DefaultExpiry              = 7 * 24 * time.Hour
	DefaultFillHorizon         = DefaultSummaryTTL // "one summary_ttl"; follows summary_ttl when unset
	DefaultChangeLogRetention  = 14 * 24 * time.Hour
	DefaultConsumerStaleAfter  = 7 * 24 * time.Hour
	DefaultChangeBodyCap       = 4 * 1024 // bytes (4 KiB)
	DefaultBatchSize           = 74
	DefaultWorkers             = 4
	DefaultRateReservePoints   = 1000
	DefaultRESTRequestsPerHour = 3000
	DefaultRESTSearchPerMinute = 20

	// DefaultGraphQLPointsPerHour is the SHADOW value: Phase A's kill
	// criterion while the daemon runs beside the live flow. The nix module
	// sets CutoverGraphQLPointsPerHour after cutover. It is overridable by
	// the file and by PG_CONNECTOR_GITHUB_GRAPHQL_POINTS_PER_HOUR.
	DefaultGraphQLPointsPerHour = 1500
	// CutoverGraphQLPointsPerHour is the ADR 0077 row S35 ceiling used after
	// cutover. It is a constant for the renderer's and the docs' reference,
	// never a loader default.
	CutoverGraphQLPointsPerHour = 4000

	// DefaultBackgroundShare is the share of every bucket that background
	// work may spend (ruling 16).
	DefaultBackgroundShare = 0.9
)

// Non-table defaults of the identity and process settings.
const (
	// DefaultHost is the public GitHub host. It names no organisation, team
	// or user; one daemon serves exactly one host.
	DefaultHost = "github.com"
	// DefaultGHPath is the gh binary resolved on PATH.
	DefaultGHPath = "gh"
	// DefaultSupervisorLabel is the launchd label of the daemon's supervisor
	// entry (design "One binary, two modes").
	DefaultSupervisorLabel = "com.phillipg.pg-connector-github-daemon"
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
	// EventLogFileName is the event log (Loki service_name
	// pg-connector-github), rotated by the daemon at 5 MiB.
	EventLogFileName = "events.jsonl"
	// DaemonLogFileName is the structured log of state changes, rotated by
	// the daemon at 5 MiB.
	DaemonLogFileName = "daemon.log.jsonl"
	// ClientLogFileName is the client log (service_name
	// pg-connector-github-client), written and rotated (1 MiB, under an
	// flock) by the client.
	ClientLogFileName = "client.log.jsonl"
)

// Names of the PG_CONNECTOR_GITHUB_* environment overrides. They exist for
// tests and the shadow harness only (the daemon's one operator-diagnostics
// variable, PG_CONNECTOR_GITHUB_PPROF_ADDR, is the serve mode's own opt-in and
// lives in pprof.go); every other setting is file-only, and the
// daemon never reads the environment of a client's call.
const (
	EnvSocket               = "PG_CONNECTOR_GITHUB_SOCKET"
	EnvStateDir             = "PG_CONNECTOR_GITHUB_STATE_DIR"
	EnvConfig               = "PG_CONNECTOR_GITHUB_CONFIG"
	EnvGH                   = "PG_CONNECTOR_GITHUB_GH"
	EnvGraphQLPointsPerHour = "PG_CONNECTOR_GITHUB_GRAPHQL_POINTS_PER_HOUR"
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
