// Package config loads the rendered config file of pg-connector-github.
//
// # File format
//
// The file named by --config (or PG_CONNECTOR_GITHUB_CONFIG when the flag is
// absent) is ONE JSON object. JSON was chosen because the home-manager module
// renders it from a nix attrset with builtins.toJSON, and because an unknown
// key is a decode error naming the key (the loader rejects them, so a typo
// never silently falls back to a default). testdata/example.json is the golden
// example, with every key written out at its default; a file MAY carry only the
// keys it changes. The loader rejects trailing data and unknown keys at any
// depth.
//
// Identity, paths and process (all file-only except where noted):
//
//	"host"             string   GitHub host, passed to every gh call as GH_HOST (default "github.com")
//	"gh_path"          string   the gh binary (default "gh"; env PG_CONNECTOR_GITHUB_GH wins)
//	"state_dir"        string   REQUIRED absolute directory for the store, logs and lock (env ..._STATE_DIR wins)
//	"socket_path"      string   absolute unix socket path (default <state_dir>/sock; env ..._SOCKET wins)
//	"supervisor_label" string   launchd label (default "com.phillipg.pg-connector-github-daemon")
//	"otel_endpoint"    string   OpenTelemetry endpoint; absent or "" means no export
//	"config_hash"      string   the hash the renderer stamped (see "Config hash")
//	"config_path"      string   optional: where the renderer installs this file (see "Config hash")
//
// state_dir and socket_path MUST be absolute: a relative path would resolve
// against whatever directory started the process, so serve started by hand and
// under a supervisor (working directory /) would differ.
//
// Queries (named, in their native syntax; none is built in, the deployment
// supplies them) and the location of the posted-review sidecar:
//
//	"pr":  {"queries": {"<name>": "<query>" | ["<query>", ...]},
//	        "posted_state_dir": "<absolute path>"}
//	"ci":  {}     reserved, takes no keys: CI runs have no queries of their own,
//	              so a ci --query resolves against the "pr" queries
//
// "pr.posted_state_dir" is optional. It names the sidecar's STATE HOME, the
// parent of its posted/ and locks/ directories, which is the old
// pg-connector-pr-github backend's own state home: the daemon keeps that
// backend's sidecar files and flock where they are, so a rollback to the old
// binary sees every review posted since cutover. Unset (absent, null or "")
// means the daemon derives the home from its OWN process environment at start,
// the way the old backend did (see package sidecar). While it is unset the key
// does not enter the config hash, so the hash of every file rendered before the
// key existed is unchanged.
//
// Settings: every row of the design's "Defaults" table is a top-level key with
// the table's snake_case name. Durations are strings ("60s", "10m", "24h",
// "7d"), "change_body_cap" is an integer of bytes or a string such as "4KiB",
// "query_ttl" is an object from configured query name to duration, merged over
// the defaults for "mine" and "team". "fill_horizon" absent or "0s" means one
// summary_ttl. The cap "graphql_points_per_hour" defaults to the shadow value
// 1500; the nix module sets 4000 after cutover, and
// PG_CONNECTOR_GITHUB_GRAPHQL_POINTS_PER_HOUR overrides it for the shadow
// harness.
//
// A JSON null for any key means unset, so its default applies: a renderer
// MAY emit null for a nix option the operator left unset.
//
// # Environment
//
// Exactly five variables are read, and only here: PG_CONNECTOR_GITHUB_SOCKET,
// _STATE_DIR, _CONFIG and _GH (design "Daemon environment and socket") and
// _GRAPHQL_POINTS_PER_HOUR. Each overrides the one setting; every other
// setting is file-only, so a load with an empty environment (env -i) succeeds.
// The daemon-side loader never reads a client's per-call environment: callers
// pass the daemon's own getenv.
//
// The one other environment read is not a setting of this file: while
// pr.posted_state_dir is unset, package sidecar reads the old backend's own
// PG_CONNECTOR_PR_GITHUB_STATE_DIR, XDG_STATE_HOME and HOME, again through the
// daemon's own getenv, so that the sidecar stays at its current path.
//
// # Config hash
//
// The renderer stamps the same hash into this file ("config_hash") and into
// every umbrella-side backends.<name> block ("config_hash" plus "config_path",
// the path of the rendered file; see FieldConfigHash and FieldConfigPath). The
// file itself MAY also carry "config_path" (a renderer cannot always know its
// own install path, so it is optional); it is recorded as Config.StampedPath,
// is informational only (messages name the path actually read, Config.Path),
// and never enters the hash. The loader returns the stamped hash when the file
// carries one; otherwise it computes a deterministic sha256 over the effective
// file settings (defaults applied, durations canonical, keys sorted), so two
// files differing only in key order, whitespace or equivalent spellings hash
// alike. Environment overrides are not hashed: the hash identifies the file. A
// block that carries neither field is accepted by CheckBlock.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Settings are the rows of the design's "Defaults" table.
type Settings struct {
	// QueryTTL maps a configured query name to its query_ttl. The defaults
	// carry "mine" and "team"; any other configured query takes
	// AdhocQueryTTL unless the file lists it here (see QueryTTLFor).
	QueryTTL      map[string]Duration `json:"query_ttl"`
	AdhocQueryTTL Duration            `json:"adhoc_query_ttl"`
	SummaryTTL    Duration            `json:"summary_ttl"`

	DetailTTL    Duration `json:"detail_ttl"`
	DetailMaxAge Duration `json:"detail_max_age"`

	ConversationTTL    Duration `json:"conversation_ttl"`
	ConversationMaxAge Duration `json:"conversation_max_age"`

	PendingTTL Duration `json:"pending_ttl"`

	CIActiveTTL Duration `json:"ci_active_ttl"`
	CIMaxAge    Duration `json:"ci_max_age"`
	CIFailedTTL Duration `json:"ci_failed_ttl"`

	TerminalTTL        Duration `json:"terminal_ttl"`
	SettleTimeout      Duration `json:"settle_timeout"`
	KeepaliveWindow    Duration `json:"keepalive_window"`
	Expiry             Duration `json:"expiry"`
	FillHorizon        Duration `json:"fill_horizon"`
	ChangeLogRetention Duration `json:"change_log_retention"`
	ConsumerStaleAfter Duration `json:"consumer_stale_after"`
	ChangeBodyCap      ByteSize `json:"change_body_cap"`

	BatchSize            int     `json:"batch_size"`
	Workers              int     `json:"workers"`
	RateReservePoints    int     `json:"rate_reserve_points"`
	GraphQLPointsPerHour int     `json:"graphql_points_per_hour"`
	BackgroundShare      float64 `json:"background_share"`
	RESTRequestsPerHour  int     `json:"rest_requests_per_hour"`
	RESTSearchPerMinute  int     `json:"rest_search_per_minute"`
}

// SettingPostedStateDir is the dotted name of PRSection.PostedStateDir, the
// spelling error messages use for it.
const SettingPostedStateDir = "pr.posted_state_dir"

// PRSection is the "pr" block, the settings of the pr instance.
type PRSection struct {
	// Queries maps a caller-facing query name to its native-syntax
	// expression (a string or a list of strings, schema.QueryExpr). The ci
	// instance resolves a query name against these too, because CI runs have
	// no queries of their own.
	Queries map[string]schema.QueryExpr `json:"queries,omitempty"`

	// PostedStateDir is the posted-review sidecar's STATE HOME, the parent of
	// its posted/ and locks/ directories. It is optional and, when set, an
	// absolute path; empty means unset, and package sidecar then derives the
	// home from the daemon's own environment the way the old backend did.
	// omitempty keeps an unset value out of the hashed encoding, so the hash
	// of a file that never named the key is the same as before the key existed.
	PostedStateDir string `json:"posted_state_dir,omitempty"`
}

// CISection is the "ci" block. The ci instance has no settings of its own, so
// the section is reserved: it accepts an empty object and no keys.
type CISection struct{}

// Config is the loaded daemon and client configuration.
type Config struct {
	Host            string `json:"host"`
	GH              string `json:"gh_path"`
	StateDir        string `json:"state_dir"`
	SocketPath      string `json:"socket_path"`
	SupervisorLabel string `json:"supervisor_label"`
	// OTelEndpoint is the OpenTelemetry endpoint; empty means no export.
	OTelEndpoint string `json:"otel_endpoint,omitempty"`

	PR PRSection `json:"pr"`
	CI CISection `json:"ci"`

	Settings

	// StampedHash is the "config_hash" the file carried, or empty.
	StampedHash string `json:"config_hash,omitempty"`
	// StampedPath is the optional "config_path" the file carried: where the
	// renderer says it installs the file. Informational only: Path is the
	// file actually read, and StampedPath never enters the hash.
	StampedPath string `json:"config_path,omitempty"`

	// Path is the config file that was loaded (flag, else env). Hash is the
	// stamped hash when present, else the computed one. Neither is part of
	// the file.
	Path string `json:"-"`
	Hash string `json:"-"`
}

// OTelEnabled reports whether an OpenTelemetry endpoint is configured.
func (c *Config) OTelEnabled() bool { return c.OTelEndpoint != "" }

// HashStamped reports whether Hash is the renderer's stamped value rather than
// a computed one.
func (c *Config) HashStamped() bool { return c.StampedHash != "" }

// QueryTTLFor returns the query_ttl of the configured query name: its entry in
// QueryTTL, else AdhocQueryTTL.
func (c *Config) QueryTTLFor(name string) Duration {
	if d, ok := c.QueryTTL[name]; ok {
		return d
	}
	return c.AdhocQueryTTL
}

// HasQuery reports whether name is a configured query of the pr section
// (the ci instance resolves a query name against it).
func (c *Config) HasQuery(name string) bool {
	_, ok := c.PR.Queries[name]
	return ok
}

// Defaults returns a Config holding every default. StateDir has none and must
// be supplied by the file or PG_CONNECTOR_GITHUB_STATE_DIR.
func Defaults() *Config {
	return &Config{
		Host:            DefaultHost,
		GH:              DefaultGHPath,
		SupervisorLabel: DefaultSupervisorLabel,
		Settings: Settings{
			QueryTTL: map[string]Duration{
				DefaultQueryNameMine: Duration(DefaultQueryTTLMine),
				DefaultQueryNameTeam: Duration(DefaultQueryTTLTeam),
			},
			AdhocQueryTTL:      Duration(DefaultAdhocQueryTTL),
			SummaryTTL:         Duration(DefaultSummaryTTL),
			DetailTTL:          Duration(DefaultDetailTTL),
			DetailMaxAge:       Duration(DefaultDetailMaxAge),
			ConversationTTL:    Duration(DefaultConversationTTL),
			ConversationMaxAge: Duration(DefaultConversationMaxAge),
			PendingTTL:         Duration(DefaultPendingTTL),
			CIActiveTTL:        Duration(DefaultCIActiveTTL),
			CIMaxAge:           Duration(DefaultCIMaxAge),
			CIFailedTTL:        Duration(DefaultCIFailedTTL),
			TerminalTTL:        Duration(DefaultTerminalTTL),
			SettleTimeout:      Duration(DefaultSettleTimeout),
			KeepaliveWindow:    Duration(DefaultKeepaliveWindow),
			Expiry:             Duration(DefaultExpiry),
			// FillHorizon stays zero: parse resolves it to SummaryTTL.
			ChangeLogRetention:   Duration(DefaultChangeLogRetention),
			ConsumerStaleAfter:   Duration(DefaultConsumerStaleAfter),
			ChangeBodyCap:        ByteSize(DefaultChangeBodyCap),
			BatchSize:            DefaultBatchSize,
			Workers:              DefaultWorkers,
			RateReservePoints:    DefaultRateReservePoints,
			GraphQLPointsPerHour: DefaultGraphQLPointsPerHour,
			BackgroundShare:      DefaultBackgroundShare,
			RESTRequestsPerHour:  DefaultRESTRequestsPerHour,
			RESTSearchPerMinute:  DefaultRESTSearchPerMinute,
		},
	}
}

// ResolvePath returns the config file to load: flagPath when non-empty (the
// --config flag wins), else PG_CONNECTOR_GITHUB_CONFIG, else an error.
func ResolvePath(flagPath string, getenv func(string) string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if getenv != nil {
		if p := getenv(EnvConfig); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file: pass --config <path> or set %s", EnvConfig)
}

// Load reads the config file at path (resolved with ResolvePath, so an empty
// path falls back to PG_CONNECTOR_GITHUB_CONFIG), applies defaults, the file,
// and then the PG_CONNECTOR_GITHUB_* environment overrides, validates, and
// computes the hash. getenv MAY be nil, which reads no overrides.
func Load(path string, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	resolved, err := ResolvePath(path, getenv)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", resolved, err)
	}
	cfg, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", resolved, err)
	}
	cfg.Path = resolved

	// The hash identifies the file, so it is taken before environment
	// overrides and before state_dir derivations.
	cfg.Hash = cfg.StampedHash
	if cfg.Hash == "" {
		h, herr := computeHash(cfg)
		if herr != nil {
			return nil, herr
		}
		cfg.Hash = h
	}

	if err := applyEnv(cfg, getenv); err != nil {
		return nil, err
	}
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("config %s: state_dir is required (or set %s)", resolved, EnvStateDir)
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(cfg.StateDir, SocketFileName)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", resolved, err)
	}
	return cfg, nil
}

// parse decodes raw over the defaults, rejecting unknown keys and trailing
// data, and normalizes fill_horizon.
func parse(raw []byte) (*Config, error) {
	cfg := Defaults()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, decodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the top-level object")
	}
	if cfg.QueryTTL == nil {
		// "query_ttl": null clears the map; null means unset, so the defaults
		// for "mine" and "team" stay.
		cfg.QueryTTL = Defaults().QueryTTL
	}
	if cfg.FillHorizon == 0 {
		cfg.FillHorizon = cfg.SummaryTTL
	}
	return cfg, nil
}

// decodeError rewrites encoding/json's unknown-field error so the offending
// key is named plainly.
func decodeError(err error) error {
	const prefix = "json: unknown field "
	if msg := err.Error(); strings.HasPrefix(msg, prefix) {
		return fmt.Errorf("unknown key %s", strings.TrimPrefix(msg, prefix))
	}
	return err
}

// applyEnv applies the five PG_CONNECTOR_GITHUB_* overrides. An empty variable
// is treated as unset.
func applyEnv(cfg *Config, getenv func(string) string) error {
	if v := getenv(EnvSocket); v != "" {
		cfg.SocketPath = v
	}
	if v := getenv(EnvStateDir); v != "" {
		cfg.StateDir = v
	}
	if v := getenv(EnvGH); v != "" {
		cfg.GH = v
	}
	if v := getenv(EnvGraphQLPointsPerHour); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			return fmt.Errorf("%s=%q: want a positive integer", EnvGraphQLPointsPerHour, v)
		}
		cfg.GraphQLPointsPerHour = n
	}
	return nil
}

func (c *Config) validate() error {
	if c.Host == "" {
		return errors.New("host must not be empty")
	}
	if c.GH == "" {
		return errors.New("gh_path must not be empty")
	}
	if c.SupervisorLabel == "" {
		return errors.New("supervisor_label must not be empty")
	}
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("state_dir must be an absolute path, got %q", c.StateDir)
	}
	if !filepath.IsAbs(c.SocketPath) {
		return fmt.Errorf("socket_path must be an absolute path, got %q", c.SocketPath)
	}
	// Optional: only a value that is set has to be absolute. A relative path
	// would resolve against whatever directory started the process, so the
	// sidecar would land elsewhere under a supervisor (working directory /)
	// than under a shell, and a rollback to the old backend would not find it.
	if d := c.PR.PostedStateDir; d != "" && !filepath.IsAbs(d) {
		return fmt.Errorf("%s must be an absolute path, got %q", SettingPostedStateDir, d)
	}
	for name, d := range map[string]Duration{
		"adhoc_query_ttl": c.AdhocQueryTTL, "summary_ttl": c.SummaryTTL,
		"detail_ttl": c.DetailTTL, "detail_max_age": c.DetailMaxAge,
		"conversation_ttl": c.ConversationTTL, "conversation_max_age": c.ConversationMaxAge,
		"pending_ttl": c.PendingTTL, "ci_active_ttl": c.CIActiveTTL,
		"ci_max_age": c.CIMaxAge, "ci_failed_ttl": c.CIFailedTTL,
		"terminal_ttl": c.TerminalTTL, "settle_timeout": c.SettleTimeout,
		"keepalive_window": c.KeepaliveWindow, "expiry": c.Expiry,
		"fill_horizon": c.FillHorizon, "change_log_retention": c.ChangeLogRetention,
		"consumer_stale_after": c.ConsumerStaleAfter,
	} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	for name, d := range c.QueryTTL {
		if d <= 0 {
			return fmt.Errorf("query_ttl.%s must be positive", name)
		}
	}
	for name, n := range map[string]int{
		"change_body_cap": int(c.ChangeBodyCap), "batch_size": c.BatchSize,
		"workers": c.Workers, "graphql_points_per_hour": c.GraphQLPointsPerHour,
		"rest_requests_per_hour": c.RESTRequestsPerHour, "rest_search_per_minute": c.RESTSearchPerMinute,
	} {
		if n <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.RateReservePoints < 0 {
		return errors.New("rate_reserve_points must not be negative")
	}
	if c.BackgroundShare <= 0 || c.BackgroundShare > 1 {
		return fmt.Errorf("background_share must be in (0, 1], got %v", c.BackgroundShare)
	}
	return nil
}

// computeHash is the sha256 of the canonical encoding of the effective file
// settings: the struct (fixed field order, sorted map keys, canonical
// durations) with the stamp fields (hash and path) cleared.
func computeHash(cfg *Config) (string, error) {
	view := *cfg
	view.StampedHash = ""
	view.StampedPath = ""
	b, err := json.Marshal(&view)
	if err != nil {
		return "", fmt.Errorf("hash config: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
