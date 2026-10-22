// Package bgkill approves `bgkill NAME`: stopping a background job that THIS
// session launched through bgrun and that is still running (bead pg2-1kygx, ADR
// 0104).
//
// # Why a stateful rule, and why bgkill
//
// 30 of 120 auto-mode classifier denials since 2026-09-20 were raw `kill <pid>`
// of the agent's own bgrun jobs: the classifier cannot tell an agent-started pid
// from anyone else's. ceta cannot tell either — it tracks Claude Code background
// SHELL ids (internal/asklog/shells.go, killshell), not OS pids, and a job
// launched through bgrun is detached from that tracking. The operator ruled
// (Phillip, 2026-10-22) for a sanctioned `bgkill NAME` tool built on bgrun's own
// job records, so the kill stays focused on jobs created in the background.
//
// # What is approved
//
// Exactly one shape: `bgkill NAME` — a single, static, path-safe job name, no
// flags (so no --dir, --grace or anything else), no env-assignment prefix, no
// live expansion, and no redirection other than to /dev/null or a descriptor
// dup. And only when the job record proves ALL of:
//
//  1. NAME.meta's first `session:` line equals the hook's session_id (bgrun
//     writes it from CLAUDE_CODE_SESSION_ID; a job another session started, or
//     one started before bgrun recorded sessions, has no match);
//  2. NAME.exit is absent (the job has not finished);
//  3. the recorded pid is alive AND is still the bgrun launcher: its command
//     line names bgrun and, when bgrun recorded it, its start time equals
//     NAME.meta's `pidstart:`. A reused pid, or a record forged to point at
//     dolt or any other process, fails here.
//
// Anything else is NotApplicable (the chain continues to its usual outcome,
// unchanged from before this rule), never Reject: under-matching costs a prompt,
// never a wrong Approve.
//
// # Why bgkill and not raw `kill <pid>`
//
// A raw `kill <recorded pid>` is deliberately NOT approved. The recorded pid is
// bgrun's launcher subshell, not the payload: SIGTERM to it would orphan the
// payload (leaving it running) and, because a pid can be reused after a crash
// that leaves no exit record, the pid check cannot be made exact for a bare pid.
// bgkill signals the payload tree first and re-verifies identity itself, so the
// approval covers a tool that cannot kill anything but its own session's job.
// pkill/killall/-u/process groups/-9 and every unrecorded pid (the launchd-managed
// shared dolt server included) stay exactly as they were.
//
// # State
//
// The job store is injected (JobStore). The live hook passes a FileStore over
// bgrun's state directory; offline replay passes nil, and the rule then
// abstains (NotApplicable) like killshell's nil ShellStore does for its Ask.
package bgkill

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Job is the part of a bgrun job record this rule decides on.
type Job struct {
	// Session is the CLAUDE_CODE_SESSION_ID bgrun recorded; empty when none.
	Session string
	// Finished is true when NAME.exit exists.
	Finished bool
	// LauncherAlive is true when the recorded pid is alive AND is still the
	// bgrun launcher (command line and recorded start time both match).
	LauncherAlive bool
}

// JobStore resolves bgrun job records by name. A nil JobStore means "no records
// available" and the rule abstains.
type JobStore interface {
	// Job returns the record for name and whether one exists.
	Job(name string) (Job, bool)
}

// nameRe is bgrun's own NAME alphabet (bg_validate_name in bg-tools-lib.bash).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Rule struct {
	store JobStore
}

// New constructs the rule. store MAY be nil (offline replay): the rule then
// never approves.
func New(store JobStore) *Rule { return &Rule{store: store} }

func (r *Rule) Name() string { return "bgkill" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("bgkill: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "bgkill" {
			continue
		}
		name, ok := approvableShape(pc)
		if !ok || r.store == nil || input.SessionID == "" {
			continue
		}
		job, found := r.store.Job(name)
		if !found || job.Session != input.SessionID || job.Finished || !job.LauncherAlive {
			continue
		}
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "bgkill: stops job " + name + ", a still-running bgrun job launched by this session",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

// approvableShape reports the job name when pc is exactly `bgkill NAME` (plus
// at most harmless output redirections), and false for every other shape.
func approvableShape(pc cmdparse.ParsedCommand) (string, bool) {
	if len(pc.Args) != 1 || pc.ArgIsLiveExpansion(0) {
		return "", false
	}
	if len(pc.EnvVars) > 0 || pc.EnvCleared || len(pc.ProcessSubstitutions) > 0 || pc.HasHeredoc {
		return "", false
	}
	for _, rd := range pc.Redirections {
		// `2>/dev/null`, `>/dev/null`, `2>&1`: no file other than the null device.
		if rd.LiveExpansion || (rd.Path != "/dev/null" && !strings.HasPrefix(rd.Path, "&")) {
			return "", false
		}
	}
	name := pc.Args[0]
	if !nameRe.MatchString(name) {
		return "", false
	}
	return name, true
}
