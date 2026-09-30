package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

// TestNewSessionDeps_wiresExister is the regression for CRITICAL #1: every
// production session.Deps must carry a non-nil Exister, or claudeSessionResumable
// is always false → resume never happens and `ccpool reap` prunes resumable rows
// (ADR 0015). It also proves the wired Exister probes the hook-recorded transcript
// path directly: Exists is true exactly when that path names an existing file.
func TestNewSessionDeps_wiresExister(t *testing.T) {
	deps := newSessionDeps(config.Config{}, nil, nil)
	if deps.Exister == nil {
		t.Fatal("production session.Deps must wire Exister (nil → reap prunes resumable rows)")
	}

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if deps.Exister.Exists(transcript) {
		t.Fatal("Exister must be false before the transcript file exists")
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !deps.Exister.Exists(transcript) {
		t.Errorf("wired production Exister must find the hook-recorded transcript at %q", transcript)
	}
}

// TestNewSessionDeps_wiresCanonicalMCPSettingsPath confirms
// cfg.Claude.CanonicalMCPSettingsPath reaches session.Deps.CanonicalMCPSettingsPath
// (docs/adr/0052-ccpool-mcp-consent-canonical-decisions-consultation.md) — an
// empty config value must stay empty (feature off), and a configured value must
// be threaded through verbatim.
func TestNewSessionDeps_wiresCanonicalMCPSettingsPath(t *testing.T) {
	deps := newSessionDeps(config.Config{}, nil, nil)
	if deps.CanonicalMCPSettingsPath != "" {
		t.Errorf("CanonicalMCPSettingsPath = %q, want empty when config.Claude.CanonicalMCPSettingsPath is unset", deps.CanonicalMCPSettingsPath)
	}

	var cfg config.Config
	cfg.Claude.CanonicalMCPSettingsPath = "/some/canonical/settings.local.json"
	deps = newSessionDeps(cfg, nil, nil)
	if deps.CanonicalMCPSettingsPath != "/some/canonical/settings.local.json" {
		t.Errorf("CanonicalMCPSettingsPath = %q, want /some/canonical/settings.local.json", deps.CanonicalMCPSettingsPath)
	}
}

// TestNewSessionDeps_wiresPoolAndMetricLabelAllowlist: every production
// session.Deps carries the pool root (whose basename is every metric record's
// pool attribute) and the metric-label allowlist of THE POOL'S OWN config. It
// loads configs through config.LoadForPool, the exact seam reap-all uses to
// govern many pools in one process, so each pool's Service is built with its
// own PoolPath (no shared or environment-derived state).
func TestNewSessionDeps_wiresPoolAndMetricLabelAllowlist(t *testing.T) {
	base := isolateLabelerEnv(t)
	poolA := newRegisteredPool(t, base, "poolA")
	poolB := newRegisteredPool(t, base, "poolB")
	if err := os.WriteFile(filepath.Join(poolB, "config.toml"),
		[]byte("[telemetry]\nmetric_label_allowlist = [\"pgrouter.role\", \"pgrouter.kind\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_POOL", "/pools/ENV-MUST-BE-IGNORED")

	cases := []struct {
		name, root, wantPath string
		wantAllow            []string
	}{
		{"default pool", "", "", []string{"pgrouter.role"}},
		{"pool A", poolA, poolA, []string{"pgrouter.role"}},
		{"pool B (configured)", poolB, poolB, []string{"pgrouter.role", "pgrouter.kind"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadForPool(tc.root)
			if err != nil {
				t.Fatal(err)
			}
			deps := newSessionDeps(cfg, nil, nil)
			if deps.PoolPath != tc.wantPath {
				t.Errorf("Deps.PoolPath = %q, want %q", deps.PoolPath, tc.wantPath)
			}
			if !reflect.DeepEqual(deps.MetricLabelAllowlist, tc.wantAllow) {
				t.Errorf("Deps.MetricLabelAllowlist = %v, want %v", deps.MetricLabelAllowlist, tc.wantAllow)
			}
		})
	}
}
