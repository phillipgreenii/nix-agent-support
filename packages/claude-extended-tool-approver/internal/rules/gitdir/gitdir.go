// Package gitdir guards direct access to a repository's `.git` directory — the
// object store, refs, config and hooks — so git metadata is modified through the
// git porcelain rather than by an agent poking at the files (a hook-support
// parity capability; GitDirectoryEvaluator).
//
// Decision policy, by DIRECTION of the access (pg2-3hk7t):
//
//   - a WRITE (`sed -i`, `>`, `rm`, `mv`/`cp` onto it, `tee`, `chmod`, …) is
//     Rejected. This is the hard security block, matching hook-support's DENY
//     with confidence 1.0 and consistent with ceta's other hard-block rules
//     (`assume` Rejects assume-role; `config-rules` Rejects blocked basenames).
//   - a READ (`ls`, `cat`, `grep`, `readlink`, `[ -e ]`, `head`, `wc`, `stat`,
//     `diff`) is a DECISIVE Ask: never auto-approved, still surfaced to the user
//     on every access, but USER-OVERRIDABLE.
//   - an access whose direction cannot be determined is treated as a WRITE.
//
// Why reads are Ask and not Reject. A non-overridable deny on a read-only
// inspection is the failure mode most likely to get the whole guard disabled,
// which is strictly worse security than a prompt — the same reasoning that
// demoted `ENV` from Reject to Ask in the env-var rule (see
// envvars.injectorAskVars). Reads of `.git/hooks/*` and `.git/worktrees/*/rebase-merge/*`
// are ordinary diagnostic traffic that no git porcelain command exposes, and the
// old blanket Reject also emitted a reason claiming the user was MODIFYING
// metadata when they were only listing it. Ask keeps the rule decisive, so a read
// can still never be silently approved by path-safety or safe-commands.
//
// Why reads are Ask and not Allow: an Allow would auto-approve silently and
// short-circuit every later rule for that leaf (engine.Evaluate is
// first-match-wins and this rule runs in the early band).
//
// SYNTACTIC ROLE, not bare text. A git-metadata path token is a violation only
// when it is a path the command actually OPERATES ON. The rule therefore parses
// the command and inspects operands by role, following
// safecmds.argsHaveDynamicExpansion's precedent of judging an argument's role
// rather than its text. It deliberately does NOT match:
//
//   - PROSE that merely mentions a `.git/…` path — a heredoc body, a commit
//     message, a bead title, a notification payload. A path operand contains no
//     whitespace, so prose can never satisfy isGitMetadataPath.
//   - an EXCLUSION: a `!`-prefixed glob (`rg -g '!**/.git/**'`), the PATTERN
//     operand of the grep/sed/awk family (`grep -v "/.git/"` filters it OUT), or
//     a `find … -path X -prune` operand. Such a command provably does not touch
//     git metadata.
//   - an argument to `git` ITSELF (notably `git config -f …/.git/config`). git is
//     the sanctioned porcelain this rule exists to funnel access through, so
//     rejecting a git command contradicted the rule's own reason string; the
//     dedicated `git` rule judges those.
package gitdir

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// maxScopeDepth bounds the substitution recursion of scopeLeaves. Deeply nested
// substitutions are not a real shape, and the bound makes a self-similar body
// (whose inner text re-parses to the same leaf) terminate.
const maxScopeDepth = 8

// direction is the access direction of a matched git-metadata path. The zero
// value is dirRead; dirWrite is strictly more restrictive, so `worse` folds by
// numeric order exactly as hookio.MostRestrictive does for decisions.
type direction int

const (
	dirRead direction = iota
	dirWrite
)

func worse(a, b direction) direction {
	if b > a {
		return b
	}
	return a
}

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "git-directory" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	switch input.ToolName {
	case "Bash":
		cmd, err := input.BashCommand()
		if err != nil {
			break
		}
		// The engine hands this rule ONE leaf of a compound at a time, but a leaf
		// that merely BINDS a path (`f="$r/.git/info/exclude"`) is identical whether
		// the sibling consuming `"$f"` reads or writes it. RootExpression is the
		// expression the leaf came from and is the only place that fact exists; it
		// is empty for a direct (non-engine) call, where the leaf IS the whole
		// command.
		scope := input.RootExpression
		if scope == "" {
			scope = cmd
		}
		if dir, matched := bashAccess(cmd, scope); matched {
			return r.verdict(dir)
		}
	case "Read":
		if path, err := input.FilePath(); err == nil && isGitMetadataPath(path) {
			return r.verdict(dirRead)
		}
	case "Write", "Edit", "MultiEdit", "Delete":
		if path, err := input.FilePath(); err == nil && isGitMetadataPath(path) {
			return r.verdict(dirWrite)
		}
	case "Glob", "Grep":
		if path, err := input.SearchPath(); err == nil && isGitMetadataPath(path) {
			return r.verdict(dirRead)
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) verdict(d direction) hookio.RuleResult {
	if d == dirWrite {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "refusing to write git metadata under .git/ directly — modify it through git commands only",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "reading git metadata under .git/ requires confirmation",
		Module:   r.Name(),
	}
}

// bashAccess reports whether leafText operates on a path inside a `.git`
// directory, and in which direction. scopeText is the whole expression leafText
// was split out of, used to resolve the direction of a path bound to a variable.
func bashAccess(leafText, scopeText string) (direction, bool) {
	dir, matched := dirRead, false
	note := func(d direction) {
		dir = worse(dir, d)
		matched = true
	}
	for _, pc := range cmdparse.Parse(leafText) {
		// `git` is the sanctioned porcelain this rule funnels access through, so its
		// own ARGUMENTS are never a violation and the dedicated `git` rule judges
		// them. Scoped to the operands only: a redirection or an assignment on the
		// same leaf (`git status > .git/foo`) is NOT a git-mediated access and is
		// still checked below.
		gitPorcelain := false
		if base, _ := effectiveExec(pc); base == "git" {
			gitPorcelain = true
		}
		if !gitPorcelain {
			for _, tok := range pathOperands(pc) {
				if isGitMetadataPath(tok) {
					note(commandDirection(pc, func(s string) bool { return s == tok }))
				}
			}
		}
		for _, rd := range pc.Redirections {
			if !isGitMetadataPath(rd.Path) {
				continue
			}
			if rd.Kind == hookio.RedirectStdin {
				note(dirRead)
			} else {
				note(dirWrite)
			}
		}
		// An assignment BINDS a path; it accesses nothing itself. Its direction is
		// whatever the expression later does with the variable.
		for _, ev := range pc.EnvVars {
			if isGitMetadataPath(ev.Value) {
				note(bindingDirection(ev.Name, scopeText))
			}
		}
	}
	return dir, matched
}

// bindingDirection resolves the direction of a git-metadata path bound to the
// variable name by folding the direction of every command in the expression that
// CONSUMES it. A binding nothing consumes here is undecidable, so it fails safe
// to a write — that is what keeps a real `f=…/.git/info/exclude` + `sed -i "$f"`
// rejected while `RM=…/rebase-merge` + `ls "$RM"` only asks.
//
// RESIDUAL ASYMMETRY, deliberate: `git` is not in readCmds, so a path consumed
// only through git (`S=…/.git/config; git config --file "$S" --get x`) folds to a
// write and Rejects, whereas the same access spelled as a DIRECT git operand
// (`git config -f …/.git/config --get x`) is not a match at all. Both spellings
// Rejected before this change, so nothing regresses, and the conservative side is
// the right one to err on here: corpus row 237336 binds `.git/config` and then
// `git config --unset-all`s it, which is a genuine write no read/write table for
// bare `git` would catch without modelling every subcommand — the `git` rule's job.
func bindingDirection(name, scope string) direction {
	dir, used := dirRead, false
	for _, pc := range scopeLeaves(scope, 0) {
		if !leafReferencesVar(pc, name) {
			continue
		}
		used = true
		dir = worse(dir, commandDirection(pc, func(s string) bool { return referencesVar(s, name) }))
		if dir == dirWrite {
			break
		}
	}
	if !used {
		return dirWrite
	}
	return dir
}

// scopeLeaves flattens an expression into every command it can run: the top-level
// leaves plus, recursively, the leaves of each command/process substitution body.
// The recursion matters because a variable is most often consumed INSIDE a
// substitution (`echo "msgnum: $(cat "$RM/msgnum")"`), and cmdparse.Parse stops at
// the substitution boundary — the substitution's text stays glued into the outer
// leaf's token.
func scopeLeaves(expr string, depth int) []cmdparse.ParsedCommand {
	if depth > maxScopeDepth {
		return nil
	}
	var out []cmdparse.ParsedCommand
	for _, pc := range cmdparse.Parse(expr) {
		out = append(out, pc)
		for _, sub := range cmdparse.EnumerateSubstitutions(pc.Raw) {
			out = append(out, scopeLeaves(sub.Body, depth+1)...)
		}
	}
	return out
}

// leafReferencesVar reports whether the leaf's OWN operands reference $name.
func leafReferencesVar(pc cmdparse.ParsedCommand, name string) bool {
	if referencesVar(pc.Executable, name) {
		return true
	}
	for _, a := range pc.Args {
		if referencesVar(a, name) {
			return true
		}
	}
	for _, rd := range pc.Redirections {
		if referencesVar(rd.Path, name) {
			return true
		}
	}
	return false
}

// referencesVar reports whether s contains a $NAME / ${NAME…} reference OUTSIDE
// any nested substitution. Text inside a substitution belongs to the INNER
// command — which scopeLeaves surfaces as its own leaf — so counting it here
// would attribute the inner command's direction to the outer one: the `echo` of
// `echo "$(cat "$RM/x")"` does not itself touch $RM, the `cat` does.
func referencesVar(s, name string) bool {
	return containsVarRef(stripSubstitutions(s), name)
}

// stripSubstitutions removes every command/process substitution BODY from s,
// reusing the shared quote-aware enumerator rather than adding another scanner.
// EnumerateSubstitutions returns only TOP-LEVEL bodies, so removing those removes
// any nesting with them.
func stripSubstitutions(s string) string {
	for _, sub := range cmdparse.EnumerateSubstitutions(s) {
		if sub.Body != "" {
			s = strings.Replace(s, sub.Body, "", 1)
		}
	}
	return s
}

func containsVarRef(s, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		rest := s[i+1:]
		rest = strings.TrimPrefix(rest, "{")
		if !strings.HasPrefix(rest, name) {
			continue
		}
		// A reference ends at the name: $RM matches `$RM` and `${RM:-x}` but not
		// `$RMDIR`.
		if tail := rest[len(name):]; tail == "" || !isNameByte(tail[0]) {
			return true
		}
	}
	return false
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// isGitMetadataPath reports whether s, taken as a WHOLE path operand, addresses
// something inside a `.git` directory.
//
// Two properties do the work. The match is ANCHORED on a `/`-separated COMPONENT
// equal to `.git`, so `.gitignore`, `.git.bak` and the bare `git` executable are
// not matches. And a path operand contains NO WHITESPACE, which is what keeps
// prose out: `"infra-block: … .git/index is 0 bytes …"` is one token, not a path,
// where the former substring match (`strings.Contains(s, " .git/")` over the raw
// command) rejected it.
func isGitMetadataPath(s string) bool {
	s = unquoteOperand(s)
	if s == "" || strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	for _, comp := range strings.Split(s, "/") {
		if comp == ".git" {
			return true
		}
	}
	return false
}

// unquoteOperand strips ONE pair of wrapping quotes. cmdparse already unquotes
// arg tokens, but it keeps an env-assignment VALUE verbatim, so
// `f="$r/.git/info/exclude"` arrives with its quotes attached.
func unquoteOperand(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// shellKeywords are compound-statement keywords cmdparse leaves as a segment's
// "executable" (`if [ -e "$h" ]` parses to Executable=="if"). The real command is
// the next token, so direction classification must step past them or every
// `if [ -e … ]` read would be an unknown command and fail safe to a write.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true,
	"while": true, "until": true, "do": true, "!": true, "time": true,
}

// effectiveExec returns the leaf's real command basename and the args that follow
// it, stepping past any leading shell keywords.
func effectiveExec(pc cmdparse.ParsedCommand) (string, []string) {
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

// readCmds never modify their path operands.
var readCmds = map[string]bool{
	"ls": true, "cat": true, "bat": true, "head": true, "tail": true, "wc": true,
	"stat": true, "file": true, "diff": true, "cmp": true, "readlink": true,
	"realpath": true, "basename": true, "dirname": true, "du": true,
	"find": true, "fd": true, "tree": true,
	"test": true, "[": true, "[[": true, "echo": true, "printf": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"awk": true, "gawk": true, "nawk": true, "cut": true, "tr": true, "sort": true,
	"uniq": true, "od": true, "xxd": true, "hexdump": true, "strings": true,
	"less": true, "more": true, "jq": true, "yq": true, "column": true,
	"md5sum": true, "shasum": true, "sha1sum": true, "sha256sum": true, "cksum": true,
}

// copyLikeCmds take a DESTINATION as their last operand and only READ their
// source, so their direction depends on WHICH operand the git-metadata path is:
// naming it as a source reads it, naming it as the destination writes it.
//
// `mv` is deliberately NOT a member — see moveCmds. Do not add it back.
var copyLikeCmds = map[string]bool{
	"cp": true, "ln": true, "install": true,
}

// moveCmds are destructive on EVERY operand they name, source included: a rename
// REMOVES the path at the source, so `mv .git/HEAD /tmp/x` destroys git metadata
// just as surely as `mv /tmp/x .git/HEAD` overwrites it.
//
// This is the one place the copyLikeCmds geometry does not hold, and getting it
// wrong is expensive: grouping `mv` with cp/ln/install classified a rename of the
// source as a READ and downgraded a destructive operation from a hard block to a
// user-overridable prompt. `rename` (util-linux / perl) is the same shape — it
// mutates the files it is given in place. Both fall through to the default write
// verdict; the set exists so the intent is explicit at the decision point rather
// than an accident of allowlist membership.
var moveCmds = map[string]bool{
	"mv": true, "rename": true,
}

// destFlagCmds carry flags that INVERT copyLikeCmds' operand geometry, so the
// last operand is no longer the destination and reading direction off it would
// pick the wrong end:
//
//   - `-t` / `--target-directory`: the destination comes from the FLAG, so every
//     positional is a source (`install -t .git/hooks evil` writes into .git);
//   - `-d` / `--directory`: install CREATES each operand as a directory, so they
//     are all destinations wherever they sit.
//
// Any match under one of these is treated as a write. That over-blocks the
// read-only `cp -t /tmp .git/config`, which is the correct side to err on.
var destFlagCmds = map[string]bool{
	"-t": true, "--target-directory": true, "-d": true, "--directory": true,
}

// mutatingFlags list, per read-allowlisted command, the flags that turn it into a
// WRITER — the second shape a read allowlist gets wrong. Each of these commands is
// genuinely read-only in its bare form, which is why it belongs on readCmds, but a
// single flag makes it destructive:
//
//   - `find -delete` removes what it matches, and `-exec`/`-execdir`/`-ok` run an
//     arbitrary command over it (`find .git -exec rm {} \;`); the `-f*print*`
//     family writes its listing to a named file.
//   - `sort -o FILE` writes to FILE, which may be the git-metadata path itself.
//   - `yq -i` edits in place. (`jq` has NO in-place flag — it is stdout-only, so it
//     is deliberately absent here.)
//   - `tree -o FILE` redirects its listing into FILE.
//
// A flag match flips the WHOLE command to write rather than just the flag's own
// operand. That deliberately over-blocks a read-only `find .git -exec grep …`: an
// `-exec` payload is opaque (`-exec sh -c '…'` doubly so), and the rule's policy is
// that a direction which cannot be determined is a write.
//
// Measured cost of the `-exec` entry: exactly ONE corpus row, 305265, a read-only
// `find <gitdirs> -name index.lock -exec sh -c 'echo … stat …'` lock scan. That row
// Rejects on unpatched main too, so this entry RESTORES the status quo for it
// rather than introducing a new deny — worth knowing before anyone "fixes" it by
// dropping `-exec`, which would silently downgrade `find .git -exec rm {} \;` to a
// user-overridable prompt.
var mutatingFlags = map[string]map[string]bool{
	"find": {
		"-delete": true, "-exec": true, "-execdir": true, "-ok": true,
		"-fprint": true, "-fprint0": true, "-fls": true, "-fprintf": true,
	},
	"sort": {"-o": true, "--output": true},
	"yq":   {"-i": true, "--inplace": true, "--in-place": true},
	"tree": {"-o": true},
}

// ddPathPrefixes are the `key=value` operand prefixes dd uses for its input and
// output files. dd is the one common command that names paths this way, and the
// whole token (`of=.git/config`) has no `.git` path COMPONENT, so a component walk
// over it silently misses the write entirely. Both directions are surfaced and dd
// is not read-allowlisted, so either resolves to a write.
var ddPathPrefixes = []string{"if=", "of="}

// patternFirstCmds take a PATTERN or SCRIPT as their first bare operand, not a
// path, and carry value-consuming flags whose values are equally not paths.
// `grep -v "/.git/"` filters that text OUT of its input and touches no git
// metadata at all. Operand extraction for these defers to the shared
// cmdparse.SkipGrepPattern, which already models both.
var patternFirstCmds = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"sed": true, "gsed": true, "awk": true, "gawk": true, "nawk": true,
}

// findPathPredicates are the find predicates whose operand is a PATTERN matched
// against the walk, not a path find descends into. find never opens these, so they
// are excluded unconditionally — `-path ./.git -prune` and `! -path './.git/*'`
// are both exclusions, and requiring `-prune` would miss the negated spelling.
var findPathPredicates = map[string]bool{
	"-path": true, "-wholename": true, "-ipath": true, "-iwholename": true,
	"-name": true, "-iname": true, "-regex": true, "-iregex": true,
}

// excludeValueFlags name flags whose FOLLOWING token is an ignore/exclude PATTERN
// rather than a path the command opens: `tree -I '.git'`, `fd -E .git`,
// `tar --exclude .git`. Two corpus rows (101602, 124447) were `tree -L 3 -I '.git'`
// — a read-only listing that EXCLUDES git metadata, which an earlier cut of this
// rule turned into a brand-new hard deny by reading `-I`'s value as an operand.
//
// Naming the flags individually is deliberate. The tempting general rule — "the
// token after any flag is not an operand" — would swallow the operand of
// `rm -rf .git`, so it is not available. These entries are consulted only for
// commands OUTSIDE patternFirstCmds, whose own value-flag vocabulary
// cmdparse.SkipGrepPattern already owns (grep's `-E` is boolean, rg's is not).
var excludeValueFlags = map[string]bool{
	"-I": true, "--ignore": true, "-E": true,
	"--exclude": true, "--exclude-dir": true, "--exclude-from": true,
	"-X": true, "--ignore-file": true,
}

// commandDirection classifies how pc accesses the operand(s) selected by target.
// Anything not positively known to be read-only is a write (fail safe).
func commandDirection(pc cmdparse.ParsedCommand, target func(string) bool) direction {
	// A redirection ONTO the target is a write whatever the command is: the writer
	// is the shell, not the executable, so `echo x > "$f"` must not inherit echo's
	// read-only classification.
	for _, rd := range pc.Redirections {
		if rd.Kind != hookio.RedirectStdin && target(rd.Path) {
			return dirWrite
		}
	}
	base, args := effectiveExec(pc)
	switch {
	case moveCmds[base]:
		// Destructive on the source as well as the destination — checked BEFORE
		// copyLikeCmds so no operand of a rename is ever read as a mere source.
		return dirWrite
	case base == "sed" || base == "gsed":
		// Only `-i` makes sed a writer; without it sed streams to stdout.
		if hasInPlaceFlag(args) {
			return dirWrite
		}
		return dirRead
	case copyLikeCmds[base]:
		// A destination-bearing flag inverts the geometry, so the last operand is
		// no longer the destination and must not be read as one.
		if hasAnyFlag(args, destFlagCmds) {
			return dirWrite
		}
		if dest, ok := lastOperand(args); ok && target(dest) {
			return dirWrite
		}
		return dirRead
	case readCmds[base]:
		if hasAnyFlag(args, mutatingFlags[base]) {
			return dirWrite
		}
		return dirRead
	}
	return dirWrite
}

// hasAnyFlag reports whether any arg is one of the given flags, matching both the
// separate (`-o FILE`) and glued (`-o=FILE`, `--output=FILE`) spellings so a
// mutating flag cannot hide behind an `=`.
func hasAnyFlag(args []string, flags map[string]bool) bool {
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

// hasInPlaceFlag reports whether args carry sed's in-place flag in any spelling:
// `-i`, `-i.bak`, bundled `-ri`, or `--in-place`. The bundled scan stops at `e`
// or `f`, which consume the rest of the token as a script/filename value.
func hasInPlaceFlag(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			if strings.HasPrefix(a, "--in-place") {
				return true
			}
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		for i := 1; i < len(a); i++ {
			if a[i] == 'i' {
				return true
			}
			if a[i] == 'e' || a[i] == 'f' {
				break
			}
		}
	}
	return false
}

// lastOperand returns the final non-flag arg — the destination for copyLikeCmds.
func lastOperand(args []string) (string, bool) {
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			return args[i], true
		}
	}
	return "", false
}

// pathOperands returns the args of pc that are candidate PATHS — the operands the
// command actually acts on — with flags and every EXCLUSION role removed.
func pathOperands(pc cmdparse.ParsedCommand) []string {
	base, args := effectiveExec(pc)
	out := make([]string, 0, len(args)+1)
	if pc.Executable != "" {
		out = append(out, pc.Executable)
	}
	if patternFirstCmds[base] {
		// Reuse the shared grep/rg operand extractor (cmdparse.SkipGrepPattern,
		// relocated to cmdparse precisely so sibling rules could share it): it drops
		// the positional PATTERN and every value-consuming flag's value, which
		// covers `grep -v "/.git/"`, `grep --exclude-dir .git` and `rg -g '!…'` in
		// one place. The rg vocabulary is honored only for rg, since -r/-E/-T are
		// boolean in grep.
		vocab := "grep"
		if base == "rg" {
			vocab = "rg"
		}
		return append(out, cmdparse.SkipGrepPattern(vocab, args)...)
	}
	isFind := base == "find"
	isDD := base == "dd"
	for i, a := range args {
		if isDD {
			// dd names its files as `if=PATH` / `of=PATH`; the whole token carries no
			// `.git` path component, so the VALUE half must be surfaced on its own.
			for _, p := range ddPathPrefixes {
				if v, ok := strings.CutPrefix(a, p); ok {
					out = append(out, v)
				}
			}
		}
		switch {
		case strings.HasPrefix(a, "-"):
			continue // a flag, not an operand
		case strings.HasPrefix(a, "!"):
			// A NEGATED glob (`fd -E '!x'`, and find's own `!` token) names what must
			// NOT be touched.
			continue
		case i > 0 && excludeValueFlags[args[i-1]]:
			continue // an ignore/exclude PATTERN, e.g. `tree -I '.git'`
		case isFind && i > 0 && findPathPredicates[args[i-1]]:
			continue // a walk PATTERN, e.g. `find . -path ./.git -prune`
		}
		out = append(out, a)
	}
	return out
}
