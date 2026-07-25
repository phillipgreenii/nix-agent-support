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

const zrFixture = "../rules/configrules/testdata/zr-rules.json"

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
		{"kc sync non-dev target abstains", "bin/kc sync -f x prod-target", hookio.Abstain},
		// buildtools: migrated ZR tools/scripts.
		{"prove approves", "prove -v t/foo.t", hookio.Approve},
		{"migrated script direct", "zr-proto-regenerate.sh", hookio.Approve},
		{"migrated script via bash", "bash zr-proto-regenerate.sh", hookio.Approve},
		// env-var interaction: the migrated script STAYS Approve with an env prefix,
		// via the buildtools config path — the flat approvedCommands matcher would
		// abstain here (len(EnvVars)>0), which is exactly why it moved to
		// buildtools.approvedScripts.
		{"migrated script with env prefix stays approve", "FOO=bar zr-proto-regenerate.sh", hookio.Approve},
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
		"zr-proto-regenerate.sh",
		"FOO=bar zr-proto-regenerate.sh",
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
