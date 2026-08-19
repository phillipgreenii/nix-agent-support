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
//
// Operand gating (pg2-2nm54): membership on the denylist is by EXECUTABLE NAME, so
// an entry whose destructive power lives entirely in its OPERANDS over-blocks its
// own read-only query form. `mount` is the one such entry — with no operands it
// prints the mounted-filesystem list and nothing else, which is the standard way to
// query mount state (`DATA_DEV=$(mount | awk …)`, row 310193, was hard-denied).
// operandGated attaches a per-entry predicate over the ARGUMENTS; an entry absent
// from that map stays dangerous on every invocation. The full list was audited for
// the same shape and `mount` is the only member gated — see operandGated's doc for
// the per-entry findings, including the candidates deliberately left ungated.
package dangerouscmds

import (
	"fmt"
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("dangerous-commands: read bash command: %w", err)
	}
	for _, pc := range parsed {
		base := filepath.Base(pc.Executable)
		if isDangerous(base, pc.Args) {
			// Reject, not Ask: this package's Decision policy (top of file) is a
			// hard block matching hook-support's DENY, the same non-overridable
			// treatment ceta gives `assume`'s assume-role and config-rules' blocked
			// basenames — every member of `dangerous` is destructive enough (sudo,
			// dd, mkfs, reboot, …) that a user-overridable prompt is the wrong
			// floor. Would soften to Ask only for a SPECIFIC entry shown to have a
			// legitimate query form this rule over-blocks — exactly the shape
			// `mount` already got via operandGated (pg2-2nm54); a denylist member
			// found to fit that shape belongs there, not here.
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "dangerous command blocked: " + base,
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

// isDangerous reports whether an invocation of base with args is dangerous: base
// must be on the denylist (including any `mkfs.<fstype>` filesystem-builder
// variant), and — for the entries in operandGated — its arguments must not reduce
// the invocation to that command's read-only query form.
func isDangerous(base string, args []string) bool {
	if !onDenylist(base) {
		return false
	}
	if gate, ok := operandGated[base]; ok {
		return gate(args)
	}
	return true
}

// onDenylist reports whether base names a denylisted executable, matching the bare
// `mkfs` name and any `mkfs.<fstype>` variant.
func onDenylist(base string) bool {
	if dangerous[base] {
		return true
	}
	return base == "mkfs" || strings.HasPrefix(base, "mkfs.")
}

// operandGated maps a denylisted basename to a predicate over that leaf's ARGUMENTS
// reporting whether the invocation is dangerous. An entry absent from this map is
// dangerous on EVERY invocation — the default stays fail-closed, so adding a
// denylist entry never silently opens a query form.
//
// AUDIT (pg2-2nm54) — every denylist entry checked for the "destructive with
// arguments, harmless query without them" shape. Only `mount` qualifies:
//
//	mount    GATED. Bare `mount` prints the mounted-filesystem list; mount(8) needs
//	         a source and/or a target to act, and the sole operand-less mounting
//	         form is `-a`/`--all`, which the flag allowlist excludes.
//	umount   NOT gated. Always needs a target; bare `umount` is a usage error, so
//	         there is no query form to recover.
//	su       NOT gated. Bare `su` is the MOST dangerous form — it attempts an
//	         interactive root shell. The shape is inverted, not present.
//	reboot / shutdown / halt / poweroff
//	         NOT gated. Bare form is the destructive one (Linux `shutdown` with no
//	         operand schedules a shutdown). Inverted, not present.
//	telnet   NOT gated. Bare `telnet` opens the interactive `telnet>` prompt — a
//	         live session, not a query.
//	dd       NOT gated. Bare `dd` copies stdin to stdout; it performs I/O and
//	         blocks rather than answering a question. No query form.
//	parted   NOT gated. Bare `parted` enters the INTERACTIVE partition editor on
//	         the first block device found. Inverted, not present.
//	sudo / doas / wget / nc / ncat / netcat / sftp / mkfs / mkfs.*
//	         NOT gated. Bare form only prints usage — harmless, but not a query
//	         anyone issues, so gating would loosen the rule for zero benefit. In
//	         the whole 323k-row corpus every one of these appears exclusively with
//	         arguments (`sudo darwin-rebuild …`, `dd if=… of=…`, `nc -z host port`).
//	fdisk    NOT gated, DELIBERATELY ERRED SAFE. `fdisk -l` really is a read-only
//	         partition-table listing on util-linux — the one near-miss. It is left
//	         rejecting because the flag is platform-divergent (BSD/darwin `fdisk`
//	         has no listing `-l`, and bare `fdisk <device>` there is an INTERACTIVE
//	         editor), and mis-reading an operand as a listing flag on an
//	         interactive partition editor is the worst outcome on this list. Should
//	         it ever be wanted, it needs its own bead and its own evidence.
var operandGated = map[string]func(args []string) bool{
	"mount": mountIsDangerous,
}

// mountInfoFlags are `mount` flags that only affect what the LISTING reports, never
// what is mounted: `-l`/`--show-labels` (util-linux, adds labels to the listing),
// `-v`/`--verbose`, and the pure-information `--version`/`--help`. Every other
// mount flag — `-a`, `-o`, `-r`, `-w`, `-B`, `-M`, `-R`, `--bind`, `--target`,
// `--source`, `-L`, `-U`, … — is absent on purpose: some mount, and several supply
// a source or target in FLAG form, which is an operand by any other name. The
// allowlist shape is what makes that fail-safe: an unrecognised (or newly added)
// flag falls through to "dangerous" instead of being assumed informational.
var mountInfoFlags = map[string]bool{
	"-l": true, "--show-labels": true,
	"-v": true, "--verbose": true,
	"-V": true, "--version": true,
	"-h": true, "--help": true,
}

// mountInfoShortLetters are the mountInfoFlags short letters, for the clustered
// form (`mount -lv`). Each is either informational or unrecognised — hence a usage
// error — on BOTH util-linux (`-l` show-labels, `-v` verbose, `-V` version,
// `-h` help) and BSD/darwin (`-v` verbose; `-l`/`-V`/`-h` do not exist).
var mountInfoShortLetters = map[byte]bool{'l': true, 'v': true, 'V': true, 'h': true}

// mountInfoValueFlags are `mount` flags that consume the NEXT argument and still
// leave the invocation a listing. Only the type selector qualifies: with no
// source/target operand, `-t <type>` merely FILTERS the printed list — util-linux
// documents it as listing mode (`mount [-l] [-t type]`), and BSD/darwin mount
// applies the type list as a filter over `getmntinfo` on the zero-operand path.
// It cannot mount, because mount(8) still has nothing to act on; the one
// operand-less mounting form, `-a`, is not on either allowlist.
var mountInfoValueFlags = map[string]bool{
	"-t": true, "--types": true,
}

// mountIsDangerous reports whether a `mount` leaf's arguments take it beyond the
// read-only listing. No arguments at all — the plain listing — is not dangerous;
// beyond that, EVERY argument must be an informational flag. Any positional operand
// (a device, a mountpoint, or an unresolved expansion such as `$TARGET`), anything
// after a `--` end-of-options marker, and any flag not on the allowlist all make it
// dangerous.
func mountIsDangerous(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case mountInfoFlags[a]:
			// Informational, consumes nothing.
		case mountInfoValueFlags[a]:
			// The value belongs to the flag, not to mount's operands. A MISSING value
			// is unclassifiable, so it fails closed.
			if i+1 >= len(args) {
				return true
			}
			i++
		case isMountInfoLongWithValue(a):
			// `--types=nfs`: the inline long-option form of the above.
		case isMountInfoShortCluster(a):
			// `-lv`: clustered informational short flags.
		default:
			// A positional operand, `--`, or any non-allowlisted flag.
			return true
		}
	}
	return false
}

// isMountInfoLongWithValue reports whether a is the `--flag=value` inline form of a
// mountInfoValueFlags long option.
func isMountInfoLongWithValue(a string) bool {
	eq := strings.IndexByte(a, '=')
	if eq <= 0 || !strings.HasPrefix(a, "--") {
		return false
	}
	return mountInfoValueFlags[a[:eq]]
}

// isMountInfoShortCluster reports whether a is a single-dash cluster of two or more
// short flags that are ALL informational (`-lv`, `-vl`). A cluster containing a
// value-consuming letter is rejected: where the value ends up (attached vs. the next
// argument) is getopt-implementation detail, and guessing wrong on an operand is
// exactly the failure this rule exists to prevent.
func isMountInfoShortCluster(a string) bool {
	if len(a) < 3 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		if !mountInfoShortLetters[a[i]] {
			return false
		}
	}
	return true
}
