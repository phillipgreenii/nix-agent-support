package buildtools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// zrBuildtoolsConfig loads the ZR consumer config fixture and returns its
// buildtools sub-config — the golden-set source of truth mirroring the ZR
// machine config's inline builtins.toJSON block. Tests that used to exercise
// baked-in ZR behavior now inject THIS config, so verdicts are identical to
// pre-refactor behavior yet fully config-driven.
func zrBuildtoolsConfig(t *testing.T) configrules.BuildtoolsConfig {
	t.Helper()
	return configrules.Load("../configrules/testdata/zr-rules.json").Buildtools
}

func TestBuildtools_Approved_Approve(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	commands := []string{
		"gradle build",
		"./gradlew test",
		"pre-commit run --all-files",
		"bats tests/test.bats",
		"bd doctor",
		"bd onboard",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestBuildtools_BdAllSubcommands_Approve(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	commands := []string{
		"bd ready --json",
		"bd show pg2-ce6 --json",
		"bd update pg2-ce6 --claim --json",
		`bd create "Issue title" --description="Details" -t task -p 1 --json`,
		`bd close pg2-ce6 --reason "Done" --json`,
		"bd sync",
		"bd list --json",
		"bd search something --json",
		"bd children pg2-e6p --json",
		`bd comments pg2-ce6 --json`,
		`bd dep add pg2-ce6 --blocked-by pg2-abc`,
		"bd graph pg2-e6p",
		"bd status",
		"bd count",
		"bd version",
		`bd update pg2-ce6 --priority 1 --json`,
		`bd create "Found bug" --description="Details" -p 1 --deps discovered-from:pg2-ce6 --json`,
		`bd supersede pg2-abc --with pg2-xyz`,
		`bd reopen pg2-ce6`,
		`bd query "status:open priority:1" --json`,
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestBuildtools_Prek_Approve(t *testing.T) {
	// pg2-o7ev5: prek is the Rust reimplementation of pre-commit and is in active
	// use here; it must be blanket-approved exactly like pre-commit.
	r := New(zrBuildtoolsConfig(t))
	commands := []string{
		"prek run --all-files",
		"prek run",
		"prek run --files x",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestBuildtools_DevboxSearch_Approve(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "devbox search nodejs"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("devbox search nodejs: got %s, want approve", got.Decision)
	}
}

func TestBuildtools_Npm_Abstain(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "npm install"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("npm install: got %s, want abstain", got.Decision)
	}
}

func TestBuildtools_Name(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	if got := r.Name(); got != "build-tools" {
		t.Errorf("Name() = %q, want build-tools", got)
	}
}

func TestBuildtools_JarXf(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"jar xf", "jar xf /tmp/cache/some.jar", hookio.Approve},
		{"jar cf not approved", "jar cf output.jar src/", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func TestBuildtools_GenerateBuildDeps(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "bin/generate-build-deps"})}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
}

func TestBuildTools_Prove(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	for _, cmd := range []string{"prove -v t/foo.t", "mp/ui/customer/bin/devxp/prove t/bar.t", "yath test"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s want approve", cmd, got.Decision)
		}
	}
}

func TestBuildtools_CueVet(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"cue vet approve", "cue vet ./schemas/ 2>&1", hookio.Approve},
		{"cue vet with path", "cue vet ./common/schemas/", hookio.Approve},
		{"cue export abstain", "cue export ./schemas/", hookio.Abstain},
		{"cue eval abstain", "cue eval ./schemas/", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

// TestBuildtools_ApprovedScripts covers the 5 project scripts migrated from the
// base Go source (and the flat approvedCommands) into buildtools.approvedScripts:
// run directly OR via `bash <script>` / `sh <script>`. env-var prefixes do NOT
// demote them (this rule ignores env), which is the whole reason they live here
// and not only in the flat approvedCommands (which abstains when env is present).
func TestBuildtools_ApprovedScripts(t *testing.T) {
	r := New(zrBuildtoolsConfig(t))
	scripts := []string{
		"zr-proto-regenerate.sh", "pre-merge-protobuf-check",
		"fix-ai-tools-ownership", "pre-merge-py-check", "generate-build-deps",
	}
	for _, s := range scripts {
		for _, cmd := range []string{s, "bin/" + s, "bash " + s, "sh " + s, "FOO=bar " + s} {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			if got := r.Evaluate(input); got.Decision != hookio.Approve {
				t.Errorf("cmd %q: got %s, want approve (migrated approvedScript)", cmd, got.Decision)
			}
		}
	}
}

// TestBuildtools_VerbScopedFromConfig proves a consumer-authored verb-scoped
// approval is honored (the schema is not dead code), and only for the named verb.
func TestBuildtools_VerbScopedFromConfig(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{
		VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
	})
	if got := r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "mytool check ./x"})}); got.Decision != hookio.Approve {
		t.Errorf("mytool check: got %s, want approve", got.Decision)
	}
	if got := r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "mytool run ./x"})}); got.Decision != hookio.Abstain {
		t.Errorf("mytool run: got %s, want abstain (verb not approved)", got.Decision)
	}
}

// --- Task-runner (recipe-dispatcher) verb scoping ---
//
// `just` is the primary task runner in the homelab consumer, but it is a RECIPE
// DISPATCHER, not a fixed-behaviour tool: `just <verb>` runs whatever the nearest
// justfile defines. A blanket approvedTools entry would therefore auto-approve
// `just deploy` / `just terraform apply` / `just converge-synology` (the last of
// which the auto-mode classifier already denies as "runs a real, non-dry-run
// converge that mutates the shared NAS"). The correct shape is
// verbScopedApprovals, per recipe. The verb list below MIRRORS the homelab
// consumer config (homelab `development/agent-support/ceta/rules.example.json`);
// it lives here as test DATA so the base binary keeps no consumer literals.

func justBuildtoolsConfig() configrules.BuildtoolsConfig {
	verbs := []string{
		// local build / test / lint / format
		"build", "check", "check-all", "check-quick", "coverage", "fmt", "lint",
		"lint-rules", "syntax-check", "test", "test-formulas", "test-rules",
		"typecheck", "validate",
		// read-only inspection + documented dry-runs
		"help", "version", "status", "logs", "check-config", "check-synology",
	}
	var cfg configrules.BuildtoolsConfig
	for _, v := range verbs {
		cfg.VerbScopedApprovals = append(cfg.VerbScopedApprovals,
			configrules.VerbScopedApproval{Tool: "just", Verb: v})
	}
	return cfg
}

func TestBuildtools_JustVerbScoped_ApprovedRecipes(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		"just check",
		"just check kprod",
		"just build",
		"just test-rules",
		"just lint-rules",
		"just syntax-check",
		"just status kagents",
		"just check-synology synfra",
		// the dominant observed form: cd into the project, then run the recipe
		"cd /repo/media/management/calibre && just check kprod",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (verb-scoped just recipe)", cmd, got.Decision)
		}
	}
}

func TestBuildtools_JustVerbScoped_MutatingRecipesAbstain(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		// the observed denial: a real converge against the shared NAS
		"just converge-synology synfra",
		"just deploy kinfra",
		"just deploy-remote 192.168.2.120",
		"just undeploy",
		// first-subcommand scoping cannot separate plan from apply
		"just terraform apply",
		"just terraform-infra apply",
		// secret-handling and publishing recipes
		"just pull-unseal-keys",
		"just check-vault",
		"just registry-login",
		"just push",
		// takes arbitrary SQL/PromQL as an argument
		`just query "DROP TABLE events"`,
		// bare `just` runs the justfile's default recipe — no verb to scope on
		"just",
		"cd /repo/infrastructure/machines/ansible && just converge-synology databackup",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (recipe not verb-scoped)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_JustVerbScoped_FlagWithValueAbstains pins the known limitation
// documented in the homelab schema: firstSubcommand skips leading-dash tokens but
// does not know which flags CONSUME a value, so `just -f <path> <verb>` resolves
// the "verb" to <path>. That is fail-safe (Abstain, never a wrong Approve), and
// the consumer docs tell callers to use `cd <dir> && just <verb>` instead.
func TestBuildtools_JustVerbScoped_FlagWithValueAbstains(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		"just -f infrastructure/k3s/prometheus-stack/justfile lint-rules",
		"just --justfile media/management/calibre/justfile check",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (flag value consumed the verb slot)", cmd, got.Decision)
		}
	}
}

// --- Base-only (empty config) guards: the base binary must carry NO ZR
// literals. Under a zero BuildtoolsConfig only the generic tools are approved. ---

func TestBuildtools_EmptyConfig_BaseGenericApproves(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	for _, cmd := range []string{
		"go build ./...", "gradle build", "./gradlew test", "pre-commit run",
		"prek run", "bats tests/", "bd ready", "tilt up",
		"devbox search x", "cue vet ./x", "jar xf /tmp/a.jar",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q under empty config: got %s, want approve (base generic tool)", cmd, got.Decision)
		}
	}
}

func TestBuildtools_EmptyConfig_ZRToolsAbstain(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	for _, cmd := range []string{
		"prove -v t/foo.t", "yath test",
		"zr-proto-regenerate.sh", "bin/generate-build-deps",
		"pre-merge-py-check", "bash pre-merge-protobuf-check", "sh fix-ai-tools-ownership",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q under empty config: got %s, want abstain (ZR tool not baked in base)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_EmptyConfig_JustAbstains guards the decision recorded in the
// homelab schema: `just` MUST NOT be promoted into baseApprovedTools. It is a
// recipe dispatcher whose behavior is defined by the repo, so approval belongs in
// a consumer's verbScopedApprovals, never in the generic base.
func TestBuildtools_EmptyConfig_JustAbstains(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	for _, cmd := range []string{"just check", "just build", "just deploy kinfra"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q under empty config: got %s, want abstain (just is not a base generic tool)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_NoZRLiteralsInSource is the optional base-has-no-ZR-literals
// source scan: quoted ZR-specific tokens MUST NOT appear in the base rule.
func TestBuildtools_NoZRLiteralsInSource(t *testing.T) {
	src, err := os.ReadFile("buildtools.go")
	if err != nil {
		t.Fatalf("read buildtools.go: %v", err)
	}
	text := string(src)
	forbidden := []string{
		`"prove"`, `"yath"`, `"generate-build-deps"`,
		`"zr-proto-regenerate.sh"`, `"pre-merge-protobuf-check"`,
		`"fix-ai-tools-ownership"`, `"pre-merge-py-check"`,
	}
	for _, lit := range forbidden {
		if strings.Contains(text, lit) {
			t.Errorf("ZR literal %s found in buildtools.go — must live only in config", lit)
		}
	}
}
