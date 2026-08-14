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
// which is NOT a path the tool opens — a pattern, a glob, a number, an enum, a
// separator. These forms are valid for rg too.
//
// A FLAG WHOSE OPERAND THE COMMAND OPENS NEVER BELONGS HERE (pg2-ygjs5, the same
// rule pg2-wrxg6 applied to the jq tables and messageFlags' doc states for its own).
// `-f`/`--file` USED TO BE HERE and was a live credential-read hole: the operand is
// the PATTERN FILE, which grep opens and whose contents become the patterns, so
// stripping it deleted a real path from screening. Those and every other
// file-opening flag now live in grepFileFlags, which EMITS the operand instead.
//
// TWO ADMISSION RULES, both learned from measured holes:
//
//   - MANDATORY VALUE ONLY. An OPTIONAL-value flag (`--color[=WHEN]`) consumes
//     NOTHING in the space spelling, so listing it here makes it SWALLOW THE
//     FOLLOWING TOKEN — the pg2-wrxg6 boolean class. `--color` was here and measured
//     `approve` for `grep --color pat ~/.ssh/id_rsa` against `reject` for the
//     positional control `grep pat ~/.ssh/id_rsa`. It is gone; only the glued
//     `--color=WHEN` spelling carries a value, and the glued arm handles that.
//     `--colour`, `--group-separator`, `--separator`, `--tabs`, `--tag`, `--width`,
//     `--fuzzy`, `--hexdump`, `--hyperlink`, `--mmap`, `--pretty`, `--query` and
//     ugrep's optional-value `--sort` are absent for this reason, not by oversight.
//   - LONG FORMS ONLY for anything added beyond the shorts already here. A short
//     flag's arity DIFFERS BETWEEN IMPLEMENTATIONS and a boolean short listed here
//     drops a real path: ugrep's `-J` is `--jobs=NUM` but BSD grep's `-J` is boolean
//     `--bz2decompress`. This is the same hazard rgFlagsWithValue' doc records for
//     `-r`/`-E`/`-T`. Long spellings do not collide — a long flag a given grep does
//     not have is an error, so no real invocation is affected.
//
// Verified against the full `--help` inventories of ugrep 7.5.0 (this machine's
// `grep`) and GNU grep rather than by inspection, so the table is an inventory
// rather than a sample.
var grepFlagsWithValue = map[string]bool{
	"-e": true, "--regexp": true,
	"-m": true, "--max-count": true,
	"-A": true, "--after-context": true,
	"-B": true, "--before-context": true,
	"-C": true, "--context": true,
	"--include": true, "--exclude": true, "--exclude-dir": true,
	"--label": true, "--colours": true, "--colors": true,
	// GNU grep + ugrep, mandatory value, never a path the tool opens.
	"-D": true, "--devices": true,
	"-d": true, "--directories": true,
	"--binary-files": true,
	// ugrep 7.5.0 additions (long forms only, mandatory value).
	"--include-dir": true, "--iglob": true,
	"--exclude-fs": true, "--include-fs": true,
	"--context-separator": true, "--delay": true, "--depth": true,
	"--encoding": true, "--filter-magic-label": true, "--format": true,
	"--jobs": true, "--max-files": true, "--max-line": true,
	"--min-count": true, "--min-line": true, "--range": true,
	"--replace": true, "--zmax": true,
	"--file-magic": true, "--file-extension": true, "--file-type": true,
	"--neg-regexp": true,
}

// rgFlagsWithValue lists ripgrep-specific flags that consume the next argument.
// Their SHORT forms -r/-E/-T are value-consuming in rg but BOOLEAN in grep
// (-r=recursive, -E=extended-regexp, -T=initial-tab), so they are honored only
// for rg — folding them into grepFlagsWithValue would make grep mis-skip a real
// file path (e.g. `grep -r pat /secrets/x` would drop /secrets/x). (pg2-ia640.2)
//
// `--ignore-file` and `--pre` USED TO BE HERE and both measured a live ALLOW
// (pg2-ygjs5). `--ignore-file FILE` is a file rg READS, so it moved to rgFileFlags.
// `--pre CMD` names a PREPROCESSOR COMMAND rg EXECUTES per file, which is a worse
// class than a read — screening its operand as a path catches `--pre ~/.ssh/id_rsa`
// but not `--pre evilcmd` — so it moved to rgExecFlags, whose presence disqualifies
// rg from being treated as a read-only command at all.
//
// The same two admission rules as grepFlagsWithValue apply; see its doc. Verified
// against the full `rg --help` inventory of ripgrep 15.1.0.
var rgFlagsWithValue = map[string]bool{
	"-g": true, "--glob": true, "--iglob": true,
	"-t": true, "--type": true,
	"-T": true, "--type-not": true,
	"--type-add": true, "--type-clear": true,
	"-r": true, "--replace": true,
	"-M": true, "--max-columns": true,
	"--sort": true, "--sortr": true,
	"-E": true, "--encoding": true,
	"--engine": true,
	"-d":       true, "--max-depth": true,
	// ripgrep 15.1.0 additions (mandatory value, never a path rg opens).
	"-j": true, "--threads": true,
	"--pre-glob": true, "--max-filesize": true,
	"--dfa-size-limit": true, "--regex-size-limit": true,
	"--colors": true, "--generate": true,
	"--context-separator": true, "--path-separator": true,
	"--field-context-separator": true, "--field-match-separator": true,
	"--hyperlink-format": true,
}

// grepFileFlags lists grep-family flags whose operand IS A PATH THE TOOL OPENS. Their
// operand is neither skipped (which deleted a real path from screening — pg2-ygjs5)
// nor left to fall through as a positional (where the pattern heuristic below can eat
// it): SkipGrepPattern EMITS it directly as a candidate path.
//
// EMITTING IS WHAT MAKES THIS INDEPENDENT OF THE PATTERN HEURISTIC, and that is the
// point. Leaving the operand to become a positional looks equivalent and is not:
// measured on main @974d0276, `grep --exclude-from ~/.ssh/id_rsa pat /tmp` returned
// `approve` against `reject` for the positional control, because the key landed in the
// FIRST positional slot and was discarded as "the pattern". A flag whose operand this
// table names can never be lost that way.
//
// `-f`/`--file` are ALSO pattern sources, so they appear in patternSourceFlags too;
// the other entries are not.
//
// MANDATORY-VALUE ONLY, and this table is where that rule bites hardest. ugrep's
// `--config[=FILE]`, `--save-config[=FILE]` and `--ignore-files[=FILE]` name files too
// and are deliberately ABSENT: their value is OPTIONAL, so in the space spelling they
// consume NOTHING and the following token is the ugrep PATTERN, not their operand.
// Listing them here would swallow that token — measured while writing this fix,
// `grep --config pat ~/.ssh/id_rsa` went from `reject` to `approve` with them included,
// a LESS-restrictive transition, which is the pg2-wrxg6 boolean class reintroduced by a
// security fix. They need no entry anywhere: `--config=FILE` is the only spelling that
// carries a path, and SkipGrepPattern's glued arm already emits the value of any flag
// this file does not claim as a non-path value flag.
var grepFileFlags = map[string]bool{
	"-f": true, "--file": true, // the PATTERN FILE (POSIX, GNU, ugrep, rg)
	"--exclude-from": true, // GNU grep + ugrep
	// ugrep 7.5.0, mandatory value, each a file ugrep reads.
	"--include-from": true,
	"--from":         true,
}

// rgFileFlags lists the rg-only flags whose operand is a path rg opens. Kept separate
// from grepFileFlags for the same reason rgFlagsWithValue is separate: the vocabulary
// is honored only for rg.
var rgFileFlags = map[string]bool{
	"--ignore-file": true,
}

// grepProgramFlags and rgProgramFlags list flags whose operand NAMES A PROGRAM THE
// TOOL RUNS. That is a different and WORSE class than a file read, and it has two
// consequences rather than one, which is why these are their own tables:
//
//  1. THE OPERAND IS STILL A CANDIDATE PATH, so it is emitted exactly as
//     grepFileFlags' operands are. `rg --pre ~/.ssh/id_rsa` must reach the same verdict
//     as naming that key positionally, and it only does if the operand is screened.
//  2. BUT SCREENING THE OPERAND IS NOT ENOUGH, because it catches
//     `rg --pre /tmp/evil` only insofar as that is SPELLED as a path and misses
//     `rg --pre evilcmd`, which resolves on PATH and looks like nothing at all. grep
//     and rg are approvable because they only READ; a program these flags name makes
//     the invocation an EXECUTION primitive, so the disqualification is of the WHOLE
//     command. GrepExecFlag reports it and internal/rules/safecmds refuses on it.
//
// EVERY ENTRY HERE TAKES A MANDATORY VALUE, so consuming the operand is safe — the
// same admission rule grepFlagsWithValue' doc states, and it is why the optional-value
// `--pager`/`--view` are in grepExecPresenceFlags instead.
var grepProgramFlags = map[string]bool{
	"--filter": true, // ugrep: `--filter='pdf:pdftotext % -'` runs the named program
}

var rgProgramFlags = map[string]bool{
	"--pre":          true, // rg: runs COMMAND per file and searches its output
	"--hostname-bin": true, // rg: runs a program to get the hostname
}

// grepExecPresenceFlags lists flags that make the tool run a program but whose value is
// OPTIONAL in the space spelling (`--pager[=COMMAND]`), so NO operand may be consumed —
// consuming one would swallow the following token and drop a real path, which is the
// pg2-wrxg6 boolean class. Only their PRESENCE is used, and presence is all the
// read-only disqualification needs.
var grepExecPresenceFlags = map[string]bool{
	"--pager": true, // ugrep: `--pager[=COMMAND]`
	"--view":  true, // ugrep: `--view[=COMMAND]`
}

// GrepExecFlag returns the first flag in args that makes cmd RUN A PROGRAM (see
// grepProgramFlags / rgProgramFlags / grepExecPresenceFlags), and whether there is one.
// Both the space and the `--flag=value` spellings are recognized, because a table keyed
// on one exact token is what every defect in this family has been (pg2-cu3ro,
// pg2-wrxg6, pg2-ygjs5).
//
// Scanning STOPS at a bare `--`: after it a token is an operand, so `rg -- --pre` is a
// search for the literal string `--pre`, not a preprocessor invocation.
//
// cmd is the command's basename; any name other than a grep-family one or "rg" reports
// no flag, so this cannot leak to an unrelated command.
func GrepExecFlag(cmd string, args []string) (string, bool) {
	if cmd != "rg" && !isGrepFamily(cmd) {
		return "", false
	}
	for _, a := range args {
		if a == "--" {
			return "", false
		}
		name := a
		if n, ok := equalsFlagName(a); ok {
			name = n
		}
		if grepProgramFlags[name] || grepExecPresenceFlags[name] || (cmd == "rg" && rgProgramFlags[name]) {
			return name, true
		}
	}
	return "", false
}

// isGrepFamily reports whether a basename is one of the grep implementations whose flag
// vocabulary grepFlagsWithValue / grepFileFlags / grepProgramFlags describe.
func isGrepFamily(cmd string) bool {
	switch cmd {
	case "grep", "egrep", "fgrep", "ugrep", "ug", "rgrep":
		return true
	}
	return false
}

// patternSourceFlags lists the flags that SUPPLY the search pattern(s), so there is no
// positional pattern and EVERY positional is a file. See SkipGrepPattern.
//
// It is keyed on the flag NAME, and SkipGrepPattern looks the name up for the glued
// spelling too. That is load-bearing rather than tidy: `grep --file=pats.txt real.log`
// supplies the pattern from a file, so `real.log` is a FILE — and with only the space
// spelling recognized it was instead discarded as "the pattern", which is a real path
// lost from screening.
//
// `--neg-regexp` (ugrep) is included and its short `-N` is NOT: `-N` is rg's BOOLEAN
// `--no-line-number`, and the long spelling exists only in ugrep so it cannot collide.
// The cost of a wrong TRUE here is a pattern screened as a path (a candidate that
// matches nothing); the cost of a wrong FALSE is a file that stops being screened, so
// the fail-safe direction is to include.
var patternSourceFlags = map[string]bool{
	"-e": true, "--regexp": true,
	"-f": true, "--file": true,
	"--neg-regexp": true,
}

// SkipGrepPattern returns the subset of args that could name a FILE the tool opens,
// for downstream path/secret checks. Three things are removed — the positional search
// PATTERN (grep and rg take it as the first non-flag argument, which is not a file),
// the value of every non-path value-consuming flag, and bare flag names — and one thing
// is ADDED that a filter alone would never produce: the operand of a file-opening flag
// (grepFileFlags / rgFileFlags) is emitted even though it followed a `-` token.
//
// When a patternSourceFlags flag is present there is NO positional pattern (the
// pattern(s) come from that flag), so every positional is a file; that branch still
// strips each value-flag's value so the pattern value is itself not mistaken for a
// searched path (fixes the prior unstripped-branch bug where `grep -e .env file.log`
// leaked `.env`). (pg2-ia640.2)
//
// EVERY FLAG TEST IS DONE FOR BOTH SPELLINGS — `--flag value` and `--flag=value`
// (pg2-ygjs5, extending pg2-cu3ro). A glued token is ONE argv token, so the blanket
// `strings.HasPrefix(a, "-")` skip below discarded the flag NAME and the VALUE
// together, and the value never reached the screening that firstSecretRef's
// GluedFlagValue arm performs for every other command. Measured on main @974d0276:
// `grep --file=~/.ssh/id_rsa x.log` returned `approve` while the positional control
// returned `reject`. The glued arm therefore judges the token by its flag NAME, exactly
// as the space form is judged, and reaches the same verdict in both spellings.
//
// AN UNRECOGNIZED GLUED FLAG'S VALUE IS EMITTED, not dropped. This is the direction
// GluedFlagValue's own doc argues for and the reason is unchanged: the cost of testing
// one extra value is a candidate path that matches nothing, whereas the cost of missing
// one is this defect. It is also what keeps a flag NOBODY WROTE DOWN from being a hole,
// which is the shape every defect in this family has had. The containment for the false
// positives that would otherwise cause is the non-path tables being INVENTORIES rather
// than samples — `rg --path-separator=/` must not offer `/` as a candidate path — which
// is why both were completed from `--help` in the same change.
//
// cmd selects the flag vocabulary: "rg" additionally honors rgFlagsWithValue and
// rgFileFlags (see rgFlagsWithValue' doc for why the conflicting short flags are
// rg-only).
func SkipGrepPattern(cmd string, args []string) []string {
	isValueFlag := func(a string) bool {
		if grepFlagsWithValue[a] {
			return true
		}
		return cmd == "rg" && rgFlagsWithValue[a]
	}
	// A flag whose operand names a file the tool OPENS or a program it RUNS: either
	// way the operand is a candidate path and must be emitted, not skipped.
	isFileFlag := func(a string) bool {
		if grepFileFlags[a] || grepProgramFlags[a] {
			return true
		}
		return cmd == "rg" && (rgFileFlags[a] || rgProgramFlags[a])
	}
	// A patternSourceFlags flag supplies the pattern(s), so there is no positional
	// pattern to skip; every positional is a file. Both spellings count, and the scan
	// stops at a bare `--` because after it a token is an operand, not a flag.
	patternSkipped := false
	for _, a := range args {
		if a == "--" {
			break
		}
		name := a
		if n, ok := equalsFlagName(a); ok {
			name = n
		}
		if patternSourceFlags[name] {
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
		if name, glued := equalsFlagName(a); glued {
			// One token, judged by its flag name. A non-path value flag's value is
			// dropped as in the space form; anything else is a candidate path.
			if value, ok := GluedFlagValue(a); ok && (isFileFlag(name) || !isValueFlag(name)) {
				result = append(result, value)
			}
			i++
			continue
		}
		if isFileFlag(a) && i+1 < len(args) {
			// The operand is a path the tool OPENS, so it is emitted rather than left
			// to fall through as a positional where the pattern skip could eat it.
			result = append(result, args[i+1])
			i += 2
			continue
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

// jqValueFlags lists jq flags that consume TWO operands, BOTH of which are
// LITERALS jq never opens: `--arg name value` and `--argjson name value`. The
// value may LOOK like a path (`--arg dir "/app/src"`) but it is a jq variable
// binding, which is why it must not be tested as a filename (pg2-ia640.2).
//
// A FLAG WHOSE OPERAND THE COMMAND OPENS MUST NEVER APPEAR IN THIS TABLE — the
// same rule messageFlags' doc states. Removing such an operand deletes a REAL
// path from the candidate set before secretpath.IsSecret and the deny-list ever
// see it, which converts a `deny` into an `allow`. That is the pg2-wrxg6 defect,
// and it is the dangerous direction for a false-positive fix to fail in.
//
// SO `--slurpfile name file` AND `--rawfile name file` ARE NOT HERE (pg2-wrxg6).
// Their SECOND operand is a file jq reads. Their NAME operand still goes
// unscreened, but not by this table: with the flag left intact the NAME becomes
// jq's apparent first positional and is judged as the PROGRAM operand, exactly as
// `--argfile name file` already was. `--argfile` is the proof this is the right
// shape rather than a guess — it was in NEITHER table and measured `deny` on
// main @6737a0ea while its two siblings measured `allow`.
var jqValueFlags = map[string]bool{
	"--arg": true, "--argjson": true,
}

// jqOneArgFlags lists jq flags that consume ONE operand which is a LITERAL jq
// never opens. `--indent n` is the only one in jq 1.8.2.
//
// FOUR FLAGS WERE REMOVED FROM THIS TABLE (pg2-wrxg6), in two classes:
//
//   - `-f` and `--from-file` take THE FILTER FILE, which jq OPENS. They are
//     already known to safecmds' programOperandFromFlag, whose contract is that
//     when one is present there is no positional program and every positional is
//     a path — but that never fired, because this function had already deleted
//     both the flag and its path.
//   - `--tab`, `--join-output` and `--jsonargs` take NO OPERAND AT ALL. Listing a
//     boolean flag here makes it SWALLOW THE FOLLOWING TOKEN, so the token after
//     it escapes screening. `--join-output` is the clean proof: measured on
//     main @6737a0ea, `jq --join-output . <deny-listed>` returned `abstain` while
//     its own short form `jq -j . <deny-listed>` returned `reject` — the same flag,
//     two spellings, two verdicts, differing only by table membership. `--jsonargs`
//     was in BOTH tables and measured `approve`, against `--args`, which is in
//     neither and measured `reject`.
//
// Every other jq flag takes no operand and belongs in neither table. Note `-L` /
// `--library-path dir` DOES take an operand — a directory jq loads MODULES from —
// and it is deliberately absent here for the same reason as `-f`. Its operand
// escapes screening by a DIFFERENT route (it becomes jq's apparent first
// positional, which safecmds claims as the PROGRAM), so adding it here would be
// the wrong fix; it belongs in safecmds' programOperandValueFlags. Measured
// abstain rather than approve, and tracked as pg2-mu8zg.
var jqOneArgFlags = map[string]bool{
	"--indent": true,
}

// SkipJqValueFlags returns the args with the operands of jq's LITERAL-valued flags
// removed, so path checking is not handed a jq variable binding and told it is a
// filename. It MUST NOT remove an operand jq opens — see jqValueFlags.
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

// GluedFlagValue returns the VALUE half of an `--flag=value` token, and whether arg
// is one. It is equalsFlagName's counterpart: that returns the NAME, for deciding
// whether a table claims the flag; this returns the value, for testing what the
// command will actually open.
//
// IT EXISTS BECAUSE A ONE-TOKEN SPELLING HID A REAL PATH (pg2-cu3ro). A caller that
// skips any token beginning with `-` — which is the right instinct, since a flag NAME
// is not a filename — discards the VALUE along with the name when the two are glued
// by `=`. So the same file, named the same way, was screened in two spellings and
// auto-approved in a third. Measured on main @6737a0ea:
//
//	deny    git commit -F /Users/phillipg/.ssh/id_rsa
//	deny    git commit --file /Users/phillipg/.ssh/id_rsa
//	ALLOW   git commit --file=/Users/phillipg/.ssh/id_rsa
//	ALLOW   cat --file=/Users/phillipg/.ssh/id_rsa
//
// The empty value of a bare `--flag=` is reported as NOT present: there is nothing to
// test, and returning "" would make every such token a candidate path. A bare `-` and
// a bare `--` contain no `=` and so are not glued forms either.
//
// It deliberately accepts SHORT tokens too (`-o=v`), because the cost of testing one
// extra value is a path that does not match anything, whereas the cost of missing one
// is this defect. Callers that need the GNU long-option convention specifically can
// check the `--` prefix themselves.
func GluedFlagValue(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", false
	}
	_, value, found := strings.Cut(arg, "=")
	if !found || value == "" {
		return "", false
	}
	return value, true
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
