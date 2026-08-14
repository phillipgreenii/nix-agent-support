package cmdparse

import "strings"

// This file holds shared argument-flag parsing helpers for grep/rg and jq —
// relocated here (pg2-ia640.2) from the safecmds rule so the sibling secrets
// rule can reuse them without importing a rules package. Both callers need to
// separate a command's real FILE-path arguments from its non-path arguments
// (search patterns, replacement/glob/context values, jq variables and filters)
// before running their path/secret checks. SkipMessageArgs (pg2-ia640.5) extends
// the same family to FREE-TEXT MESSAGE arguments.

// grepFlagsWithValue lists grep flags that consume the next argument as a value
// (so that value is NOT a searched file path). These forms are valid for rg too.
var grepFlagsWithValue = map[string]bool{
	"-e": true, "--regexp": true,
	"-f": true, "--file": true,
	"-m": true, "--max-count": true,
	"-A": true, "--after-context": true,
	"-B": true, "--before-context": true,
	"-C": true, "--context": true,
	"--include": true, "--exclude": true, "--exclude-dir": true,
	"--label": true, "--color": true, "--colours": true,
}

// rgFlagsWithValue lists ripgrep-specific flags that consume the next argument.
// Their SHORT forms -r/-E/-T are value-consuming in rg but BOOLEAN in grep
// (-r=recursive, -E=extended-regexp, -T=initial-tab), so they are honored only
// for rg — folding them into grepFlagsWithValue would make grep mis-skip a real
// file path (e.g. `grep -r pat /secrets/x` would drop /secrets/x). (pg2-ia640.2)
var rgFlagsWithValue = map[string]bool{
	"-g": true, "--glob": true, "--iglob": true,
	"-t": true, "--type": true,
	"-T": true, "--type-not": true,
	"--type-add": true,
	"-r":         true, "--replace": true,
	"-M": true, "--max-columns": true,
	"--sort": true, "--sortr": true,
	"-E": true, "--encoding": true,
	"--engine":      true,
	"--pre":         true,
	"--ignore-file": true,
	"-d":            true, "--max-depth": true,
}

// SkipGrepPattern returns args with the positional search PATTERN removed (grep
// and rg take the pattern as the first non-flag argument, which is not a file)
// and the value of every value-consuming flag removed, leaving only file-path
// arguments for downstream path/secret checks.
//
// When -e/--regexp or -f/--file is present there is NO positional pattern (the
// pattern(s) come from those flags), so every positional is a file; that branch
// still strips each value-flag's value so the pattern / pattern-file value is
// itself not mistaken for a searched path (fixes the prior unstripped-branch bug
// where `grep -e .env file.log` leaked `.env`). (pg2-ia640.2)
//
// cmd selects the flag vocabulary: "rg" additionally honors rgFlagsWithValue
// (see its doc for why the conflicting short flags are rg-only).
func SkipGrepPattern(cmd string, args []string) []string {
	isValueFlag := func(a string) bool {
		if grepFlagsWithValue[a] {
			return true
		}
		return cmd == "rg" && rgFlagsWithValue[a]
	}
	// -e/--regexp and -f/--file supply the pattern(s), so there is no positional
	// pattern to skip; every positional is a file.
	patternSkipped := false
	for _, a := range args {
		if a == "-e" || a == "--regexp" || a == "-f" || a == "--file" {
			patternSkipped = true
			break
		}
	}
	var result []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after -- is files.
			result = append(result, args[i:]...)
			break
		}
		if isValueFlag(a) && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if !patternSkipped {
			patternSkipped = true
			i++
			continue // skip the positional pattern
		}
		result = append(result, a)
		i++
	}
	return result
}

// jqValueFlags lists jq flags that consume two value arguments (name value).
// These arguments may look like paths (e.g. --arg dir "/app/src") but are jq
// variables, not file references.
var jqValueFlags = map[string]bool{
	"--arg": true, "--argjson": true,
	"--slurpfile": true, "--rawfile": true, "--jsonargs": true,
}

// jqOneArgFlags lists jq flags that consume one value argument.
var jqOneArgFlags = map[string]bool{
	"--indent": true, "--tab": true, "--from-file": true, "--jsonargs": true,
	"-f": true, "--join-output": true,
}

// SkipJqValueFlags returns the args with jq value-flag arguments removed, so path
// checking only sees actual file arguments.
func SkipJqValueFlags(args []string) []string {
	var result []string
	i := 0
	for i < len(args) {
		a := args[i]
		if jqValueFlags[a] && i+2 < len(args) {
			i += 3 // skip flag + name + value
			continue
		}
		if jqOneArgFlags[a] && i+1 < len(args) {
			i += 2 // skip flag + value
			continue
		}
		result = append(result, a)
		i++
	}
	return result
}

// messageFlags maps a command basename to the flags whose VALUE the command
// STORES AS TEXT — a commit message, a bead comment/reason/notes, a PR body or
// title. Such a value is never opened, executed or transmitted as a path, so
// testing it as a filename is what made a paragraph of prose that merely MENTIONS
// `~/.ssh/agent` or `secrets/prod.yaml` read as a credential reference
// (pg2-ia640.5; asklog rows 313634, 325419, 325591, 325750).
//
// THE SET OF COMMANDS IS CLOSED, and so is the blast radius: SkipMessageArgs
// filters nothing for a basename absent from this map, so an unlisted command
// (`cp ~/.ssh/id_rsa /tmp`) keeps the unfiltered behaviour.
//
// NO FILE-TAKING FLAG APPEARS HERE, deliberately — that is the rule for extending
// the tables, and it is the difference between a false positive and a bypass:
//   - git `-F` / `--file` read the message FROM a file, so the value IS a path;
//     `git commit -F ~/.ssh/id_rsa` must keep prompting.
//   - the same for gh `-F` / `--body-file`, and bd `--file` / `--body-file` /
//     `--design-file` / `--graph` / `-f` / `--metadata @file.json`.
//
// bd's `-t` is `--type`, NOT `--title`, which is why only the long `--title`
// spelling is listed for bd; gh's `-t` IS `--title` (and `--template` for the
// list/api subcommands, likewise free text).
var messageFlags = map[string]map[string]bool{
	"bd": {
		"--reason": true, "--notes": true, "--append-notes": true,
		"--description": true, "--acceptance": true, "--design": true,
		"--context": true, "--title": true,
	},
	"git": {"-m": true, "--message": true},
	"gh":  {"--body": true, "-b": true, "--title": true, "-t": true},
}

// gitMessageSubcommands are the git subcommands whose `-m` is a MESSAGE. The gate
// exists because `-m` is BOOLEAN in other subcommands and the following token is
// then a real path: `git checkout -m ~/.ssh/config` (--merge, then a pathspec git
// WRITES) asks today and must keep asking. An unrecognized subcommand keeps
// today's behaviour, so a missing entry costs a false positive, never a bypass.
var gitMessageSubcommands = map[string]bool{
	"commit": true, "tag": true, "merge": true, "stash": true,
	"notes": true, "revert": true, "cherry-pick": true,
}

// SkipMessageArgs returns args with cmd's FREE-TEXT MESSAGE arguments removed, so
// path checking sees only arguments that could name a file. cmd is the command's
// basename (see messageFlags for the closed set and for why no file-taking flag
// is in it); any other cmd returns args unchanged.
//
// Removed, and nothing else:
//   - the token FOLLOWING a message flag (`bd close --reason <prose>`,
//     `git commit -m <prose>`, `gh pr comment --body <prose>`);
//   - the single token of the EQUALS spelling (`--reason=<prose>`). Today's
//     callers already skip every `-`-prefixed token before testing it, so this
//     arm changes no verdict; it is written out so the filter is complete on its
//     own terms rather than by relying on a caller's flag skip;
//   - for bd, the trailing BODY positional of `bd comment <id> <body>` (see
//     bdCommentBodyIndex). The `<id>` positional is NOT removed.
//
// Two boundaries are load-bearing:
//   - Scanning STOPS at a bare `--`: after it a token is an operand, not a flag,
//     so `git commit -- -m ~/.ssh/id_rsa` must not have the path swallowed as
//     `-m`'s value.
//   - git's `-m`/`--message` is honored only for gitMessageSubcommands. The
//     subcommand is looked for as a TOKEN anywhere before the flag rather than
//     resolved as "the first positional", so `git -C <dir> commit -m …` works
//     without this file needing a table of git's own global value-flags.
//
// DECLINED: a "drop any argument containing a newline" prose backstop. Every
// observed row is covered by the positions above, and the bounded form — drop a
// multi-line argument only when its newline-stripped/trimmed form is not itself a
// secret path — can never provide relief, because stripping newlines is MONOTONE
// over secretpath.IsSecret: a newline cannot sit inside a component that matched,
// and removing newlines elsewhere only ever JOINS text into new matches (as
// trimming a leading newline turns the non-matching `"\n.ssh/x"` into the
// matching `".ssh/x"`). So an argument that is a hit today is still a hit
// stripped, i.e. still kept. A newline is therefore not evidence of prose, and an
// UNbounded version would be an escape hatch for every command rather than these
// three.
func SkipMessageArgs(cmd string, args []string) []string {
	flags := messageFlags[cmd]
	if flags == nil {
		return args
	}
	bodyIdx := -1
	if cmd == "bd" {
		bodyIdx = bdCommentBodyIndex(args)
	}
	result := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after -- is an operand, never a flag.
			result = append(result, args[i:]...)
			break
		}
		if i == bodyIdx {
			i++
			continue
		}
		if name, ok := equalsFlagName(a); ok {
			if isMessageFlag(cmd, flags, args[:i], name) {
				i++ // the whole `--flag=value` token is the message
				continue
			}
			result = append(result, a)
			i++
			continue
		}
		if isMessageFlag(cmd, flags, args[:i], a) && i+1 < len(args) {
			i += 2 // the flag plus the message value it consumes
			continue
		}
		result = append(result, a)
		i++
	}
	return result
}

// isMessageFlag reports whether name is one of cmd's message flags, given the
// tokens that PRECEDE it (before), which is what the git subcommand gate reads.
func isMessageFlag(cmd string, flags map[string]bool, before []string, name string) bool {
	if !flags[name] {
		return false
	}
	if cmd == "git" && !gitTakesMessage(before) {
		return false
	}
	return true
}

// gitTakesMessage reports whether any token preceding the flag is a git
// subcommand whose `-m` is a message (see gitMessageSubcommands).
func gitTakesMessage(before []string) bool {
	for _, a := range before {
		if gitMessageSubcommands[a] {
			return true
		}
	}
	return false
}

// equalsFlagName returns the flag NAME of an `--flag=value` token and whether arg
// has that shape at all. A leading `-` is required, so a positional containing an
// `=` (`bd comment x "a=b"`) is not mistaken for a flag.
func equalsFlagName(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", false
	}
	name, _, found := strings.Cut(arg, "=")
	if !found {
		return "", false
	}
	return name, true
}

// bdCommentBodyIndex returns the index of the BODY positional of the
// `bd comment <id> <body>` PREFIX FORM, or -1 when args are not that form.
//
// The form is matched STRICTLY — args[0] is `comment` and args[1], args[2] are
// both non-flag tokens — rather than by counting positionals, because an unknown
// value-taking flag ahead of the body (`bd comment --actor X <id> <body>`) shifts
// that count, and dropping "the third positional" of the shifted list would drop
// the WRONG token: with `bd comment --actor X ~/.ssh/id_rsa <body>` it would drop
// the path and keep the prose. Both observed rows (325419, 325750) are the prefix
// form; every other spelling keeps today's behaviour, which is the fail-closed
// direction.
//
// Exactly ONE token is dropped. bd also accepts an unquoted multi-word body
// (`bd comment <id> Working on this now`), whose second word onward stays a
// candidate — fail-closed again, and a single quoted body is the shape agents
// actually emit.
func bdCommentBodyIndex(args []string) int {
	if len(args) < 3 || args[0] != "comment" {
		return -1
	}
	if strings.HasPrefix(args[1], "-") || strings.HasPrefix(args[2], "-") {
		return -1
	}
	return 2
}
