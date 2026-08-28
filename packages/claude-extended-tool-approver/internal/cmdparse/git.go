package cmdparse

import "strings"

// GIT FLAG / REFSPEC MATCHERS (pg2-si0bp)
//
// HasShortFlag, HasLongFlag, FirstOperand, Operands and ClassifyPushRefspecs
// below are the token-level primitive the git rule needs to see the flag and refspec
// forms git ITSELF accepts. The rule module currently locates flags and operands
// by exact token equality, so a clustered short (`-fu`), an `=`-glued long
// (`--force-with-lease=other`), and a force/delete expressed purely in the
// refspec (`+main`, `:main`) are all invisible to it. HasLongFlagPrefix was added
// beside them by pg2-os1kq for the remaining spelling axis, git's unique-prefix
// ABBREVIATIONS (`--har` IS `--hard`); see its doc for the fail-safe over-matching
// property and the use restriction that comes with it.
//
// SCANNER CLASS — these helpers are TOKEN-LEVEL and POST-unquote: they consume
// ParsedCommand.Args, i.e. the arguments AFTER the parser has lowered the raw
// command text to tokens and applied unquote. They never look at raw command
// text, never make a quoting decision, and hold no quote state, so they are NOT
// members of the "multiple independent text scanners with inconsistent
// quote-awareness" class that bead pg2-1vme1 reviews. They also sit DOWNSTREAM of
// the ADR 0039 parser-migration seam: pg2-fez3d / pg2-zeqa5 delete raw-text
// scanners that PRODUCE ParsedCommand, and ParsedCommand.Args survives that
// chain unchanged, so these helpers need no migration when those land.
//
// SCOPE — this file adds the primitive ONLY. No verdict changes here; each
// consumer (pg2-bohpm force-push, pg2-8imjo `git remote`, pg2-szadj `git config`)
// wires it into internal/rules/git in its own reviewable diff.

// HasShortFlag reports whether short flag byte f is present in args, either bare
// (`-f`) or as a member of a cluster (`-fd`, `-fdx`, `-xdf`, `-fu`).
//
// COVERS: bare shorts; clustered shorts in any position within the cluster; a
// `--` end-of-options terminator, at which the scan STOPS (so `git push -- -f`
// reports false — that `-f` is an operand, not a flag).
//
// DELIBERATELY DOES NOT COVER: long flags. A token beginning `--` is never
// treated as a cluster, so HasShortFlag(args, 'f') is false for `--force` and
// for `--f`. A lone `-` is an operand (see FirstOperand) and is never scanned.
// Nor does it know flag ARITY: a short flag with a GLUED value (`-Cpath`,
// `-mmsg`) is scanned as if every byte after the dash were a flag letter, and an
// `=`-glued short (`-f=x`) likewise contributes `=` and `x`. Callers MUST NOT
// ask about a byte that could plausibly occur inside such a value; the git flags
// this exists for (-f, -d, -u, -D) are boolean and carry no glued value.
//
// The cluster test is a deliberate byte loop over the token, which is the only
// way to see a clustered flag: bead pg2-x9452's Guard 2 is a mechanical
// hand-rolled-scanner detector known to false-positive on character loops, and
// this is one of those false positives — the loop indexes bytes of a single
// already-tokenized, already-unquoted argument and makes no lexical decision.
func HasShortFlag(args []string, f byte) bool {
	for _, a := range args {
		if a == "--" {
			return false // end of options; the rest are operands
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue // operand, lone "-", or a long flag
		}
		for i := 1; i < len(a); i++ {
			if a[i] == f {
				return true
			}
		}
	}
	return false
}

// HasLongFlag reports whether long flag name is present in args, and returns its
// value when the `=`-glued form was used. name MAY be written with or without its
// leading `--` ("force-with-lease" and "--force-with-lease" are equivalent).
//
// COVERS: the bare form `--name`, which returns ("", true); and the glued form
// `--name=value`, which returns (value, true). Scanning STOPS at a `--`
// end-of-options terminator, which returns ("", false).
//
// DELIBERATELY DOES NOT COVER: the SEPARATED value form `--name value` — the
// value is not returned, because knowing whether a given long flag consumes the
// next token requires a per-command flag-arity table that this primitive does not
// build. It reports ok for that form (the flag IS present) with an empty value,
// which is indistinguishable from both the bare form and an explicitly empty
// `--name=`. Nor does it cover git's unique-prefix ABBREVIATIONS (git accepts
// `--har` for `--hard`) or `--no-` negations. A caller that must catch the
// abbreviations MUST NOT enumerate spellings by hand — that is how one gets
// missed, which is the defect pg2-os1kq closed. Use HasLongFlagPrefix for a
// boolean dangerous-flag test, or a MEASURED per-subcommand minimum where the
// match length or the flag's value is load-bearing.
func HasLongFlag(args []string, name string) (value string, ok bool) {
	name = strings.TrimPrefix(name, "--")
	if name == "" {
		return "", false
	}
	want := "--" + name
	for _, a := range args {
		if a == "--" {
			return "", false // end of options; the rest are operands
		}
		if a == want {
			return "", true
		}
		if strings.HasPrefix(a, want+"=") {
			return a[len(want)+1:], true
		}
	}
	return "", false
}

// HasLongFlagPrefix reports whether args carries long flag canonical in ANY
// spelling git's parse-options would accept for it — the full name, or `--`
// followed by any NON-EMPTY PREFIX of it. For canonical "hard" each of `--hard`,
// `--har`, `--ha` and `--h` reports true. canonical MAY be written with or
// without its leading `--` ("hard" and "--hard" are equivalent).
//
// WHY IT EXISTS BESIDE HasLongFlag (pg2-os1kq). HasLongFlag matches ONE exact
// spelling and documents that a caller needing git's unique-prefix abbreviations
// must ask for each spelling — and enumerating spellings by hand is exactly how
// one gets missed. Measured on git 2.54.0, 2026-07-30, one FRESH repo per
// spelling: `git reset --har HEAD~1`, `--ha` and `--h` each answered `HEAD is now
// at <sha> base` and reverted the worktree, i.e. all three PERFORMED the hard
// reset, and an exact-token `--hard` test saw none of them.
//
// IT DELIBERATELY OVER-MATCHES, AND THAT IS THE FAIL-SAFE DIRECTION. It knows
// nothing of git's option TABLE, so it also matches a prefix git ITSELF refuses
// as ambiguous (`git push --forc` answers `error: ambiguous option: forc`, yet
// this reports true for canonical "force"). For a DANGEROUS-FLAG BOOLEAN TEST
// that error is always in the safe direction: matching more spellings than git
// accepts can only make the caller MORE restrictive — Ask or Reject where it
// would have Approved — never less. That property is the entire reason this
// primitive is allowed to be imprecise, and stating it is what lets a reader
// check the reasoning rather than re-derive it.
//
// IT IS ALSO A USE RESTRICTION, for the same reason. A caller MUST NOT use it
// where the match's LENGTH or the flag's VALUE is load-bearing — locating an
// operand, eliding a separated flag argument, or reading an `=`-glued value —
// because there an over-match shifts an operand count or attributes a value to a
// flag git never parsed, and neither error has a safe direction. Such a caller
// needs a MEASURED per-subcommand minimum instead: HasAbbrevLongFlag below, or
// internal/rules/git's package-private hasAbbrevLongFlag (which predates it and
// records, in its own doc, the rule for choosing between the two).
//
// COVERS: the bare form `--name`; every non-empty `--`-prefixed prefix of it; and
// the `=`-glued form of any of those (`--force-with-lea=main`), whose value is
// NOT returned — this answers a boolean only. Scanning STOPS at a `--`
// end-of-options terminator, so `git reset -- --hard` reports false: that token
// is a pathspec, exactly as HasShortFlag and HasLongFlag treat one.
//
// DELIBERATELY DOES NOT COVER: short flags and clusters (HasShortFlag); `--no-`
// negations, since `no-hard` does not prefix-match `hard` and negating a flag is
// not setting it; and any spelling LONGER than canonical, so `--force-with-lease`
// is NOT a match for canonical "force" — which is what keeps a caller's two
// separately-worded gates for those flags from collapsing into one.
func HasLongFlagPrefix(args []string, canonical string) bool {
	canonical = strings.TrimPrefix(canonical, "--")
	if canonical == "" {
		return false
	}
	for _, a := range args {
		if a == "--" {
			return false // end of options; the rest are operands
		}
		if !strings.HasPrefix(a, "--") {
			continue // an operand, a lone "-", or a short flag / cluster
		}
		name := a[2:]
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i] // `--force-with-lea=main`: the flag is the part before '='
		}
		if name == "" || len(name) > len(canonical) {
			continue
		}
		if canonical[:len(name)] == name {
			return true
		}
	}
	return false
}

// HasAbbrevLongFlag reports whether args carries long flag name in any spelling
// a GNU-getopt_long-style parser would accept — the full name, or `--` followed
// by an unambiguous prefix down to minLen characters — and returns the value of
// the `=`-glued form of whichever spelling matched (see HasLongFlag for what an
// empty value means). It asks HasLongFlag once per candidate spelling, LONGEST
// FIRST, so the glued value is read from the longest spelling actually present.
//
// This is the exported twin of internal/rules/git's package-private
// hasAbbrevLongFlag (pg2-os1kq): same body, promoted here so a caller OUTSIDE
// the git rule — internal/rules/safecmds' `cp --target-directory`, whose
// `=`-glued VALUE becomes the write destination the gate rules on (pg2-1xq3m) —
// can share the primitive instead of re-deriving it. git.go's own
// hasAbbrevLongFlag is left as its own declaration rather than rewritten to call
// this one, so the git package's pinned AST-guard tests (flagmatch_test.go),
// which walk git.go's OWN source for the call shape, are undisturbed.
//
// minLen is per CALLER, not a property of this function: what a prefix is
// ambiguous with depends on which option table the target program parsed
// against, so it must be MEASURED against the real binary — see
// internal/rules/git/git.go's hasAbbrevLongFlag doc for the worked examples
// (`git push --repo`, `git config`) and its "WHICH MATCHER TO USE" section for
// the rule that decides between this and the open, unbounded HasLongFlagPrefix:
// use THIS where the match's LENGTH or the flag's VALUE is load-bearing: a
// caller reading the glued value needs to know it came from an unambiguous
// spelling of THIS flag, not an over-matched prefix that could, in principle,
// also be the start of some other option's name. Use HasLongFlagPrefix instead
// for a plain BOOLEAN dangerous-flag test, where over-matching is fail-safe.
func HasAbbrevLongFlag(args []string, name string, minLen int) (string, bool) {
	for n := len(name); n >= minLen; n-- {
		if v, ok := HasLongFlag(args, name[:n]); ok {
			return v, true
		}
	}
	return "", false
}

// ConfigStripDashDash removes every literal "--" token from args. UNLIKE most
// git subcommands, `git config` does not treat "--" as an end-of-options
// terminator: MEASURED on git 2.54.0, 2026-08-27, each of `git config --
// --edit`, `git config -- -e`, `git config -- --unset <key>` and `git config
// -- <key> <value>` performs the SAME write/edit its unprefixed spelling does.
// Every OTHER primitive in this file (HasShortFlag, HasLongFlag,
// HasAbbrevLongFlag, FirstOperand, Operands) DOES treat "--" as a
// terminator — correctly, for the subcommands they were measured against — so
// a caller checking `git config`'s write shape must strip a literal "--"
// token BEFORE handing args to any of them, or a write hidden behind one
// silently clears (pg2-uaxa3 measured this for `git config -- --edit`/`-- -e`
// slipping past both the write-flag and write-subcommand checks).
//
// NOT A GENERAL-PURPOSE PRIMITIVE: do not reuse this for a subcommand where
// "--" really does terminate options (nearly every other one).
func ConfigStripDashDash(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--" {
			out = append(out, a)
		}
	}
	return out
}

// ConfigHasEditFlag reports whether args contains git config's
// --edit-invoking `-e` in ANY short-cluster spelling (`-e`, `-ez`, `-ze`, …),
// correctly treating a byte following `-f` or `-t` WITHIN THE SAME TOKEN as
// that flag's glued value rather than a further flag letter — `git config`'s
// only two value-taking shorts.
//
// NEITHER existing short-flag primitive gets this right: an exact-token test
// (`a == "-e"`) avoids the `-f`/`-t` glued-value false trigger but then misses
// every clustered spelling; HasShortFlag catches every cluster but knows no
// flag arity, so it ALSO fires on a glued value that merely happens to
// contain the byte 'e' (`-fsome.env`). This is the narrower, config-specific
// primitive that gets both right. MEASURED on git 2.54.0, 2026-08-27: `git
// config -ez` and `git config -ze` both invoke $GIT_EDITOR (pg2-uaxa3).
//
// Does NOT itself ignore a literal "--" token (it is not a flag by this
// scan's own `a[0] != '-'` test already excludes it, but a byte AFTER "--" is
// still scanned normally, matching the measured non-terminating behavior) —
// see ConfigStripDashDash's doc for why "--" needs no special handling here
// either way once this scan simply keeps going past it.
func ConfigHasEditFlag(args []string) bool {
	for _, a := range args {
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue // operand, lone "-", or a long flag
		}
		for i := 1; i < len(a); i++ {
			c := a[i]
			if c == 'e' {
				return true
			}
			if c == 'f' || c == 't' {
				break // rest of THIS token is that flag's glued value
			}
		}
	}
	return false
}

// FirstOperand returns the first non-flag token in args and its index, or
// ("", -1) when args holds no operand.
//
// COVERS: leading flags of every shape — bare shorts, clustered shorts,
// `--long`, and `--long=value` (whose value is part of the same token, so it is
// skipped with the flag). A lone `-` counts as an OPERAND, matching git (`git
// switch -`). After a `--` end-of-options terminator the next token is the
// operand even if it begins with a dash.
//
// DELIBERATELY DOES NOT COVER: SEPARATED flag values. `-f <file>` /
// `--repo <repo>` would need a per-command flag-arity table, which this
// primitive does not build, so such a value IS returned as the operand. This
// limit is load-bearing rather than incidental: for the pinned `git remote -v add
// upstream <url>` case the answer MUST be "add", and a value-skipping
// implementation would consume "add" as -v's value and wrongly return
// "upstream". Callers whose command has separated-value flags before the first
// operand MUST account for the shift themselves.
func FirstOperand(args []string) (string, int) {
	idx := operandIndexes(args, 1)
	if len(idx) == 0 {
		return "", -1
	}
	return args[idx[0]], idx[0]
}

// Operands returns EVERY non-flag token in args, in order — FirstOperand's
// whole-list form, walking the same operand scan so the two cannot disagree
// about what counts as a flag.
//
// It exists for a caller that must be immune to POSITION, not merely to leading
// flags: `git config` accepts its key at three different operand positions
// (`git config <key> <value>`, `git config set <key> <value>`, and after a
// separated `-f <file>`), so asking "does ANY operand name a gated key" is the
// only formulation none of those spellings walks around.
//
// It inherits FirstOperand's separated-value limitation, and for this use the
// direction of that error is load-bearing and SAFE: a separated flag value
// (`-f <file>`, `--type bool`) is returned as an extra operand, so the returned
// slice can only ever be a SUPERSET of the real operands. A caller scanning it
// for a dangerous token therefore cannot lose that token to a flag-arity trick;
// it can only consider one extra token that is not really an operand. A caller
// that needs the operands to be exact MUST NOT use this.
func Operands(args []string) []string {
	idx := operandIndexes(args, 0)
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, args[i])
	}
	return out
}

// operandIndexes returns the indexes of the non-flag tokens in args, stopping
// once limit operands have been found (limit <= 0 means "all"). It is the single
// operand walk shared by FirstOperand and ClassifyPushRefspecs so the two cannot
// disagree about what counts as a flag. See FirstOperand's doc for the covered
// forms and the separated-value limitation.
func operandIndexes(args []string, limit int) []int {
	var out []int
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// End of options: every remaining token is an operand.
			for j := i + 1; j < len(args); j++ {
				out = append(out, j)
				if limit > 0 && len(out) >= limit {
					return out
				}
			}
			return out
		}
		if len(a) > 1 && a[0] == '-' {
			continue // a flag (short, cluster, long, or long=value); a lone "-" is not
		}
		out = append(out, i)
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	return out
}

// Refspec is one classified `git push` refspec operand, as produced by
// ClassifyPushRefspecs. Src/Dst are reported verbatim — no ref is resolved,
// abbreviated, or expanded to refs/heads/*.
type Refspec struct {
	Raw    string // the operand exactly as written, including any leading '+'
	Force  bool   // a leading '+' was present: this refspec is a per-refspec force
	Delete bool   // empty source with an explicit ':' (`:dst`): a remote-ref delete
	Src    string // source side with any leading '+' stripped ("" for `:dst`)
	Dst    string // destination side ("" when HasDst is false)
	HasDst bool   // an explicit ':' was present, so Dst is meaningful
}

// SameRef reports whether the refspec pushes to the same ref name it reads from,
// i.e. whether it is NOT a cross-branch push. A refspec with no explicit
// destination (`main`) is same-ref by definition. `HEAD` is compared as the
// literal string it is, so `HEAD:main` reports false even when HEAD is main —
// a deliberate over-approximation, since resolving HEAD is impossible from the
// token alone and the safe direction for a caller gating a push is to treat it
// as cross-branch.
func (r Refspec) SameRef() bool {
	if !r.HasDst {
		return true
	}
	return r.Src == r.Dst
}

// ClassifyPushRefspecs classifies the refspec operands of a `git push`. args are
// the tokens AFTER the `push` subcommand (GitInvocation's rest).
//
// For `git push` the FIRST non-flag operand is the REMOTE, not a refspec; only
// the operands after it are refspecs. A nil/empty result therefore means "no
// refspec given" (`git push`, `git push origin`), which a caller MUST be able to
// tell apart from a present-but-same-branch refspec — the latter is one element
// whose SameRef reports true.
//
// COVERS the refspec forms `src`, `src:dst`, `+src:dst`, `+src`, `:dst` (a
// remote-ref delete), and `HEAD:dst`. Force is the `+` prefix; Delete is an
// empty source with an explicit ':'.
//
// DELIBERATELY DOES NOT COVER: flags that force or delete for the WHOLE
// invocation. `--force` / `-f` forces every refspec and `--delete` / `-d` turns
// every operand into a delete, yet neither is reflected in the returned
// Refspecs — a caller MUST check those separately with HasLongFlag /
// HasShortFlag. Likewise `--all` / `--mirror` / `--tags`, which push refs no
// operand names. It resolves nothing: no ref lookup, no refs/heads/*
// expansion, no `push.default` / `remote.<name>.push` config, so `main` and
// `refs/heads/main` are different strings here. `src:dst:more` splits on the
// FIRST ':' only, and `src:` yields HasDst with an empty Dst rather than a
// Delete (git rejects it; only an empty SOURCE is a delete).
//
// It inherits FirstOperand's separated-value limitation: with `git push -o opt
// origin +main` the operand walk takes "opt" as the remote and "origin" as a
// refspec, shifting the remote attribution. Force and Delete detection survives
// that shift — every operand after the first is still classified, so a `+main`
// or `:main` token is still reported — but a caller reading the remote or
// SameRef from a shifted parse would be reading the wrong operand.
func ClassifyPushRefspecs(args []string) []Refspec {
	idx := operandIndexes(args, 0)
	if len(idx) < 2 {
		return nil // no operands at all, or only the remote
	}
	out := make([]Refspec, 0, len(idx)-1)
	for _, i := range idx[1:] {
		out = append(out, parseRefspec(args[i]))
	}
	return out
}

// parseRefspec classifies one refspec token. See ClassifyPushRefspecs for the
// covered forms and the omissions.
func parseRefspec(tok string) Refspec {
	r := Refspec{Raw: tok}
	body := tok
	if strings.HasPrefix(body, "+") {
		r.Force = true
		body = body[1:]
	}
	if src, dst, found := strings.Cut(body, ":"); found {
		r.Src, r.Dst, r.HasDst = src, dst, true
		r.Delete = src == "" // an EMPTY SOURCE is the delete form (`:dst`)
	} else {
		r.Src = body
	}
	return r
}

// GitInvocation parses a git command's pre-subcommand options (the slice AFTER the
// `git` executable), returning the ordered `-C <path>` chdir values, the subcommand
// ("" if none), and the args after it. It consumes the option-arg for
// -C/-c/--git-dir/--work-tree/--namespace exactly as git does.
func GitInvocation(args []string) (chdirs []string, subcmd string, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-C":
			if i+1 < len(args) {
				chdirs = append(chdirs, args[i+1])
			}
			i += 2
			continue
		case "-c", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return chdirs, a, args[i+1:]
		}
	}
	return chdirs, "", nil
}

// GitDirWorkTreeOperands scans args — a git invocation's own argv, after the
// executable (cmdparse has already unwrapped any env/command/nice/...
// prefix by this point — see unwrapCommand — so `env GIT_DIR=... git
// --work-tree=... init` reaches here exactly like the native
// `GIT_DIR=... git --work-tree=... init` spelling: the `env` assignment is
// lifted into ParsedCommand.EnvVars and only `--work-tree=...` remains in
// Args) — for the PRE-SUBCOMMAND `--git-dir` / `--work-tree` options and
// returns every value found, in argv order.
//
// It walks the identical pre-subcommand span GitInvocation walks, honouring
// the same `-C`/`-c`/`--namespace` arities, so the two scans always agree on
// where the subcommand starts. Unlike GitInvocation — which only needs to
// skip PAST `--git-dir`/`--work-tree` to find the subcommand and so never
// returns their VALUE — this exists to recover exactly that value:
// pg2-yoqsr's temp-root carve-out needs to see where these two flags
// redirect the effective repository, the same way it needs to see
// GIT_DIR/GIT_WORK_TREE.
//
// Both the separated (`--git-dir <path>`) and glued (`--git-dir=<path>`)
// spellings are recognised. Git accepts no ABBREVIATION of either — measured
// on git 2.54.0 (see internal/rules/git's classify doc comment for the
// citation and method: `--git-di=<dir>`, `--work-tre=<dir>` etc. all answer
// "unknown option") — so an exact-token/exact-prefix test is git's own parse
// here, not an under-match the way an abbreviation-blind test would be for a
// flag git DOES let the caller shorten.
func GitDirWorkTreeOperands(args []string) (gitDirs, workTrees []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--git-dir":
			if i+1 < len(args) {
				gitDirs = append(gitDirs, args[i+1])
			}
			i += 2
		case strings.HasPrefix(a, "--git-dir="):
			gitDirs = append(gitDirs, strings.TrimPrefix(a, "--git-dir="))
			i++
		case a == "--work-tree":
			if i+1 < len(args) {
				workTrees = append(workTrees, args[i+1])
			}
			i += 2
		case strings.HasPrefix(a, "--work-tree="):
			workTrees = append(workTrees, strings.TrimPrefix(a, "--work-tree="))
			i++
		case a == "-C" || a == "-c" || a == "--namespace":
			i += 2 // consumed by GitInvocation for a different purpose; not this scan's concern
		case strings.HasPrefix(a, "-"):
			i++
		default:
			return gitDirs, workTrees // reached the subcommand
		}
	}
	return gitDirs, workTrees
}
