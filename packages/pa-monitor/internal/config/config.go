package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/phillipgreenii/pa-monitor/internal/timing"
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
	WaitingFreshWindow time.Duration
	// StaleAfter is how old the authoritative status-line rate_limits capture
	// (rate_limits.*.captured_at) may be before the TUI renders the 5h/7d value
	// as stale(age) rather than a live percentage (ADR 0021 §1). Limits refresh
	// only during interactive status-line renders, so a headless-only period
	// naturally ages the value past this window.
	StaleAfter               time.Duration
	AutoResumeDelay          time.Duration
	AutoResumeMessage        string
	DisruptGrace             time.Duration
	EscalationAfter          time.Duration
	CmuxSidebarEnable        bool
	CmuxSidebarIntervalTicks int
	// AutoRestartOnVersionMismatch opts the cmux-bridge and TUI into
	// re-executing themselves in place when the daemon reports a newer build
	// (see internal/reexec). Default false (opt-in); no defaults() entry — the
	// Go zero value is the intended default.
	AutoRestartOnVersionMismatch bool
	// BridgeSnapshotInterval and BridgeHeartbeatInterval are the two base
	// connection cadences from which the daemon and the cmux-bridge DERIVE their
	// watchdog (PushBudget) and reaper (StaleAfter) windows via internal/timing.
	// Only these two are configurable; the derived values are never set directly,
	// so a config cannot express an inconsistent (inverted) set. See the timing
	// package doc.
	BridgeSnapshotInterval  time.Duration
	BridgeHeartbeatInterval time.Duration
	Decorators              []DecoratorConfig
	OTel                    OTelConfig
	// Pricing is the per-model price table the native CostPricer uses (ADR 0021
	// §3). Sourced from [account.pricing]; built-in defaults populate it so
	// native cost emits with no config (cost is notional on this plan).
	Pricing PricingConfig
}

// ModelPricing is one model's per-million-token USD prices.
type ModelPricing struct {
	InputPerMTok         float64
	OutputPerMTok        float64
	CacheCreationPerMTok float64
	CacheReadPerMTok     float64
}

// PricingConfig is the [account.pricing] block: per-model prices plus a Default
// applied to models absent from the map.
type PricingConfig struct {
	Models  map[string]ModelPricing
	Default ModelPricing
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
//
// Command is shell-split into a binary + args (the binary must be under
// /nix/store/), and Env carries extra environment variables forwarded to the
// child — so a generic decorator can be configured with flags and env from the
// [decorator.env] table instead of a bespoke writeShellScriptBin wrapper.
type DecoratorConfig struct {
	Name      string
	Command   string
	Env       map[string]string
	TimeoutMS int
}

type tomlConfig struct {
	PlanTier                 *string  `toml:"plan_tier"`
	TopupPoolUSD             *float64 `toml:"topup_pool_usd"`
	BurnWindowShortS         *int     `toml:"burn_window_short_s"`
	BurnWindowLongS          *int     `toml:"burn_window_long_s"`
	RefreshIntervalMS        *int     `toml:"refresh_interval_ms"`
	CaffeinateGraceS         *int     `toml:"caffeinate_grace_s"`
	WorkingThresholdS        *int     `toml:"working_threshold_s"`
	IdleThresholdS           *int     `toml:"idle_threshold_s"`
	WaitingFreshWindowS      *int     `toml:"waiting_fresh_window_s"`
	StaleAfterS              *int     `toml:"stale_after_s"`
	AutoResumeDelayS         *int     `toml:"auto_resume_delay_s"`
	AutoResumeMessage        *string  `toml:"auto_resume_message"`
	DisruptGraceS            *int     `toml:"disrupt_grace_s"`
	EscalationAfterS         *int     `toml:"escalation_after_s"`
	CmuxSidebarEnable        *bool    `toml:"cmux_sidebar_enable"`
	CmuxSidebarIntervalTicks *int     `toml:"cmux_sidebar_interval_ticks"`
	// Longer field name than the aligned block above; the comment breaks the
	// gofmt alignment run so the whole struct is not reflowed.
	AutoRestartOnVersionMismatch *bool           `toml:"auto_restart_on_version_mismatch"`
	BridgeSnapshotIntervalMS     *int            `toml:"bridge_snapshot_interval_ms"`
	BridgeHeartbeatIntervalMS    *int            `toml:"bridge_heartbeat_interval_ms"`
	Decorators                   []tomlDecorator `toml:"decorator"`
	OTel                         *tomlOTel       `toml:"otel"`
	Account                      *tomlAccount    `toml:"account"`
}

type tomlAccount struct {
	Pricing *tomlPricing `toml:"pricing"`
}

type tomlPricing struct {
	Default *tomlModelPricing            `toml:"default"`
	Models  map[string]*tomlModelPricing `toml:"models"`
}

type tomlModelPricing struct {
	InputPerMTok         *float64 `toml:"input_per_mtok"`
	OutputPerMTok        *float64 `toml:"output_per_mtok"`
	CacheCreationPerMTok *float64 `toml:"cache_creation_per_mtok"`
	CacheReadPerMTok     *float64 `toml:"cache_read_per_mtok"`
}

type tomlDecorator struct {
	Name      string            `toml:"name"`
	Command   string            `toml:"command"`
	Env       map[string]string `toml:"env"`
	TimeoutMS *int              `toml:"timeout_ms"`
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
		// Governs the idle->dormant (long-idle) age refinement, NOT the
		// working->idle transition (that's the registry activity verdict). 20m so
		// a briefly-quiet session is not prematurely marked dormant. Kept in sync
		// with session.LongIdleThreshold (the TUI display default).
		IdleThreshold: 20 * time.Minute,
		// Default ~2*WorkingThreshold: a fresh "waiting" flag should not have a
		// transcript that has advanced well past statusUpdatedAt.
		WaitingFreshWindow:       60 * time.Second,
		StaleAfter:               10 * time.Minute,
		AutoResumeDelay:          45 * time.Second,
		AutoResumeMessage:        "continue",
		DisruptGrace:             30 * time.Second,
		EscalationAfter:          60 * time.Second,
		CmuxSidebarEnable:        true,
		CmuxSidebarIntervalTicks: 5,
		BridgeSnapshotInterval:   timing.DefaultSnapshotInterval,
		BridgeHeartbeatInterval:  timing.DefaultHeartbeatInterval,
		Pricing:                  defaultPricing(),
	}
}

// defaultPricing is the built-in per-model price table (Anthropic published
// per-MTok rates as of 2026-06: Opus 4.x in5/out25/cc6.25/cr0.50, Sonnet 4.x
// in3/out15/cc3.75/cr0.30, Haiku 4.5 in1/out5/cc1.25/cr0.10). It lets native
// cost emit with no [account.pricing] config; [account.pricing] overrides it.
// Default (unknown-model fallback) matches Opus, the priciest common tier, so
// unknown models are never understated.
func defaultPricing() PricingConfig {
	return PricingConfig{
		Default: ModelPricing{InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
		Models: map[string]ModelPricing{
			"claude-opus-4-7":           {InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
			"claude-opus-4-6":           {InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
			"claude-opus-4-5":           {InputPerMTok: 5, OutputPerMTok: 25, CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.50},
			"claude-sonnet-4-6":         {InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.30},
			"claude-sonnet-4-5":         {InputPerMTok: 3, OutputPerMTok: 15, CacheCreationPerMTok: 3.75, CacheReadPerMTok: 0.30},
			"claude-haiku-4-5-20251001": {InputPerMTok: 1, OutputPerMTok: 5, CacheCreationPerMTok: 1.25, CacheReadPerMTok: 0.10},
		},
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
	defer func() { _ = f.Close() }()

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
	if raw.StaleAfterS != nil {
		cfg.StaleAfter = time.Duration(*raw.StaleAfterS) * time.Second
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
	if raw.AutoRestartOnVersionMismatch != nil {
		cfg.AutoRestartOnVersionMismatch = *raw.AutoRestartOnVersionMismatch
	}
	if raw.CmuxSidebarIntervalTicks != nil {
		cfg.CmuxSidebarIntervalTicks = *raw.CmuxSidebarIntervalTicks
	}
	if raw.BridgeSnapshotIntervalMS != nil {
		cfg.BridgeSnapshotInterval = time.Duration(*raw.BridgeSnapshotIntervalMS) * time.Millisecond
	}
	if raw.BridgeHeartbeatIntervalMS != nil {
		cfg.BridgeHeartbeatInterval = time.Duration(*raw.BridgeHeartbeatIntervalMS) * time.Millisecond
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
		dc := DecoratorConfig{Name: d.Name, Command: d.Command, Env: d.Env}
		if d.TimeoutMS != nil {
			dc.TimeoutMS = *d.TimeoutMS
		}
		cfg.Decorators = append(cfg.Decorators, dc)
	}
	if raw.Account != nil && raw.Account.Pricing != nil {
		applyPricing(&cfg.Pricing, raw.Account.Pricing)
	}
}

// applyPricing merges a parsed [account.pricing] block over the built-in
// defaults: any per-field override replaces that field; a configured model adds
// to / overrides the map entry, seeded from the current Default so a partial
// model block still yields a complete ModelPricing.
func applyPricing(cfg *PricingConfig, raw *tomlPricing) {
	if raw.Default != nil {
		mergeModelPricing(&cfg.Default, raw.Default)
	}
	for name, mp := range raw.Models {
		if mp == nil {
			continue
		}
		cur, ok := cfg.Models[name]
		if !ok {
			// Seed a brand-new model from Default so a partial override still
			// yields complete prices. A model already present (including one
			// legitimately priced at all-zero) keeps its values and is merged
			// onto, not re-seeded.
			cur = cfg.Default
		}
		mergeModelPricing(&cur, mp)
		if cfg.Models == nil {
			cfg.Models = map[string]ModelPricing{}
		}
		cfg.Models[name] = cur
	}
}

func mergeModelPricing(dst *ModelPricing, src *tomlModelPricing) {
	if src.InputPerMTok != nil {
		dst.InputPerMTok = *src.InputPerMTok
	}
	if src.OutputPerMTok != nil {
		dst.OutputPerMTok = *src.OutputPerMTok
	}
	if src.CacheCreationPerMTok != nil {
		dst.CacheCreationPerMTok = *src.CacheCreationPerMTok
	}
	if src.CacheReadPerMTok != nil {
		dst.CacheReadPerMTok = *src.CacheReadPerMTok
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

// ApplyOTelEnv exports the standard OTEL_* env vars from the config's [otel]
// block, but ONLY for env vars that are currently unset — an explicit env
// (e.g. a launchd plist) always wins. This lets the SDK-native otel
// constructors read endpoint/resource-attrs from env without bespoke exporter
// wiring. No OTEL_EXPORTER_OTLP_PROTOCOL is set: the gRPC exporters ignore it.
func ApplyOTelEnv(o OTelConfig) {
	setIfUnset := func(key, val string) {
		if val == "" {
			return
		}
		if _, ok := os.LookupEnv(key); ok {
			return
		}
		_ = os.Setenv(key, val)
	}
	setIfUnset("OTEL_EXPORTER_OTLP_ENDPOINT", o.Endpoint)
	setIfUnset("OTEL_RESOURCE_ATTRIBUTES", encodeResourceAttrs(o.ResourceAttrs))
}

// encodeResourceAttrs renders attrs as the comma list the OTel SDK expects
// (key=value,key=value), sorted for determinism.
func encodeResourceAttrs(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}
