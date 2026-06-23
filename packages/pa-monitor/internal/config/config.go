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
	PlanTier         string
	TopupPoolUSD     float64
	BurnWindowShort  time.Duration
	BurnWindowLong   time.Duration
	RefreshInterval  time.Duration
	CaffeinateGrace  time.Duration
	WorkingThreshold time.Duration
	IdleThreshold    time.Duration
	// WaitingFreshWindow bounds how far the transcript may have advanced past
	// the registry's statusUpdatedAt before a "waiting" flag is treated as
	// stale (and ignored, falling through to Idle). See the registry-driven
	// activity verdict (claude-transcript.ClassifyActivity) §4.3.
	WaitingFreshWindow       time.Duration
	AutoResumeDelay          time.Duration
	AutoResumeMessage        string
	DisruptGrace             time.Duration
	EscalationAfter          time.Duration
	CmuxSidebarEnable        bool
	CmuxSidebarIntervalTicks int
	Decorators               []DecoratorConfig
	OTel                     OTelConfig
}

// OTelConfig is the [otel] block. Endpoint is the OTLP gRPC endpoint
// (an http:// scheme selects insecure gRPC). There is intentionally no
// protocol field: the emitters import the gRPC-only exporter packages, so
// transport is fixed at compile time and OTEL_EXPORTER_OTLP_PROTOCOL is a
// no-op. ResourceAttrs becomes OTEL_RESOURCE_ATTRIBUTES.
type OTelConfig struct {
	Endpoint      string
	ResourceAttrs map[string]string
}

// DecoratorConfig is one [[decorator]] block parsed from config.toml. The
// daemon turns each entry into a labels.Decorator that shells out to the
// command on every session-label refresh. See ADR-0011 for why per-host
// extension lives here and not in detector code.
type DecoratorConfig struct {
	Name      string
	Command   string
	TimeoutMS int
}

type tomlConfig struct {
	PlanTier                 *string         `toml:"plan_tier"`
	TopupPoolUSD             *float64        `toml:"topup_pool_usd"`
	BurnWindowShortS         *int            `toml:"burn_window_short_s"`
	BurnWindowLongS          *int            `toml:"burn_window_long_s"`
	RefreshIntervalMS        *int            `toml:"refresh_interval_ms"`
	CaffeinateGraceS         *int            `toml:"caffeinate_grace_s"`
	WorkingThresholdS        *int            `toml:"working_threshold_s"`
	IdleThresholdS           *int            `toml:"idle_threshold_s"`
	WaitingFreshWindowS      *int            `toml:"waiting_fresh_window_s"`
	AutoResumeDelayS         *int            `toml:"auto_resume_delay_s"`
	AutoResumeMessage        *string         `toml:"auto_resume_message"`
	DisruptGraceS            *int            `toml:"disrupt_grace_s"`
	EscalationAfterS         *int            `toml:"escalation_after_s"`
	CmuxSidebarEnable        *bool           `toml:"cmux_sidebar_enable"`
	CmuxSidebarIntervalTicks *int            `toml:"cmux_sidebar_interval_ticks"`
	Decorators               []tomlDecorator `toml:"decorator"`
	OTel                     *tomlOTel       `toml:"otel"`
}

type tomlDecorator struct {
	Name      string `toml:"name"`
	Command   string `toml:"command"`
	TimeoutMS *int   `toml:"timeout_ms"`
}

type tomlOTel struct {
	Endpoint      *string           `toml:"endpoint"`
	ResourceAttrs map[string]string `toml:"resource_attributes"`
}

func defaults() Config {
	return Config{
		PlanTier:         "max_5x",
		TopupPoolUSD:     0,
		BurnWindowShort:  60 * time.Second,
		BurnWindowLong:   300 * time.Second,
		RefreshInterval:  1 * time.Second,
		CaffeinateGrace:  60 * time.Second,
		WorkingThreshold: 30 * time.Second,
		IdleThreshold:    10 * time.Minute,
		// Default ~2*WorkingThreshold: a fresh "waiting" flag should not have a
		// transcript that has advanced well past statusUpdatedAt.
		WaitingFreshWindow:       60 * time.Second,
		AutoResumeDelay:          45 * time.Second,
		AutoResumeMessage:        "continue",
		DisruptGrace:             30 * time.Second,
		EscalationAfter:          60 * time.Second,
		CmuxSidebarEnable:        true,
		CmuxSidebarIntervalTicks: 5,
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
	if raw.BurnWindowShortS != nil {
		cfg.BurnWindowShort = time.Duration(*raw.BurnWindowShortS) * time.Second
	}
	if raw.BurnWindowLongS != nil {
		cfg.BurnWindowLong = time.Duration(*raw.BurnWindowLongS) * time.Second
	}
	if raw.RefreshIntervalMS != nil {
		cfg.RefreshInterval = time.Duration(*raw.RefreshIntervalMS) * time.Millisecond
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
	if raw.WaitingFreshWindowS != nil {
		cfg.WaitingFreshWindow = time.Duration(*raw.WaitingFreshWindowS) * time.Second
	}
	if raw.AutoResumeDelayS != nil {
		cfg.AutoResumeDelay = time.Duration(*raw.AutoResumeDelayS) * time.Second
	}
	if raw.AutoResumeMessage != nil {
		cfg.AutoResumeMessage = *raw.AutoResumeMessage
	}
	if raw.DisruptGraceS != nil {
		cfg.DisruptGrace = time.Duration(*raw.DisruptGraceS) * time.Second
	}
	if raw.EscalationAfterS != nil {
		cfg.EscalationAfter = time.Duration(*raw.EscalationAfterS) * time.Second
	}
	if raw.CmuxSidebarEnable != nil {
		cfg.CmuxSidebarEnable = *raw.CmuxSidebarEnable
	}
	if raw.CmuxSidebarIntervalTicks != nil {
		cfg.CmuxSidebarIntervalTicks = *raw.CmuxSidebarIntervalTicks
	}
	if raw.OTel != nil {
		if raw.OTel.Endpoint != nil {
			cfg.OTel.Endpoint = *raw.OTel.Endpoint
		}
		if raw.OTel.ResourceAttrs != nil {
			cfg.OTel.ResourceAttrs = raw.OTel.ResourceAttrs
		}
	}
	for _, d := range raw.Decorators {
		dc := DecoratorConfig{Name: d.Name, Command: d.Command}
		if d.TimeoutMS != nil {
			dc.TimeoutMS = *d.TimeoutMS
		}
		cfg.Decorators = append(cfg.Decorators, dc)
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
	return base + "/pa-monitor/config.toml"
}
