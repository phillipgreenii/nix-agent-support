package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// These tests exercise the REAL factory (NewEngineForCWD) end-to-end: the
// configrules loader reads rules.json from XDG_CONFIG_HOME and injects the
// structured kubectl/buildtools sub-configs into their rules (DI, ADR 0033).
// They prove the extraction is genuinely config-driven — identical ZR verdicts
// WITH the config, base-generic abstention WITHOUT it.

const zrFixture = "../rules/configrules/testdata/consumer-rules.json"

const commandBlocksFixture = "../rules/configrules/testdata/command-blocks-rules.json"

// withXDGConfig points XDG_CONFIG_HOME at a temp dir; if fixture != "" it copies
// that rules.json into place, else the config is absent (base behavior).
func withXDGConfig(t *testing.T, fixture string) {
	t.Helper()
	xdg := t.TempDir()
	if fixture != "" {
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatalf("read fixture %s: %v", fixture, err)
		}
		dir := filepath.Join(xdg, "claude-extended-tool-approver")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rules.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
}

func bashHook(cwd, cmd string) *hookio.HookInput {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: b}
}

// TestFactory_ConfigDriven_ZRConfigLoaded: with the ZR rules.json present, the
// factory-built engine reproduces ZR kc/buildtools verdicts.
func TestFactory_ConfigDriven_ZRConfigLoaded(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	withXDGConfig(t, zrFixture)
	cwd := t.TempDir()
	eng := NewEngineForCWD(cwd)

	cases := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// kc alias + read-only ZR plugin verb.
		{"kc read-only wslogs", "bin/kc wslogs -n mp--ui--customer", hookio.Approve},
		// positional dev-scope (sync takes the workspace as a bare positional).
		{"kc sync positional dev workspace", "AWS_PROFILE=dev/developers-dev bin/kc sync -f mp/ui/customer/layouts/test-runner d-phillipg01", hookio.Approve},
		// flag-form dev-scope.
		{"kc syncdev flag dev workspace", "bin/kc syncdev --ws d-phillipg01", hookio.Approve},
		// non-dev positional target must NOT be approved.
		{"kc sync non-dev target abstains", "bin/kc sync -f x prod-target", hookio.NoOpinion},
		// buildtools: migrated ZR tools/scripts.
		{"prove approves", "prove -v t/foo.t", hookio.Approve},
		{"migrated script direct", "proto-regenerate.sh", hookio.Approve},
		{"migrated script via bash", "bash proto-regenerate.sh", hookio.Approve},
		// env-var interaction: the migrated script STAYS Approve with an env prefix,
		// via the buildtools config path — the flat approvedCommands matcher would
		// abstain here (len(EnvVars)>0), which is exactly why it moved to
		// buildtools.approvedScripts.
		{"migrated script with env prefix stays approve", "FOO=bar proto-regenerate.sh", hookio.Approve},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.EvaluateHook(bashHook(cwd, tc.command))
			if got.Decision != tc.want {
				t.Errorf("%s: got %s (%s: %s) want %s", tc.name, got.Decision, got.Module, got.Reason, tc.want)
			}
		})
	}
}

// TestFactory_CommandAwareBlocks_Configured proves the WS3 ssh/vault/curl/
// monorepo blocks drive real decisions end-to-end through the factory-built
// engine, using neutral example data (no consumer-specific strings in
// ceta-core). It also proves the safe-commands ordering fix: a configured
// ssh/vault leaf is DECIDED by its dedicated rule, not pre-approved by
// safe-commands.
func TestFactory_CommandAwareBlocks_Configured(t *testing.T) {
	withXDGConfig(t, commandBlocksFixture)
	cwd := t.TempDir()
	eng := NewEngineForCWD(cwd)

	// NOTE on module attribution: EvaluateHook folds a Bash command's leaves
	// most-restrictive-wins. Before pg2-he22o, an Approve verdict always wrapped
	// as module "engine" ("all sub-commands approved") regardless of which rule
	// actually approved, because the fold's Approve-seed occupied the tie-break's
	// "current" slot on every leaf; since that fix, an Approve now attributes to
	// the deciding rule, but that is not exercised HERE — the Reject/Ask cases
	// below carry wantModule to prove the SSH/VAULT rule (not safe-commands) is
	// the decider — the safe-commands ordering guarantee. The Approve cases assert
	// the decision only (the reordering is what lets the dedicated rule reach the
	// leaf first; safe-commands Abstains on these executables); module attribution
	// for the Approve path is exercised in internal/engine's own tests instead.
	cases := []struct {
		name       string
		command    string
		wantDec    hookio.Decision
		wantModule string // asserted only when non-empty (Reject/Ask decisive leaves)
	}{
		// ssh: decided by the ssh rule, NOT pre-approved by safe-commands.
		{"ssh readonly approved", "ssh host ls -la", hookio.Approve, ""},
		{"ssh disallowed user rejected by ssh rule", "ssh root@host ls", hookio.Reject, "ssh"},
		{"ssh -o password auth rejected by ssh rule", "ssh -oPasswordAuthentication=yes host ls", hookio.Reject, "ssh"},
		{"ssh secret path asked by ssh rule", "ssh host cat /etc/shadow", hookio.Ask, "ssh"},
		{"ssh interactive asked by ssh rule", "ssh host", hookio.Ask, "ssh"},
		// vault: decided by the vault rule.
		{"vault read approved", "vault read secret/foo", hookio.Approve, ""},
		{"vault write asked by vault rule", "vault write secret/foo x=1", hookio.Ask, "vault"},
		// curl: configured domain + per-domain method.
		{"curl allowed domain GET approved", "curl https://api.internal.example/health", hookio.Approve, ""},
		{"curl per-domain POST approved", "curl -X POST https://api.internal.example/submit", hookio.Approve, ""},
		{"curl elsewhere abstains", "curl https://evil.example.test/x", hookio.NoOpinion, ""},
		// monorepo: approved command + dangerous-env deferral.
		{"monorepo approved command", "tc build", hookio.Approve, ""},
		{"monorepo dangerous env defers", "TC_DANGER=1 tc build", hookio.NoOpinion, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.EvaluateHook(bashHook(cwd, tc.command))
			if got.Decision != tc.wantDec {
				t.Errorf("%s: got %s (%s: %s), want %s", tc.name, got.Decision, got.Module, got.Reason, tc.wantDec)
			}
			if tc.wantModule != "" && got.Module != tc.wantModule {
				t.Errorf("%s: decided by module %q, want %q (%s)", tc.name, got.Module, tc.wantModule, got.Reason)
			}
		})
	}
}

// TestFactory_CommandAwareBlocks_NoConfig: with NO rules.json the WS3 rules ship
// only the mechanism — ssh/vault/curl(consumer)/monorepo all Abstain, so nothing
// is auto-approved without consumer data.
func TestFactory_CommandAwareBlocks_NoConfig(t *testing.T) {
	withXDGConfig(t, "") // absent config
	cwd := t.TempDir()
	eng := NewEngineForCWD(cwd)

	for _, cmd := range []string{
		"ssh host ls",
		"ssh root@host rm -rf /",
		"vault read secret/foo",
		"vault write secret/foo x=1",
		"curl https://api.internal.example/health",
		"tc build",
	} {
		got := eng.EvaluateHook(bashHook(cwd, cmd))
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q with no config: got Approve (%s: %s); WS3 rules must defer without consumer data", cmd, got.Module, got.Reason)
		}
	}
}

// TestFactory_ConfigDriven_NoConfig: with NO rules.json, ZR literals are NOT
// baked into the base — kc, ZR plugin verbs, dev-scope, prove, and the migrated
// scripts all fall back to non-approval (Abstain).
func TestFactory_ConfigDriven_NoConfig(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	withXDGConfig(t, "") // absent config
	cwd := t.TempDir()
	eng := NewEngineForCWD(cwd)

	for _, cmd := range []string{
		"bin/kc wslogs -n x",
		"AWS_PROFILE=dev/developers-dev bin/kc sync -f x d-phillipg01",
		"bin/kc exe --ws d-phillipg01 -c c -- bats",
		"prove -v t/foo.t",
		"proto-regenerate.sh",
		"FOO=bar proto-regenerate.sh",
	} {
		got := eng.EvaluateHook(bashHook(cwd, cmd))
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q with no config: got Approve (%s: %s); base must carry no ZR literals", cmd, got.Module, got.Reason)
		}
	}

	// A generic kubectl read-only + a generic build tool still approve with no config.
	for _, cmd := range []string{"kubectl get pods", "gradle build"} {
		got := eng.EvaluateHook(bashHook(cwd, cmd))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q with no config: got %s (%s: %s); base generic must still approve", cmd, got.Decision, got.Module, got.Reason)
		}
	}
}
