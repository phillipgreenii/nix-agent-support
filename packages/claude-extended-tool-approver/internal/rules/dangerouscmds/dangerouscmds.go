// Package dangerouscmds is a blanket denylist for inherently dangerous
// executables that must NEVER be auto-approved in a Claude session (a
// hook-support parity capability; DangerousCommandEvaluator).
//
// Decision policy: Reject — a hard block matching hook-support's DENY, and
// consistent with ceta's `assume`/`config-rules` block convention. Because the
// engine evaluates a Bash compound leaf-by-leaf and folds most-restrictive-wins
// (Approve < Abstain < Ask < Reject), a dangerous leaf anywhere in a compound
// (e.g. `git status && sudo rm -rf /`) demotes the whole command to Reject.
//
// Scope note: the denylist below is the RATIFIED set for this bead, reconciled
// against hook-support's `dangerous_commands`. Deliberate differences:
//   - `curl` and `ssh`/`scp` are NOT here — they have dedicated rules (`curl`,
//     `ssh`) with allowlist/read-only logic; a blanket Reject would defeat them.
//   - `kill`/`killall`/`pkill`/`modprobe`/`insmod`/`rmmod` (in hook-support) are
//     intentionally omitted from this bead's scope.
//   - both `ncat` and `netcat` are included so either netcat spelling is caught.
//   - `mkfs` matches the bare name and any `mkfs.<fstype>` variant.
package dangerouscmds

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// dangerous is the exact-match denylist of dangerous executable basenames.
var dangerous = map[string]bool{
	"sudo": true, "su": true, "doas": true,
	"dd": true, "fdisk": true, "parted": true,
	"mount": true, "umount": true,
	"reboot": true, "shutdown": true, "halt": true, "poweroff": true,
	"wget": true, "nc": true, "ncat": true, "netcat": true, "telnet": true, "sftp": true,
}

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "dangerous-commands" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		base := filepath.Base(pc.Executable)
		if isDangerous(base) {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "dangerous command blocked: " + base,
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// isDangerous reports whether base is on the denylist, including any
// `mkfs.<fstype>` filesystem-builder variant.
func isDangerous(base string) bool {
	if dangerous[base] {
		return true
	}
	return base == "mkfs" || strings.HasPrefix(base, "mkfs.")
}
