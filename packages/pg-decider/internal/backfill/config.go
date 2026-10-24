package backfill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ConfigEnv names the environment variable holding the driver's configuration
// file; without it the file is $XDG_CONFIG_HOME/pg-pr-backfill/config.json, and
// the file MAY be absent (the defaults and the flags then decide).
const ConfigEnv = "PG_PR_BACKFILL_CONFIG"

// Config is the driver's configuration file (JSON). A deployment renders it; a flag
// of the same name overrides a key. Unknown keys are tolerated.
type Config struct {
	// QueriesFile is the file of PR query names the deployment renders from the
	// option that renders the connector's query definitions (see ReadQueryNames).
	QueriesFile string `json:"queries_file,omitempty"`
	// AnchorsQuery is the tracker's named query that lists the anchors
	// (default work-beads), Backend the tracker backend instance and BeadsDir the
	// tracker directory; both fall back to the decider's configuration
	// (PG_DECIDER_CONFIG: agent_tracker_backend, beads_dir).
	AnchorsQuery string `json:"anchors_query,omitempty"`
	Backend      string `json:"backend,omitempty"`
	BeadsDir     string `json:"beads_dir,omitempty"`

	// RateSource is where the rate-limit signal comes from: old-backend-log (before
	// the daemon cutover; reads RateLog) or daemon-metrics (after it; queries
	// MetricsURL for MetricsQuery). THE KEY THE DAEMON CUTOVER (packet P1-c)
	// CHANGES, from old-backend-log to daemon-metrics. Default old-backend-log.
	RateSource   string `json:"rate_source,omitempty"`
	RateLog      string `json:"rate_log,omitempty"`
	MetricsURL   string `json:"metrics_url,omitempty"`
	MetricsQuery string `json:"metrics_query,omitempty"`
	// RateMaxAge is how old the newest reading may be (a Go duration): default 1h
	// for the log, 15m for the metrics.
	RateMaxAge string `json:"rate_max_age,omitempty"`

	// Floor is the GraphQL budget below which a run ends FLOOR (default 3000), Pace
	// the wait between two PRs (a Go duration, default 2s), MaxPlanAge the bound on
	// the age of the oldest plan record (default 48h).
	Floor      int    `json:"floor,omitempty"`
	Pace       string `json:"pace,omitempty"`
	MaxPlanAge string `json:"max_plan_age,omitempty"`

	// RunRecord and SetFile are the paths of the run record and of the persisted
	// set. Connector and ChangeCmd name the programs the driver runs.
	RunRecord string `json:"run_record,omitempty"`
	SetFile   string `json:"set_file,omitempty"`
	Connector string `json:"connector,omitempty"`
	ChangeCmd string `json:"change_cmd,omitempty"`
}

// StateHome is $XDG_STATE_HOME, else ~/.local/state, else the temporary directory.
func StateHome() string {
	if s := os.Getenv("XDG_STATE_HOME"); s != "" {
		return s
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state")
	}
	return os.TempDir()
}

func configHome() string {
	if s := os.Getenv("XDG_CONFIG_HOME"); s != "" {
		return s
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return os.TempDir()
}

// LoadConfig reads the configuration file: the one flagPath names, else the one
// ConfigEnv names (either MUST exist), else the default path (which MAY be absent).
func LoadConfig(flagPath string) (*Config, error) {
	path, must := flagPath, flagPath != ""
	if path == "" {
		if v := os.Getenv(ConfigEnv); v != "" {
			path, must = v, true
		}
	}
	if path == "" {
		path = filepath.Join(configHome(), "pg-pr-backfill", "config.json")
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && !must {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the driver configuration %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("the driver configuration %s is not JSON: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("the driver configuration %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	switch c.RateSource {
	case "", SourceOldBackendLog, SourceDaemonMetrics:
	default:
		return fmt.Errorf("rate_source %q is neither %s nor %s", c.RateSource, SourceOldBackendLog, SourceDaemonMetrics)
	}
	for k, v := range map[string]string{"pace": c.Pace, "max_plan_age": c.MaxPlanAge, "rate_max_age": c.RateMaxAge} {
		if v == "" {
			continue
		}
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("%s %q is not a duration: %w", k, v, err)
		}
	}
	return nil
}

// WithDefaults fills every key a file and a flag both left empty.
func (c *Config) WithDefaults() {
	if c.AnchorsQuery == "" {
		c.AnchorsQuery = "work-beads"
	}
	if c.RateSource == "" {
		c.RateSource = SourceOldBackendLog
	}
	if c.RateLog == "" {
		c.RateLog = filepath.Join(StateHome(), "pg-connector-pr-github", "events.jsonl")
	}
	if c.MetricsURL == "" {
		c.MetricsURL = "http://127.0.0.1:9090"
	}
	if c.Floor == 0 {
		c.Floor = DefaultFloor
	}
	if c.RunRecord == "" {
		if v := os.Getenv("PG_PR_CHANGE_RUN_RECORD"); v != "" {
			c.RunRecord = v
		} else {
			c.RunRecord = filepath.Join(StateHome(), "pg-pr-change", RunRecordFileName)
		}
	}
	if c.SetFile == "" {
		c.SetFile = filepath.Join(StateHome(), "pg-pr-backfill", "set.json")
	}
}

// PaceDuration is Pace as a duration (DefaultPace when unset).
func (c *Config) PaceDuration() time.Duration {
	if c.Pace == "" {
		return DefaultPace
	}
	d, _ := time.ParseDuration(c.Pace)
	return d
}

// MaxPlanAgeDuration is MaxPlanAge as a duration (DefaultMaxPlanAge when unset).
func (c *Config) MaxPlanAgeDuration() time.Duration {
	if c.MaxPlanAge == "" {
		return DefaultMaxPlanAge
	}
	d, _ := time.ParseDuration(c.MaxPlanAge)
	return d
}

// RateSourceFor builds the configured RateSource.
func (c *Config) RateSourceFor() (RateSource, error) {
	var maxAge time.Duration
	if c.RateMaxAge != "" {
		maxAge, _ = time.ParseDuration(c.RateMaxAge)
	}
	switch c.RateSource {
	case SourceOldBackendLog:
		if maxAge == 0 {
			maxAge = DefaultLogMaxAge
		}
		return LogSource{Path: c.RateLog, MaxAge: maxAge}, nil
	case SourceDaemonMetrics:
		return MetricsSource{BaseURL: c.MetricsURL, Query: c.MetricsQuery, MaxAge: maxAge}, nil
	}
	return nil, fmt.Errorf("rate_source %q is neither %s nor %s", c.RateSource, SourceOldBackendLog, SourceDaemonMetrics)
}
