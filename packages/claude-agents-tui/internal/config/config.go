package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	PlanTier              string
	TopupPoolUSD          float64
	TopupPurchaseDate     string
	BurnWindowShort       time.Duration
	BurnWindowLong        time.Duration
	RefreshInterval       time.Duration
	HeadlessInterval      time.Duration
	CaffeinateGrace       time.Duration
	WorkingThreshold      time.Duration
	IdleThreshold         time.Duration
	ConsecutiveIdleChecks int
	MaximumWait           time.Duration
	AutoResumeDelay       time.Duration
	AutoResumeMessage     string
}

type tomlConfig struct {
	PlanTier              *string  `toml:"plan_tier"`
	TopupPoolUSD          *float64 `toml:"topup_pool_usd"`
	TopupPurchaseDate     *string  `toml:"topup_purchase_date"`
	BurnWindowShortS      *int     `toml:"burn_window_short_s"`
	BurnWindowLongS       *int     `toml:"burn_window_long_s"`
	RefreshIntervalMS     *int     `toml:"refresh_interval_ms"`
	HeadlessIntervalS     *int     `toml:"headless_interval_s"`
	CaffeinateGraceS      *int     `toml:"caffeinate_grace_s"`
	WorkingThresholdS     *int     `toml:"working_threshold_s"`
	IdleThresholdS        *int     `toml:"idle_threshold_s"`
	ConsecutiveIdleChecks *int     `toml:"consecutive_idle_checks"`
	MaximumWaitS          *int     `toml:"maximum_wait_s"`
	AutoResumeDelayS      *int     `toml:"auto_resume_delay_s"`
	AutoResumeMessage     *string  `toml:"auto_resume_message"`
}

func defaults() Config {
	return Config{
		PlanTier:              "max_5x",
		TopupPoolUSD:          0,
		TopupPurchaseDate:     "",
		BurnWindowShort:       60 * time.Second,
		BurnWindowLong:        300 * time.Second,
		RefreshInterval:       1 * time.Second,
		HeadlessInterval:      5 * time.Second,
		CaffeinateGrace:       60 * time.Second,
		WorkingThreshold:      30 * time.Second,
		IdleThreshold:         10 * time.Minute,
		ConsecutiveIdleChecks: 3,
		MaximumWait:           2 * time.Hour,
		AutoResumeDelay:       45 * time.Second,
		AutoResumeMessage:     "continue",
	}
}

func Load(path string) (Config, error) {
	cfg := defaults()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	var raw tomlConfig
	if _, err := toml.NewDecoder(f).Decode(&raw); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	apply(&cfg, raw)
	return cfg, nil
}

func apply(cfg *Config, raw tomlConfig) {
	if raw.PlanTier != nil {
		cfg.PlanTier = *raw.PlanTier
	}
	if raw.TopupPoolUSD != nil {
		cfg.TopupPoolUSD = *raw.TopupPoolUSD
	}
	if raw.TopupPurchaseDate != nil {
		cfg.TopupPurchaseDate = *raw.TopupPurchaseDate
	}
	if raw.BurnWindowShortS != nil {
		cfg.BurnWindowShort = time.Duration(*raw.BurnWindowShortS) * time.Second
	}
	if raw.BurnWindowLongS != nil {
		cfg.BurnWindowLong = time.Duration(*raw.BurnWindowLongS) * time.Second
	}
	if raw.RefreshIntervalMS != nil {
		cfg.RefreshInterval = time.Duration(*raw.RefreshIntervalMS) * time.Millisecond
	}
	if raw.HeadlessIntervalS != nil {
		cfg.HeadlessInterval = time.Duration(*raw.HeadlessIntervalS) * time.Second
	}
	if raw.CaffeinateGraceS != nil {
		cfg.CaffeinateGrace = time.Duration(*raw.CaffeinateGraceS) * time.Second
	}
	if raw.WorkingThresholdS != nil {
		cfg.WorkingThreshold = time.Duration(*raw.WorkingThresholdS) * time.Second
	}
	if raw.IdleThresholdS != nil {
		cfg.IdleThreshold = time.Duration(*raw.IdleThresholdS) * time.Second
	}
	if raw.ConsecutiveIdleChecks != nil {
		cfg.ConsecutiveIdleChecks = *raw.ConsecutiveIdleChecks
	}
	if raw.MaximumWaitS != nil {
		cfg.MaximumWait = time.Duration(*raw.MaximumWaitS) * time.Second
	}
	if raw.AutoResumeDelayS != nil {
		cfg.AutoResumeDelay = time.Duration(*raw.AutoResumeDelayS) * time.Second
	}
	if raw.AutoResumeMessage != nil {
		cfg.AutoResumeMessage = *raw.AutoResumeMessage
	}
}

func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = home + "/.config"
	}
	return base + "/claude-agents-tui/config.toml"
}
