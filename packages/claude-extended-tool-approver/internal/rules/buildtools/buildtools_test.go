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
	// MIRRORS the homelab consumer config's buildtools.valueFlags: only the
	// justfile/working-directory NAVIGATION flags are declared. `just`'s
	// execution-altering value flags (--shell, --shell-arg, -c/--command,
	// -E/--dotenv-path, --chooser, --set) are deliberately absent so their value
	// still lands in the verb slot and the command Abstains.
	cfg.ValueFlags = map[string][]string{
		"just": {"-f", "--justfile", "-d", "--working-directory", "--color"},
	}
	// MIRRORS the homelab consumer config's buildtools.allowedFlags (tc-080p).
	// Its PRESENCE puts `just` in strict flag checking, which is what makes the
	// omissions above load-bearing in EVERY spelling rather than only in the
	// separated one. Every entry is output/verbosity or does strictly less work.
	cfg.AllowedFlags = map[string][]string{
		"just": {
			"--unstable", "-q", "--quiet", "-v", "--verbose", "-n", "--dry-run",
			"--highlight", "--no-highlight", "--no-aliases", "--no-deps",
			"--timestamp", "--time", "--explain",
		},
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

// TestBuildtools_JustVerbScoped_ValueFlagResolvesVerb is the tc-xjoe fix for what
// TestBuildtools_JustVerbScoped_FlagWithValueAbstains used to PIN as a limitation:
// with buildtools.valueFlags declaring `just`'s -f/--justfile and
// -d/--working-directory, the flag's VALUE no longer occupies the verb slot, so
// the `just -f <justfile> <verb>` form — the dominant form in the homelab decision
// DB — resolves to <verb> and the verb-scoped approval applies.
func TestBuildtools_JustVerbScoped_ValueFlagResolvesVerb(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		// separated short + long form
		"just -f infrastructure/k3s/prometheus-stack/justfile lint-rules",
		"just --justfile media/management/calibre/justfile check",
		// glued short + long form
		"just -f=infrastructure/k3s/prometheus-stack/justfile lint-rules",
		"just --justfile=media/management/calibre/justfile check",
		// working-directory, and both navigation flags together
		"just -d media/management/calibre --justfile media/management/calibre/justfile check",
		"just --working-directory=/repo -f /repo/justfile test-rules",
		// a boolean flag before and after a value flag
		"just --unstable -f /repo/justfile --quiet lint-rules",
		// recipe arguments still follow the verb
		"just -f infrastructure/k3s/prometheus-stack/justfile status kagents",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (value flag must not consume the verb slot)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_JustVerbScoped_ValueFlagSafety is the load-bearing half of
// tc-xjoe: resolving MORE commands to a real verb MUST NOT approve any mutating
// recipe. Each command below now resolves its verb correctly and MUST still
// Abstain because the verb is not in verbScopedApprovals.
func TestBuildtools_JustVerbScoped_ValueFlagSafety(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		// the three cases named in the bead's safety requirement
		"just -f infrastructure/machines/ansible/justfile converge-synology synfra",
		"just -f infrastructure/k3s/kinfra/justfile deploy kinfra",
		"just -d infrastructure/machines/monorepod terraform apply",
		// same verbs via the long / glued forms
		"just --justfile=infrastructure/k3s/kinfra/justfile deploy kinfra",
		"just --working-directory /repo --justfile /repo/justfile undeploy",
		"just -f /repo/justfile pull-unseal-keys",
		"just -f /repo/justfile push",
		`just -f /repo/justfile query "DROP TABLE events"`,
		// no verb at all after the flag pair — runs the default recipe
		"just -f /repo/justfile",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (mutating recipe MUST NOT auto-approve)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_JustVerbScoped_MissingValueDoesNotApprove covers the truncated
// invocation: a declared value flag with NO value must neither panic nor approve.
func TestBuildtools_JustVerbScoped_MissingValueDoesNotApprove(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		"just -f",
		"just --justfile",
		"just --working-directory",
		// the verb is consumed as the flag's value, leaving nothing behind
		"just -f check",
		"just --justfile lint-rules",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (no resolvable verb)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_JustVerbScoped_UndeclaredValueFlagAbstains proves the
// deliberate omission of `just`'s execution-altering value flags is load-bearing
// in the SEPARATED form: the flag's value occupies the verb slot, so the command
// Abstains rather than approving a recipe run under an operator-supplied shell /
// dotenv / variable override.
//
// The omission alone does NOT cover the glued form — see
// TestBuildtools_JustVerbScoped_ExecutionFlagsAbstainInEverySpelling, which is
// what actually holds tc-080p closed. This test is kept as the regression pin the
// bead asks for: the separated form must not stop Abstaining.
func TestBuildtools_JustVerbScoped_UndeclaredValueFlagAbstains(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		"just --shell /bin/evil check",
		"just --shell-arg zsh check",
		"just --dotenv-path /tmp/evil.env check",
		"just -E /tmp/evil.env check",
		"just --set IMAGE evil check",
		"just --chooser /bin/evil check",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (undeclared value flag must stay fail-safe)", cmd, got.Decision)
		}
	}
}

// --- tc-080p: allowlist (strict) flag checking ---

// TestBuildtools_JustVerbScoped_ExecutionFlagsAbstainInEverySpelling is the
// tc-080p fix. Every flag that changes HOW an approved recipe executes MUST fail
// to resolve a verb in EVERY spelling — separated, glued with `=`, and bare
// (boolean) — so the verb-scoped approval cannot fire.
//
// The glued column is the reported defect: `just --shell=/bin/evil check` is a
// single dash-token, so the pre-tc-080p resolver skipped it wholesale, resolved
// `check`, and APPROVED a recipe run under an operator-supplied shell.
//
// The bare column is the reason this is an ALLOW list rather than the DENY list
// the bead first proposed: `--no-dotenv`, `--shell-command`, `-g`, `--yes`,
// `--choose`, `-e` and `--allow-missing` all alter execution and none of them
// appear in the bead's proposed deny list, because they take no value and so
// never occupied the verb slot to begin with. Under an allowlist they need no
// enumeration at all — they are simply not on it.
func TestBuildtools_JustVerbScoped_ExecutionFlagsAbstainInEverySpelling(t *testing.T) {
	r := New(justBuildtoolsConfig())
	// The bead's SAFETY REQUIREMENT set: each MUST Abstain glued AND separated.
	valueTaking := []string{
		"--shell", "--shell-arg", "--command", "-c", "--dotenv-path", "-E",
		"--chooser", "--cygpath", "--tempdir", "--set",
		// found by reading `just --version 1.51.0 --help`; all missing from the
		// bead's proposed deny list, all execution-relevant
		"--dotenv-filename", "--justfile-name", "--ceiling", "--command-color",
	}
	for _, f := range valueTaking {
		for _, cmd := range []string{
			"just " + f + "=/bin/evil check",  // glued — the tc-080p defect
			"just " + f + " /bin/evil check",  // separated — already Abstained
			"just " + f + "=/bin/evil deploy", // and a mutating verb, both ways
			"just " + f + " /bin/evil deploy",
			// behind a legitimate navigation flag, so the dangerous flag is not
			// the first token examined
			"just -f /repo/justfile " + f + "=/bin/evil check",
			"just --justfile=/repo/justfile " + f + " /bin/evil check",
		} {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			if got := r.Evaluate(input); got.Decision != hookio.Abstain {
				t.Errorf("cmd %q: got %s, want abstain (execution-altering flag must never resolve a verb)", cmd, got.Decision)
			}
		}
	}
	// Boolean execution-altering flags — no value to displace the verb, so ONLY
	// the allowlist stops these.
	for _, f := range []string{
		"--no-dotenv", "--shell-command", "-g", "--global-justfile", "--yes",
		"--choose", "-e", "--edit", "--allow-missing", "--one",
	} {
		for _, cmd := range []string{
			"just " + f + " check",
			"just -f /repo/justfile " + f + " check",
		} {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
			if got := r.Evaluate(input); got.Decision != hookio.Abstain {
				t.Errorf("cmd %q: got %s, want abstain (undeclared boolean flag must not resolve a verb)", cmd, got.Decision)
			}
		}
	}
}

// TestBuildtools_JustVerbScoped_NonCanonicalFlagSpellingsAbstain pins the three
// spellings the bead calls out explicitly. None of them can be expressed as a
// deny-list entry, and all three are unknown NAMES under the allowlist:
//
//   - `--` end-of-flags: it is a dash-token that parseFlagName refuses to accept,
//     so it can never be declared allowed and always stops resolution. That closes
//     `just -- --shell=/bin/x check` as a bypass.
//   - clustered short flags (`-nq`): the cluster is one token whose name matches no
//     declared flag, so it Abstains rather than silently resolving a verb — even
//     though every flag INSIDE the cluster is individually allowed.
//   - an attached short value (`-E/tmp/evil.env`, `-f/repo/justfile`): likewise one
//     unrecognized token. `-f` attached therefore costs a prompt; the ask-log shows
//     0 uses of that form against 135 uses of the separated `-f <path>`.
func TestBuildtools_JustVerbScoped_NonCanonicalFlagSpellingsAbstain(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		// end-of-flags separator, alone and as a bypass attempt
		"just -- check",
		"just -- --shell=/bin/x check",
		"just --shell=/bin/x -- check",
		"just -f /repo/justfile -- --shell=/bin/x check",
		// clustered short flags (both members individually allowed)
		"just -nq check",
		"just -qv check",
		// attached short value, dangerous and benign alike
		"just -E/tmp/evil.env check",
		"just -c/bin/evil check",
		"just -f/repo/justfile check",
		// a value glued onto a flag declared BOOLEAN contradicts the declaration
		"just --quiet=x check",
		"just --unstable=1 check",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (unrecognized flag spelling must fail closed)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_JustVerbScoped_AllowedFlagsStillApprove is the other half: the
// allowlist must not cost the forms the consumer actually uses. Everything here
// resolved and approved before tc-080p and MUST continue to.
func TestBuildtools_JustVerbScoped_AllowedFlagsStillApprove(t *testing.T) {
	r := New(justBuildtoolsConfig())
	for _, cmd := range []string{
		"just check",
		"just --quiet check",
		"just -q -v check",
		"just --dry-run check",
		"just --unstable --no-deps test-rules",
		// declared value flags, separated and GLUED (tc-xjoe must not regress)
		"just -f /repo/justfile lint-rules",
		"just -f=/repo/justfile lint-rules",
		"just --justfile=/repo/justfile check",
		"just --working-directory=/repo -f /repo/justfile test-rules",
		"just --color=always -f /repo/justfile check",
		// allowed booleans interleaved with declared value flags
		"just --unstable -f /repo/justfile --quiet lint-rules",
		"just --timestamp --justfile /repo/justfile status kagents",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (declared/allowed flags must still resolve the verb)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_AllowedFlags_AbsentEntryIsUnchanged is the compatibility guard
// the bead requires: a tool with NO allowedFlags entry keeps the exact
// pre-tc-080p behavior, INCLUDING the glued hole. Strictness is opt-in per tool,
// so adding the field cannot change a consumer that has not adopted it.
func TestBuildtools_AllowedFlags_AbsentEntryIsUnchanged(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{
		VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
		ValueFlags:          map[string][]string{"mytool": {"-f"}},
	})
	cases := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"plain verb", "mytool check", hookio.Approve},
		{"declared value flag", "mytool -f ./cfg check", hookio.Approve},
		{"unknown boolean flag is skipped", "mytool --whatever check", hookio.Approve},
		{"unknown glued flag is still skipped (pre-existing behavior)", "mytool --shell=/bin/x check", hookio.Approve},
		{"end-of-flags is still skipped", "mytool -- check", hookio.Approve},
		{"unknown separated value flag still eats the verb", "mytool --shell /bin/x check", hookio.Abstain},
		{"unapproved verb", "mytool run", hookio.Abstain},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := r.Evaluate(input); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s (no allowedFlags entry must mean no behavior change)", tt.command, got.Decision, tt.want)
			}
		})
	}
}

// TestBuildtools_AllowedFlags_EmptyEntryIsStrict pins the switch itself: it is the
// PRESENCE of the tool key, not a non-empty list, that turns strictness on. An
// operator who writes `"allowedFlags": {"mytool": []}` means "this tool takes no
// bare flags", and a key whose every entry is malformed collapses to the same
// thing rather than silently reverting to permissive.
func TestBuildtools_AllowedFlags_EmptyEntryIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"empty list", []string{}},
		{"all entries malformed", []string{"check", "", "-", "--", "--opt=x", "--opt:2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(configrules.BuildtoolsConfig{
				VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
				ValueFlags:          map[string][]string{"mytool": {"-f"}},
				AllowedFlags:        map[string][]string{"mytool": tc.flags},
			})
			cases := []struct {
				command string
				want    hookio.Decision
			}{
				{"mytool check", hookio.Approve},
				{"mytool -f ./cfg check", hookio.Approve},
				{"mytool --whatever check", hookio.Abstain},
				{"mytool --shell=/bin/x check", hookio.Abstain},
				{"mytool -- check", hookio.Abstain},
			}
			for _, c := range cases {
				input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": c.command})}
				if got := r.Evaluate(input); got.Decision != c.want {
					t.Errorf("cmd %q: got %s, want %s", c.command, got.Decision, c.want)
				}
			}
		})
	}
}

// TestBuildtools_AllowedFlags_MisdeclarationFailsSafe records the property that
// makes this field safe to author, and is the mirror image of the valueFlags
// hazard documented on parseValueFlagSpec.
//
// Declaring a VALUE-TAKING flag as if it were boolean does not over-skip: the
// value is left in the verb slot, matches no approval, and Abstains. Declaring a
// flag that does not exist changes nothing. So the worst outcome of a wrong entry
// here is a prompt — never a wrong Approve.
func TestBuildtools_AllowedFlags_MisdeclarationFailsSafe(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{
		VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
		// --shell actually TAKES a value; declaring it boolean is the mistake.
		AllowedFlags: map[string][]string{"mytool": {"--shell", "--does-not-exist"}},
	})
	for _, cmd := range []string{
		"mytool --shell /bin/evil check",
		"mytool --shell=/bin/evil check",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (a mis-declared allowed flag must not approve)", cmd, got.Decision)
		}
	}
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "mytool --does-not-exist check"})}
	if got := r.Evaluate(input); got.Decision != hookio.Approve {
		t.Errorf("declaring a nonexistent flag must be inert: got %s, want approve", got.Decision)
	}
}

// TestBuildtools_AllowedFlags_ParseFlagName unit-tests the entry validator. It is
// stricter than parseValueFlagSpec on purpose: an allowed flag consumes no tokens,
// so `:<n>` is meaningless, and `=` would confuse a name with a glued token. Bare
// `-` / `--` are refused so the end-of-flags separator can never be allowlisted.
func TestBuildtools_AllowedFlags_ParseFlagName(t *testing.T) {
	cases := []struct {
		spec     string
		wantName string
		wantOK   bool
	}{
		{"--quiet", "--quiet", true},
		{"-q", "-q", true},
		{"--no-dotenv", "--no-dotenv", true},
		{"quiet", "", false},
		{"", "", false},
		{"-", "", false},
		{"--", "", false},
		{"--set:2", "", false},
		{"--color=always", "", false},
	}
	for _, tt := range cases {
		name, ok := parseFlagName(tt.spec)
		if name != tt.wantName || ok != tt.wantOK {
			t.Errorf("parseFlagName(%q) = (%q, %v), want (%q, %v)", tt.spec, name, ok, tt.wantName, tt.wantOK)
		}
	}
}

// TestBuildtools_ValueFlags_NoDeclarationIsUnchanged is the no-regression guard:
// a verb-scoped tool with NO valueFlags entry behaves exactly as before the
// tc-xjoe change — dash tokens are skipped and the first remaining token is the
// verb, so a flag's value still lands in the verb slot and Abstains.
func TestBuildtools_ValueFlags_NoDeclarationIsUnchanged(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{
		VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
	})
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"mytool check ./x", hookio.Approve},
		{"mytool --verbose check", hookio.Approve},
		{"mytool -f ./cfg check", hookio.Abstain},
		{"mytool run ./x", hookio.Abstain},
	}
	for _, tt := range cases {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
		if got := r.Evaluate(input); got.Decision != tt.want {
			t.Errorf("cmd %q: got %s, want %s (undeclared-tool behavior must not change)", tt.command, got.Decision, tt.want)
		}
	}
}

// TestBuildtools_ValueFlags_Arity covers the generic multi-token mechanism
// (`--set NAME VALUE`-shaped flags, declared as `--flag:2`) and the rejection of a
// malformed arity suffix, on a synthetic tool so the base carries no consumer data.
func TestBuildtools_ValueFlags_Arity(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{
		VerbScopedApprovals: []configrules.VerbScopedApproval{{Tool: "mytool", Verb: "check"}},
		ValueFlags: map[string][]string{
			"mytool": {"--opt:2", "--one", "--bad:x", "--worse:0", "--neg:-1"},
		},
	})
	cases := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"two-token flag resolves the verb", "mytool --opt NAME VALUE check", hookio.Approve},
		{"two-token flag glued supplies the first value", "mytool --opt=NAME VALUE check", hookio.Approve},
		{"two-token flag truncated consumes the verb", "mytool --opt NAME check", hookio.Abstain},
		{"one-token flag resolves the verb", "mytool --one v check", hookio.Approve},
		{"malformed arity suffix is dropped", "mytool --bad v check", hookio.Abstain},
		{"zero arity is dropped", "mytool --worse v check", hookio.Abstain},
		{"negative arity is dropped", "mytool --neg v check", hookio.Abstain},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := r.Evaluate(input); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s", tt.command, got, tt.want)
			}
		})
	}
}

// TestBuildtools_ValueFlags_ParseSpec unit-tests the spec parser directly, since
// its reject-rather-than-default behavior is the guard against a declaration that
// over-skips tokens (the only direction that can manufacture a wrong Approve).
func TestBuildtools_ValueFlags_ParseSpec(t *testing.T) {
	cases := []struct {
		spec      string
		wantName  string
		wantArity int
		wantOK    bool
	}{
		{"--justfile", "--justfile", 1, true},
		{"-f", "-f", 1, true},
		{"--set:2", "--set", 2, true},
		{"--big:3", "--big", 3, true},
		{"--set:x", "", 0, false},
		{"--set:0", "", 0, false},
		{"--set:-1", "", 0, false},
		{"--set:", "", 0, false},
		{"justfile", "", 0, false},
		{"", "", 0, false},
		{"-", "", 0, false},
		{"--", "", 0, false},
	}
	for _, tt := range cases {
		name, arity, ok := parseValueFlagSpec(tt.spec)
		if name != tt.wantName || arity != tt.wantArity || ok != tt.wantOK {
			t.Errorf("parseValueFlagSpec(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tt.spec, name, arity, ok, tt.wantName, tt.wantArity, tt.wantOK)
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

// --- Base-generic verb resolution (bead tc-457w) ---
//
// The base path behind `devbox search` / `cue vet` / `jar xf` used to skip every
// dash-prefixed token with no allowlist, which is the wrong-approve class tc-080p
// fixed for consumer-configured tools. These tests pin the strict replacement.

// TestBuildtools_BaseVerbs_PinnedApprovals is the no-regression pin: the exact
// spellings the base has always approved MUST keep approving, and the pre-verb
// flags enumerated as output-only in baseVerbFlags MUST be accepted. A future
// widening of this set is visible as a diff to this list.
func TestBuildtools_BaseVerbs_PinnedApprovals(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	for _, cmd := range []string{
		// canonical, no pre-verb flags
		"devbox search nodejs",
		"cue vet ./schemas/",
		"jar xf /tmp/cache/some.jar",
		// devbox: the root command's only persistent flags
		"devbox -q search nodejs",
		"devbox --quiet search nodejs",
		// cue: the root command's persistent flags, singly and combined
		"cue -E vet ./x",
		"cue --all-errors vet ./x",
		"cue -i vet ./x",
		"cue --ignore vet ./x",
		"cue -s vet ./x",
		"cue --simplify vet ./x",
		"cue -E -i -s vet ./x",
		// post-verb flags never reach verb resolution
		"devbox search --show-all nodejs",
		"cue vet -c ./x",
		"jar xf /tmp/a.jar META-INF/MANIFEST.MF",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (pinned base approval)", cmd, got.Decision)
		}
	}
}

// TestBuildtools_BaseVerbs_UnrecognisedFlagAbstains is the fix itself: a dash
// token that is not on the built-in allowlist resolves NO verb, in every spelling
// that defeated the old dash-skipping resolver.
//
// The `jar --create --file=<path> xf` case is the measured exploit that motivated
// the change — jar creates and OVERWRITES <path> there, while the old resolver
// approved it as "jar xf (extraction)". See baseVerbFlags for the enumeration.
func TestBuildtools_BaseVerbs_UnrecognisedFlagAbstains(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	cases := []struct {
		name    string
		command string
	}{
		{"jar exploit: glued long flag flips extract to create", "jar --create --file=/etc/motd xf"},
		{"jar exploit: short mode flag", "jar -c --file=/etc/motd xf"},
		{"jar exploit: extract from an attacker-named archive", "jar -x --file=/tmp/evil.jar xf"},
		{"jar: no flag may precede the legacy operand", "jar -v xf /tmp/a.jar"},
		{"jar: end-of-flags separator is never allowlisted", "jar -- xf /tmp/a.jar"},
		{"devbox: undeclared glued flag", "devbox --config=/tmp/evil search nodejs"},
		{"devbox: undeclared bare flag", "devbox --debug search nodejs"},
		{"devbox: clustered shorts fail closed", "devbox -qh search nodejs"},
		{"devbox: value glued onto a boolean flag", "devbox --quiet=x search nodejs"},
		{"devbox: end-of-flags separator", "devbox -- search nodejs"},
		{"cue: undeclared glued flag", "cue --schema=/tmp/evil vet ./x"},
		{"cue: undeclared bare flag", "cue --inject-vars vet ./x"},
		{"cue: attached short value", "cue -E/tmp/x vet ./x"},
		{"cue: clustered shorts fail closed", "cue -Eis vet ./x"},
		{"cue: end-of-flags separator", "cue -- vet ./x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tc.command})}
			if got := r.Evaluate(input); got.Decision != hookio.Abstain {
				t.Errorf("cmd %q: got %s, want abstain", tc.command, got.Decision)
			}
		})
	}
}

// TestBuildtools_BaseVerbs_WrongVerbAbstains guards the verb slot itself: the
// strict policy must not accidentally widen which verb resolves.
func TestBuildtools_BaseVerbs_WrongVerbAbstains(t *testing.T) {
	r := New(configrules.BuildtoolsConfig{})
	for _, cmd := range []string{
		"devbox run build", "devbox -q shell", "devbox",
		"cue export ./x", "cue -E eval ./x", "cue cmd deploy", "cue",
		"jar cf out.jar src/", "jar xvf /tmp/a.jar", "jar",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

// TestBuildtools_BaseFlagPolicies_AllStrict pins the invariant that makes
// baseVerbIs safe: every base-generic tool carries a STRICT policy, and jar's is
// empty. A new entry added without strict:true would silently reinstate
// unconditional dash-skipping for that tool.
func TestBuildtools_BaseFlagPolicies_AllStrict(t *testing.T) {
	for _, tool := range []string{"devbox", "cue", "jar"} {
		policy, ok := baseFlagPolicies[tool]
		if !ok {
			t.Fatalf("%s: no base flag policy compiled", tool)
		}
		if !policy.strict {
			t.Errorf("%s: base flag policy is not strict", tool)
		}
		if len(policy.valueFlags) != 0 {
			t.Errorf("%s: base policy declares value flags %v; over-declaring arity is the only direction that manufactures a wrong Approve", tool, policy.valueFlags)
		}
	}
	if n := len(baseFlagPolicies["jar"].allowed); n != 0 {
		t.Errorf("jar: allowlist has %d entries, want 0 (no dash token may precede the legacy `xf` operand)", n)
	}
}

// TestBuildtools_BaseVerbIs_UnknownToolFailsClosed pins the fail-closed default:
// a tool with no compiled policy must resolve nothing rather than fall through to
// the permissive zero flagPolicy.
func TestBuildtools_BaseVerbIs_UnknownToolFailsClosed(t *testing.T) {
	if baseVerbIs([]string{"check"}, "not-a-base-tool", "check") {
		t.Error("baseVerbIs resolved a verb for a tool with no compiled policy")
	}
}

// TestBuildtools_FirstSubcommand_StrictGuardTruthTable pins firstSubcommand's
// strict guard over the COMPLETE boolean space its condition reads: strict ×
// declared-value-flag × declared-allowed-flag × glued = 16 rows, all enumerated.
//
// It exists because that guard is the security-relevant one — it is what stops a
// glued value (`--shell=/bin/x <verb>`) from being skipped as a single dash-token
// so the real verb resolves and approves — and because the guard was rewritten
// from `!(p.allowed[name] && !glued)` to a named `bareAllowed` for staticcheck
// QF1001. Enumerating the space makes any future re-spelling of the condition
// (De Morgan or otherwise) provably behavior-preserving rather than asserted to be.
//
// Every row uses the SAME argv shape — `<dash-token> VAL verb` — so the outcome
// alone distinguishes the three reachable behaviors: "" (guard fired, nothing can
// approve), "VAL" (the dash-token was skipped and consumed no following token), and
// "verb" (the dash-token consumed VAL as its value, so the verb slot resolved).
func TestBuildtools_FirstSubcommand_StrictGuardTruthTable(t *testing.T) {
	const flag = "-f"
	tests := []struct {
		strict  bool
		valFlag bool // flag is a declared value flag (arity 1)
		allowed bool // flag is a declared allowed (boolean) flag
		glued   bool // spelled `-f=VAL` rather than bare `-f`
		want    string
	}{
		// Not strict: a dash-token is always skipped, so the guard is unreachable
		// and only the declared arity moves the result.
		{strict: false, valFlag: false, allowed: false, glued: false, want: "VAL"},
		{strict: false, valFlag: false, allowed: false, glued: true, want: "VAL"},
		{strict: false, valFlag: false, allowed: true, glued: false, want: "VAL"},
		{strict: false, valFlag: false, allowed: true, glued: true, want: "VAL"},
		{strict: false, valFlag: true, allowed: false, glued: false, want: "verb"},
		{strict: false, valFlag: true, allowed: false, glued: true, want: "VAL"},
		{strict: false, valFlag: true, allowed: true, glued: false, want: "verb"},
		{strict: false, valFlag: true, allowed: true, glued: true, want: "VAL"},

		// Strict + NOT a declared value flag: the guard's live quadrant. Only the
		// BARE declared allowed flag survives; an undeclared flag dies either
		// spelling, and an allowed flag carrying a glued value dies because a
		// boolean-by-declaration flag taking a value contradicts the declaration.
		{strict: true, valFlag: false, allowed: false, glued: false, want: ""},
		{strict: true, valFlag: false, allowed: false, glued: true, want: ""},
		{strict: true, valFlag: false, allowed: true, glued: false, want: "VAL"},
		{strict: true, valFlag: false, allowed: true, glued: true, want: ""},

		// Strict + a declared value flag: declared arity wins, guard never fires.
		{strict: true, valFlag: true, allowed: false, glued: false, want: "verb"},
		{strict: true, valFlag: true, allowed: false, glued: true, want: "VAL"},
		{strict: true, valFlag: true, allowed: true, glued: false, want: "verb"},
		{strict: true, valFlag: true, allowed: true, glued: true, want: "VAL"},
	}
	if len(tests) != 16 {
		t.Fatalf("truth table has %d rows, want all 16 combinations", len(tests))
	}
	seen := map[[4]bool]bool{}
	for _, tt := range tests {
		key := [4]bool{tt.strict, tt.valFlag, tt.allowed, tt.glued}
		if seen[key] {
			t.Fatalf("duplicate combination %v in truth table", key)
		}
		seen[key] = true

		p := flagPolicy{strict: tt.strict}
		if tt.valFlag {
			p.valueFlags = map[string]int{flag: 1}
		}
		if tt.allowed {
			p.allowed = map[string]bool{flag: true}
		}
		tok := flag
		if tt.glued {
			tok = flag + "=x"
		}
		if got := firstSubcommand([]string{tok, "VAL", "verb"}, p); got != tt.want {
			t.Errorf("firstSubcommand(%q strict=%v valFlag=%v allowed=%v) = %q, want %q",
				tok, tt.strict, tt.valFlag, tt.allowed, got, tt.want)
		}
	}
}
