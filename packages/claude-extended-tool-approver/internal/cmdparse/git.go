package cmdparse

import "strings"

// GIT FLAG / REFSPEC MATCHERS (pg2-si0bp)
//
// HasShortFlag, HasLongFlag, FirstOperand, Operands and ClassifyPushRefspecs
// below are the token-level primitive the git rule needs to see the flag and refspec
// forms git ITSELF accepts. The rule module currently locates flags and operands
// by exact token equality, so a clustered short (`-fu`), an `=`-glued long
// (`--force-with-lease=other`), and a force/delete expressed purely in the
// refspec (`+main`, `:main`) are all invisible to it.
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
// `--for` for `--force`) or `--no-` negations; a caller that must catch those
// MUST ask for each spelling.
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
