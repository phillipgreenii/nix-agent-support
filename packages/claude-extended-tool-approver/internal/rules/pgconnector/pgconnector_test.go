package pgconnector

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// evalPgConnector is the shared one-command driver, mirroring pgpr's evalPgPR.
func evalPgConnector(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(cmd),
	}))
}

// TestPgConnector_IssueGrantedVerbs_Approve pins the four verbs
// PG_ROUTER_CCPOOL_HANDLER_CONFIG's allowedTools already grants (bead
// pg2-s9zh5) to Approve, bare and with a --backend flag.
func TestPgConnector_IssueGrantedVerbs_Approve(t *testing.T) {
	cmds := []string{
		"pg-connector issue show pg2-itxtv",
		"pg-connector issue show pg2-itxtv --backend pg-connector-issue-beads",
		"pg-connector issue comment pg2-itxtv --body hi",
		"pg-connector issue update pg2-itxtv --add-label human",
		"pg-connector issue close pg2-itxtv",
	}
	for _, cmd := range cmds {
		if got := evalPgConnector(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgConnector_EnvVarPrefix_StillApprove is the bead pg2-r848s regression
// case: the escalation-triager's prompt instructs it to prefix EVERY
// pg-connector invocation with PG_CONNECTOR_ISSUE_BEADS_DIR=<dir> (per
// docs/superpowers/specs/2026-09-22-pg-router-ccpool-escalation-design.md's
// "Tracker targeting" section). That prefix breaks Claude Code's own native
// --allowedTools literal-text-prefix match against
// "Bash(pg-connector issue show:*)" (the command text no longer starts with
// "pg-connector"). This rule must still Approve because cmdparse lifts the
// leading VAR=value assignment into EnvVars before Evaluate ever sees
// Executable/Args, making the decision invocation-shape-independent.
func TestPgConnector_EnvVarPrefix_StillApprove(t *testing.T) {
	cmds := []string{
		"PG_CONNECTOR_ISSUE_BEADS_DIR=/Users/phillipg/phillipg_mbp pg-connector issue show pg2-itxtv",
		"PG_CONNECTOR_ISSUE_BEADS_DIR=/Users/phillipg/phillipg_mbp pg-connector issue comment pg2-itxtv --body hi",
		"PG_CONNECTOR_ISSUE_BEADS_DIR=/Users/phillipg/phillipg_mbp pg-connector issue update pg2-itxtv --add-label human",
		"PG_CONNECTOR_ISSUE_BEADS_DIR=/Users/phillipg/phillipg_mbp pg-connector issue close pg2-itxtv",
	}
	for _, cmd := range cmds {
		if got := evalPgConnector(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgConnector_UngrantedIssueVerbs_Unclaimed pins every `pg-connector
// issue` verb OUTSIDE the four-verb grant (create/list/transition/deps/
// changes) to NoOpinion (unclaimed) — this rule must not widen approval past
// what PG_ROUTER_CCPOOL_HANDLER_CONFIG's allowedTools actually authorizes.
func TestPgConnector_UngrantedIssueVerbs_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-connector issue create --title x",
		"pg-connector issue list",
		"pg-connector issue transition pg2-itxtv --state done",
		"pg-connector issue deps pg2-itxtv",
		"pg-connector issue changes escalated-work",
	}
	for _, cmd := range cmds {
		if got := evalPgConnector(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgConnector_OtherResourcesAndBinaries_Unclaimed pins every OTHER
// pg-connector resource (not "issue") and, per acceptance criterion 4, the
// SEPARATE ccpool/pg-router binaries this bug report says already succeed —
// to NoOpinion. This rule checks Executable == "pg-connector" before doing
// anything else, so ccpool/pg-router can never reach (let alone be affected
// by) its issue-verb classification.
func TestPgConnector_OtherResourcesAndBinaries_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-connector pr files",
		"pg-connector ci runs",
		"pg-connector scm worktree add",
		"pg-connector config show",
		"pg-connector --help",
		"pg-connector issue --help",
		"ccpool list",
		"ccpool state pg-router-pg2-escalation-triager-pg2-itxtv-20260929T061947.457718000",
		"pg-router run-role escalation-triager",
	}
	for _, cmd := range cmds {
		if got := evalPgConnector(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed by THIS rule)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgConnector_OutputFlagBeforeResource_StillApprove exercises pg-connector's
// persistent root --output flag (output.go's addOutputFlag) placed BEFORE the
// resource/verb path, confirming pgConnectorCommandWordIndexes skips it
// (mirroring pgpr's own flag-skipping resolver test coverage).
func TestPgConnector_OutputFlagBeforeResource_StillApprove(t *testing.T) {
	cmds := []string{
		"pg-connector --output json issue show pg2-itxtv",
		"pg-connector --output=json issue show pg2-itxtv",
	}
	for _, cmd := range cmds {
		if got := evalPgConnector(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}
