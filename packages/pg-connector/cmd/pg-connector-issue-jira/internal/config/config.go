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
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Config is the loaded daemon and client configuration. The json tags are the
// key names of the rendered file; Path and Hash are not part of the file.
type Config struct {
	// Identity, paths and process.
	StateDir        string `json:"state_dir"`
	SocketPath      string `json:"socket_path"`
	PjiraPath       string `json:"pjira_path"`
	Instance        string `json:"instance"`
	SupervisorLabel string `json:"supervisor_label"`
	// OTelEndpoint is the OpenTelemetry endpoint; empty means no export.
	OTelEndpoint string `json:"otel_endpoint,omitempty"`
	// PjiraEnv is the ONLY environment the daemon gives every pjira process.
	PjiraEnv map[string]string `json:"pjira_env"`

	// Queries maps a caller-facing query name to its definition. None is
	// built in; nothing is watched unless its definition says so.
	Queries map[string]schema.QueryDef `json:"queries"`

	// Budget.
	RequestRatePerSecond int      `json:"request_rate_per_second"`
	Concurrency          int      `json:"concurrency"`
	DefaultPause         Duration `json:"default_pause"`
	// RateReserveRequests is the floor on the origin's own count of requests
	// left in its burst window (X-RateLimit-Remaining). 0 holds nothing back: a
	// request is refused only when none is left.
	RateReserveRequests int `json:"rate_reserve_requests"`

	// Write notice.
	WriteNoticeTimeout Duration `json:"write_notice_timeout"`
	WriteLease         Duration `json:"write_lease"`

	// Groups and retention.
	SummaryTTL          Duration            `json:"summary_ttl"`
	DetailTTL           Duration            `json:"detail_ttl"`
	DetailMaxAge        Duration            `json:"detail_max_age"`
	QueryTTL            map[string]Duration `json:"query_ttl"`
	QueryTTLDefault     Duration            `json:"query_ttl_default"`
	TerminalTTL         Duration            `json:"terminal_ttl"`
	Expiry              Duration            `json:"expiry"`
	TrackedSetAlertSize int                 `json:"tracked_set_alert_size"`

	// Change detection and the feed.
	SettleTimeout      Duration `json:"settle_timeout"`
	ChangeLogRetention Duration `json:"change_log_retention"`
	ConsumerStaleAfter Duration `json:"consumer_stale_after"`
	ChangeBodyCap      ByteSize `json:"change_body_cap"`
	FillHorizon        Duration `json:"fill_horizon"`
	BatchSize          int      `json:"batch_size"`
	Workers            int      `json:"workers"`

	// StampedHash is the "config_hash" the renderer stamped into the file.
	StampedHash string `json:"config_hash,omitempty"`
	// RenderedPath is the optional "config_path" the file carried: where the
	// renderer says it installs the file. Informational only; it never enters
	// the computed hash.
	RenderedPath string `json:"config_path,omitempty"`

	// Path is the resolved path of the file that was loaded. Hash is the
	// stamped hash. Neither is part of the file.
	Path string `json:"-"`
	Hash string `json:"-"`
}

// OTelEnabled reports whether an OpenTelemetry endpoint is configured.
func (c *Config) OTelEnabled() bool { return c.OTelEndpoint != "" }

// QueryTTLFor returns the query_ttl of the configured query name: its entry in
// QueryTTL, else QueryTTLDefault.
func (c *Config) QueryTTLFor(name string) Duration {
	if d, ok := c.QueryTTL[name]; ok {
		return d
	}
	return c.QueryTTLDefault
}

// Defaults returns a Config holding every default. StateDir and PjiraPath have
// none and must be supplied by the file.
func Defaults() *Config {
	return &Config{
		Instance:             DefaultInstance,
		SupervisorLabel:      DefaultSupervisorLabel,
		PjiraEnv:             map[string]string{},
		Queries:              map[string]schema.QueryDef{},
		RequestRatePerSecond: DefaultRequestRatePerSecond,
		Concurrency:          DefaultConcurrency,
		DefaultPause:         Duration(DefaultDefaultPause),
		RateReserveRequests:  DefaultRateReserveRequests,
		WriteNoticeTimeout:   Duration(DefaultWriteNoticeTimeout),
		WriteLease:           Duration(DefaultWriteLease),
		SummaryTTL:           Duration(DefaultSummaryTTL),
		DetailTTL:            Duration(DefaultDetailTTL),
		DetailMaxAge:         Duration(DefaultDetailMaxAge),
		QueryTTL:             map[string]Duration{},
		QueryTTLDefault:      Duration(DefaultQueryTTLDefault),
		TerminalTTL:          Duration(DefaultTerminalTTL),
		Expiry:               Duration(DefaultExpiry),
		TrackedSetAlertSize:  DefaultTrackedSetAlertSize,
		SettleTimeout:        Duration(DefaultSettleTimeout),
		ChangeLogRetention:   Duration(DefaultChangeLogRetention),
		ConsumerStaleAfter:   Duration(DefaultConsumerStaleAfter),
		ChangeBodyCap:        ByteSize(DefaultChangeBodyCap),
		// FillHorizon and Workers stay zero: parse resolves them (to
		// SummaryTTL and Concurrency) after the file is decoded.
		BatchSize: DefaultBatchSize,
	}
}

// Load reads the config file at path, applies the defaults, validates, and
// returns the result. It reads NO environment variable: getenv MAY be nil and
// is never called, there is no override and no fallback for an empty path (an
// empty path is an error). The hash is the one the renderer stamped
// ("config_hash"); Load does not recompute it, and only when the file carries
// none does it fall back to a deterministic sha256 over the effective file
// settings so that Hash is never empty.
func Load(path string, getenv func(string) string) (*Config, error) {
	_ = getenv // accepted for signature symmetry; deliberately never read
	if path == "" {
		return nil, errors.New("no config file: pass --config <path>")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %s: %w", path, err)
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

	if cfg.StateDir == "" {
		return nil, fmt.Errorf("config %s: state_dir is required", resolved)
	}
	if cfg.PjiraPath == "" {
		return nil, fmt.Errorf("config %s: pjira_path is required", resolved)
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(cfg.StateDir, SocketFileName)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", resolved, err)
	}

	// The hash is taken after the derived defaults are filled in, so a file
	// that spells a default out hashes like one that omits it.
	cfg.Hash = cfg.StampedHash
	if cfg.Hash == "" {
		h, herr := computeHash(cfg)
		if herr != nil {
			return nil, herr
		}
		cfg.Hash = h
	}
	return cfg, nil
}

// parse decodes raw over the defaults, rejecting unknown keys and trailing
// data, and resolves the keys whose default follows another key.
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
	// A JSON null for a map clears it; null means unset, so normalise to the
	// empty default (also keeps the computed hash stable).
	if cfg.PjiraEnv == nil {
		cfg.PjiraEnv = map[string]string{}
	}
	if cfg.Queries == nil {
		cfg.Queries = map[string]schema.QueryDef{}
	}
	if cfg.QueryTTL == nil {
		cfg.QueryTTL = map[string]Duration{}
	}
	if cfg.FillHorizon == 0 {
		cfg.FillHorizon = cfg.SummaryTTL
	}
	// workers defaults to concurrency, but an explicit zero is an error, so
	// whether the key was set (non-null) has to be known.
	var probe struct {
		Workers *int `json:"workers"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, decodeError(err)
	}
	if probe.Workers == nil {
		cfg.Workers = cfg.Concurrency
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

func (c *Config) validate() error {
	for _, kv := range [][2]string{
		{"state_dir", c.StateDir}, {"socket_path", c.SocketPath}, {"pjira_path", c.PjiraPath},
	} {
		if name, p := kv[0], kv[1]; !filepath.IsAbs(p) {
			return fmt.Errorf("%s must be an absolute path, got %q", name, p)
		}
	}
	if err := proc.CheckSocketPath(c.SocketPath, proc.MaxSocketPathBytes()); err != nil {
		return fmt.Errorf("socket_path: %w", err)
	}
	if c.Instance == "" {
		return errors.New("instance must not be empty")
	}
	if c.SupervisorLabel == "" {
		return errors.New("supervisor_label must not be empty")
	}
	for name, d := range map[string]Duration{
		"default_pause": c.DefaultPause, "write_notice_timeout": c.WriteNoticeTimeout,
		"write_lease": c.WriteLease, "summary_ttl": c.SummaryTTL,
		"detail_ttl": c.DetailTTL, "detail_max_age": c.DetailMaxAge,
		"query_ttl_default": c.QueryTTLDefault, "terminal_ttl": c.TerminalTTL,
		"expiry": c.Expiry, "settle_timeout": c.SettleTimeout,
		"change_log_retention": c.ChangeLogRetention, "consumer_stale_after": c.ConsumerStaleAfter,
		"fill_horizon": c.FillHorizon,
	} {
		if time.Duration(d) <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.RateReserveRequests < 0 {
		return fmt.Errorf("rate_reserve_requests must not be negative, got %d", c.RateReserveRequests)
	}
	for name, d := range c.QueryTTL {
		if d <= 0 {
			return fmt.Errorf("query_ttl.%s must be positive", name)
		}
	}
	for name, n := range map[string]int{
		"request_rate_per_second": c.RequestRatePerSecond, "concurrency": c.Concurrency,
		"workers": c.Workers, "batch_size": c.BatchSize,
		"change_body_cap": int(c.ChangeBodyCap), "tracked_set_alert_size": c.TrackedSetAlertSize,
	} {
		if n <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	return nil
}

// computeHash is the sha256 of the canonical encoding of the effective file
// settings: the struct (fixed field order, sorted map keys, canonical
// durations) with the stamp fields cleared.
func computeHash(cfg *Config) (string, error) {
	view := *cfg
	view.StampedHash = ""
	view.RenderedPath = ""
	b, err := json.Marshal(&view)
	if err != nil {
		return "", fmt.Errorf("hash config: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
