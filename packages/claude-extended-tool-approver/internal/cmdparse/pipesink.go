package cmdparse

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// This file holds the SINK CLASSIFICATION half of the pipeline relation: given a
// pipeline stage, does it CONSUME what it receives without persisting it, or
// might it write the bytes somewhere they are kept?
//
// It is the natural companion of DownstreamStages, which answers the other half
// ("which stages receive this stage's stdout"). The two are never used apart —
// every caller is the same loop:
//
//	for _, stage := range cmdparse.DownstreamStages(leaves, leafRaw) {
//	    if cmdparse.StageWritesInput(stage) { … }
//	}
//
// which is why PipedToWriter below packages it, and why both halves live in the
// package that OWNS the relation rather than in whichever rule needed it first.
//
// WHY IT IS HERE AND NOT IN A RULE (tc-yk2z). It was written in
// internal/rules/gitdir for tc-vul7, where it answered "did this read of `.git/`
// get piped into something that keeps a copy". The ssh rule needs the same
// answer about an ssh REMOTE command's pipeline, and a rule must not import
// another rule's package — so the choice was to duplicate the tables or relocate
// them. This is exactly the relocation cmdparse.SkipGrepPattern got (see
// argflags.go's header) and that hookio.IsSafeRedirectTarget got, for the same
// reason, and it is the same MECHANISM/POLICY split tc-vul7 itself applied when
// it put the pipeline RELATION here and left gitdir's copy-out POLICY in gitdir:
//
//   - MECHANISM (here): "tee persists its stdin; grep does not" is a fact about
//     the COMMAND. It is true regardless of which guard is asking.
//   - POLICY (in each rule): what a writing sink MEANS. gitdir turns it into a
//     dirCopyOut and Asks; ssh takes it as proof the remote command is not
//     read-only and Asks with its own reason. Neither policy moved.
//
// The per-caller half of the mechanism is the FILTER SET: StageWritesInputWith
// takes one, so a rule with its own vocabulary is not forced onto the default.
// Both current callers want the default, so both call StageWritesInput.

// PipeFilterCmds are pipeline stages that CONSUME what they receive without
// persisting it: they transform their stdin onto their own stdout and open no
// file for writing. Everything NOT listed is treated as a possible WRITER, which
// is the fail-closed direction tc-080p settled (an allowlist of the known-safe,
// never a denylist of the known-dangerous) and the direction tc-403c already
// applies to an undeterminable access.
//
// It is a SEPARATE set from any rule's read-only command list even though they
// overlap heavily, because they answer different questions. A read-only list asks
// "does this command modify its PATH OPERANDS" — `tee`, `dd` and `install` are
// absent from such a list for that reason but so are `sed`, `tac` and `base64`,
// which are perfectly good filters. Reusing gitdir's readCmds here would have
// made `cat .git/config | sed 's/x/y/'` prompt.
//
// `xargs`, `sh`, `bash` and `python` are deliberately absent: each runs an
// arbitrary command over what the pipeline carries, so the sink is whatever that
// command is. `tee` is the shape this exists to catch and MUST NOT be added — it
// is also the one entry the ssh rule used to special-case by name (tc-yk2z), a
// one-entry denylist that could only ever catch the sink somebody had thought of.
//
// MEASURED RESIDUE, accepted: `awk` stays a filter although its program text can
// itself redirect (`awk '{print > "/tmp/x"}'`). Detecting that means reading the
// awk program, and the cheap proxy — a `>` anywhere in the script — also fires on
// every `awk '$1 > 5'` comparison. The same is true of the `-e`/`-f` script of
// `sed`, and of `jq`'s `output` builtins.
var PipeFilterCmds = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"head": true, "tail": true, "wc": true, "cut": true, "tr": true, "sort": true,
	"uniq": true, "column": true, "jq": true, "yq": true, "nl": true, "fold": true,
	"awk": true, "gawk": true, "nawk": true, "sed": true, "gsed": true,
	"cat": true, "bat": true, "tac": true, "rev": true, "less": true, "more": true,
	"od": true, "xxd": true, "hexdump": true, "strings": true, "base64": true,
	"md5sum": true, "shasum": true, "sha1sum": true, "sha256sum": true, "cksum": true,
	"echo": true, "printf": true, "true": true, "false": true,
}

// MutatingFlags list, per otherwise-read-only command, the flags that turn it
// into a WRITER — the second shape a read allowlist gets wrong. Each of these
// commands is genuinely read-only in its bare form, which is why it appears on
// read/filter allowlists at all, but a single flag makes it destructive:
//
//   - `find -delete` removes what it matches, and `-exec`/`-execdir`/`-ok` run an
//     arbitrary command over it (`find .git -exec rm {} \;`); the `-f*print*`
//     family writes its listing to a named file.
//   - `sort -o FILE` writes to FILE, which may be the guarded path itself.
//   - `yq -i` edits in place; `yq -s`/`--split-exp`/`--split-exp-file` writes ONE
//     NEW FILE PER RESULT, named from the expression (pg2-1wt3b — verified against
//     yq's own `--help`, 2026-08-18, yq v4.34.2: "print each result (or doc) into
//     a file named (exp)"). This entry is the single source of truth for yq's
//     write vocabulary: `internal/rules/safecmds`' isYqInPlace consumes it
//     directly (via HasAnyFlag) rather than re-listing the flags, and
//     `internal/cmdparse`'s own substitution seam (parser.go's
//     substitutionWriteFlags) OR's in a supplement only for whatever gap remains
//     here — which, as of this widening, is none for yq. (`jq` has NO in-place
//     flag — it is stdout-only, so it is deliberately absent here.)
//   - `tree -o FILE` redirects its listing into FILE.
//
// A flag match flips the WHOLE command to write rather than just the flag's own
// operand. That deliberately over-blocks a read-only `find .git -exec grep …`: an
// `-exec` payload is opaque (`-exec sh -c '…'` doubly so), and the settled policy
// is that a direction which cannot be determined is a write.
//
// Measured cost of the `-exec` entry: exactly ONE corpus row, 305265, a read-only
// `find <gitdirs> -name index.lock -exec sh -c 'echo … stat …'` lock scan. That row
// Rejects on unpatched main too, so this entry RESTORES the status quo for it
// rather than introducing a new deny — worth knowing before anyone "fixes" it by
// dropping `-exec`, which would silently downgrade `find .git -exec rm {} \;` to a
// user-overridable prompt.
var MutatingFlags = map[string]map[string]bool{
	"find": {
		"-delete": true, "-exec": true, "-execdir": true, "-ok": true,
		"-fprint": true, "-fprint0": true, "-fls": true, "-fprintf": true,
	},
	"sort": {"-o": true, "--output": true},
	"yq": {
		"-i": true, "--inplace": true, "--in-place": true,
		"-s": true, "--split-exp": true, "--split-exp-file": true,
	},
	"tree": {"-o": true},
}

// shellKeywords are compound-statement keywords Parse leaves as a segment's
// "executable" (`if [ -e "$h" ]` parses to Executable=="if"). The real command is
// the next token, so any classification keyed on the executable must step past
// them or every `if [ -e … ]` read would be an unknown command.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true,
	"while": true, "until": true, "do": true, "!": true, "time": true,
}

// EffectiveExec returns the leaf's real command basename and the args that follow
// it, stepping past any leading shell keywords.
func EffectiveExec(pc ParsedCommand) (string, []string) {
	base := baseName(pc.Executable)
	args := pc.Args
	for shellKeywords[base] && len(args) > 0 {
		base = baseName(args[0])
		args = args[1:]
	}
	return base, args
}

func baseName(s string) string {
	if s == "" {
		return ""
	}
	return filepath.Base(s)
}

// HasAnyFlag reports whether any arg is one of the given flags, matching both the
// separate (`-o FILE`) and glued (`-o=FILE`, `--output=FILE`) spellings so a
// mutating flag cannot hide behind an `=`.
func HasAnyFlag(args []string, flags map[string]bool) bool {
	if len(flags) == 0 {
		return false
	}
	for _, a := range args {
		if flags[a] {
			return true
		}
		if eq := strings.IndexByte(a, '='); eq > 0 && flags[a[:eq]] {
			return true
		}
	}
	return false
}

// CapturesStdout reports whether pc's own redirections land its STDOUT somewhere
// that keeps the bytes. Only the STDOUT-bearing kinds count: `2>/dev/null`
// discards diagnostics and captures none of the payload, and a target that
// captures nothing — /dev/null, the tty, an inherited fd — is likewise not a
// capture (hookio.IsSafeRedirectTarget).
func CapturesStdout(pc ParsedCommand) bool {
	for _, rd := range pc.Redirections {
		if rd.Kind != hookio.RedirectStdout && rd.Kind != hookio.RedirectAll {
			continue
		}
		if !hookio.IsSafeRedirectTarget(rd.Path) {
			return true
		}
	}
	return false
}

// StageWritesInput reports whether a downstream pipeline stage might PERSIST what
// it receives, judged against the default PipeFilterCmds vocabulary. Unknown is a
// writer.
func StageWritesInput(pc ParsedCommand) bool {
	return StageWritesInputWith(PipeFilterCmds, pc)
}

// StageWritesInputWith is StageWritesInput against a caller-supplied filter set,
// for a rule whose consume-without-persist vocabulary is not the default one. An
// EMPTY set makes every command a writer, which is the fail-closed direction and
// therefore a safe (if useless) argument.
func StageWritesInputWith(filters map[string]bool, pc ParsedCommand) bool {
	// A capturing stdout redirection persists the whole pipeline's payload whatever
	// the stage is: `cat .git/config | grep url > /tmp/x` is a copy-out through a
	// stage that is otherwise a pure filter.
	if CapturesStdout(pc) {
		return true
	}
	base, args := EffectiveExec(pc)
	if base == "" {
		// A command-less stage is a bare redirection segment; CapturesStdout already
		// ruled on it, and anything left captures nothing.
		return false
	}
	if !filters[base] {
		return true
	}
	// A filter with a flag that makes it write a file is a writer after all —
	// `sort -o FILE`, `yq -i`, `tree -o`.
	return HasAnyFlag(args, MutatingFlags[base])
}

// PipedToWriter reports whether the stage whose raw text is leafRaw pipes into any
// downstream stage that might PERSIST what it receives. It is the packaged form of
// the DownstreamStages + StageWritesInput loop every caller writes.
//
// `leaves` MUST be a single Parse call's output — see DownstreamStages, which owns
// that constraint. A leafRaw matching no stage yields false, the same answer as
// "not in a pipeline".
func PipedToWriter(leaves []ParsedCommand, leafRaw string) bool {
	for _, stage := range DownstreamStages(leaves, leafRaw) {
		if StageWritesInput(stage) {
			return true
		}
	}
	return false
}
