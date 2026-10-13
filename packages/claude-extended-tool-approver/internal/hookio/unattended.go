package hookio

import "strings"

// AutonomousEnvVar is the process-environment signal that a session has no human
// attached. ccpool injects it at launch (`ccpool new --autonomous`, which the
// pg-router ccpool handler passes for every worker), and a hook process inherits the
// launching session's environment, so a PreToolUse hook run by an autonomous worker
// sees it. It is read ONLY at the hook entry point (main.go) and carried into the
// engine as HookInput.Unattended; nothing below main reads the process environment,
// so offline replay (evaluate/baseline/compare) stays deterministic.
const AutonomousEnvVar = "CCPOOL_AUTONOMOUS"

// unattendedPermissionModes are the Claude Code permission modes whose own contract
// is "never ask a human": dontAsk auto-denies every tool call that no allow rule
// pre-approved, so no permission dialog is ever meant to open in it.
//
// WHY ONLY dontAsk. bypassPermissions silently accepts an Ask (measured, pg2-2t9wz),
// so an Ask there never reaches a human at all, and `auto` is an attended mode that
// prompts. A session that is unattended for a reason the permission mode cannot
// express (a ccpool worker launched in some other mode) is covered by the separate
// AutonomousEnvVar signal instead.
//
// WHY THE MODE IS SUFFICIENT EVIDENCE. Observed 2026-10-13 in the transcripts of two
// ccpool workers (one drain-pg2, one review pool, both `"permissionMode":"dontAsk"`):
// this hook returned permissionDecision "ask" and Claude Code opened a permission
// dialog that nobody could answer, even though dontAsk auto-denies every ordinary
// non-allowlisted call within seconds. An explicit hook Ask is the one outcome the
// mode does NOT convert into a denial, so this package has to.
var unattendedPermissionModes = map[string]bool{"dontAsk": true}

// IsUnattended reports whether no human can answer a permission dialog for this tool
// call: the session runs in a permission mode that never asks (dontAsk), or the
// entry point flagged it as an autonomous worker (HookInput.Unattended).
func (h *HookInput) IsUnattended() bool {
	return h.Unattended || unattendedPermissionModes[h.PermissionMode]
}

// unattendedWhy names the signal(s) that made the session unattended, for the denial
// reason the model reads.
func (h *HookInput) unattendedWhy() string {
	var signals []string
	if unattendedPermissionModes[h.PermissionMode] {
		signals = append(signals, "permission mode "+h.PermissionMode)
	}
	if h.Unattended {
		signals = append(signals, "autonomous worker ("+AutonomousEnvVar+"=1)")
	}
	return strings.Join(signals, " and ")
}

// NeverAskWhenUnattended is the delivery policy for an unattended session: a final
// verdict of Ask becomes a Reject whose reason a model can act on, and every other
// verdict passes through untouched.
//
// WHY A REJECT AND NOT AN ABSTAIN. An Ask means "a human must look at this", and
// there is no human. Abstaining would hand the call back to Claude Code's own
// allowlist, which can silently ALLOW it (a worker's allowlist routinely carries
// `Bash(cat:*)`, so abstaining on `cat <secret path>` would permit the very read the
// Ask existed to stop). A deterministic denial with an instruction is the only
// outcome that is both safe and unstalling.
//
// WHY AT THE FACADE. This is applied once, to the FINAL folded verdict of
// engine.EvaluateHook, not inside any rule: every rule keeps judging the call on its
// merits (so an attended session is byte-for-byte what it was), and a new rule that
// returns Ask can never reintroduce an unanswerable dialog, because the policy lives
// where every verdict passes.
//
// The returned result keeps the originating rule's Module and everything else, so the
// ask-log's per-rule statistics still attribute the verdict to the rule that wanted a
// human; only Decision and Reason change.
func NeverAskWhenUnattended(input *HookInput, result RuleResult) RuleResult {
	if result.Decision != Ask || !input.IsUnattended() {
		return result
	}
	result.Decision = Reject
	result.Reason = unattendedDenyReason(input, result)
	return result
}

// unattendedDenyReason builds the denial text. The prefix is a stable marker so a
// converted verdict is greppable in the ask-log; the rule's own words are preserved
// verbatim so the model (and a human reading the log) still sees WHY a human would
// have been asked; the tail says what to do instead.
func unattendedDenyReason(input *HookInput, result RuleResult) string {
	var b strings.Builder
	b.WriteString("unattended session never asks: this session is unattended (")
	b.WriteString(input.unattendedWhy())
	b.WriteString("), so no human can answer a permission dialog and this call is denied instead of stalling the session")
	if result.Module != "" || result.Reason != "" {
		b.WriteString(". Rule ")
		if result.Module != "" {
			b.WriteString(result.Module)
		} else {
			b.WriteString("(unnamed)")
		}
		b.WriteString(" needed a human to approve it: ")
		b.WriteString(result.Reason)
	}
	b.WriteString(". ")
	b.WriteString(unattendedAdvice(result.Module))
	return b.String()
}

// unattendedAdvice says what an unattended worker should do instead, keyed on the
// rule that wanted the human. The two named entries are the causes actually observed
// stalling pool workers; everything else gets the generic instruction.
//
// NOTHING HERE MAY ASSERT WHAT IS INSTALLED OR ON PATH. These strings are read by a
// model in a session this package knows nothing about, and whether a given tool is on
// its PATH is a fact of that session's launch configuration, which changes
// independently of this code. An earlier draft said the toolchain "is expected on
// PATH"; on the pool that was false, and a denial that lies sends the worker looking
// for a way around it. The advice therefore says only what NOT to do and what to
// REPORT, both of which stay true whatever PATH holds (pinned by
// TestUnattendedAdvice_NeverAssertsWhatIsOnPath).
func unattendedAdvice(module string) string {
	switch module {
	case "secrets":
		return "Do not read credential or environment files (.envrc, .env*, .ssh, and the like) and do not search /nix/store for a toolchain. " +
			"If a tool you need is missing from PATH, stop hunting for it and report it as `stuck` naming the missing tool, so the orchestrator can decide; do not retry this call."
	case "env-vars":
		return "Do not replace PATH or set other sensitive environment variables on a command: prepending a directory you were given to the existing value " +
			"(PATH=/some/bin:$PATH) needs no approval, a literal replacement (PATH=/a:/b) does. Do not search /nix/store for a toolchain. " +
			"If a tool you need is missing from PATH, stop hunting for it and report it as `stuck` naming the missing tool, so the orchestrator can decide; do not retry this call as written."
	default:
		return "Do not retry this call or work around the denial. Use an alternative that needs no approval, " +
			"or, if you are blocked, say so in your final report so the orchestrator can decide."
	}
}
