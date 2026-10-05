package cmddesc

// pg2-slsc0: the small read-only system probes the agent-support plugins'
// commands and skills instruct but that had no schema (so every form
// abstained): `readlink -f` and `diff A B` (pb /drain-beads' store-served
// staleness check), `date -u +FORMAT` / `date +%F` (pb's marker and note
// timestamps, beads-lifecycle, drain-stuck), and `command -v NAME` (pb-gate-lifecycle's "is pb on PATH" probe).
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim): "any
// command which is supposed to work as part of a skill should be
// autoapproved". All four are READ-ONLY and are pure schema FACTS (a path
// read, a metadata or content stdout): none adds an effectpolicy judgment, so
// gate 5 of ceta-spec-gen (pg2-xu7sz / tc-o14i5.3.10) does not apply. Flags no
// skill instructs and whose effect this model cannot see stay unmodeled
// (UnknownFlagInsufficient) and abstain.
//
// readlink / date are GNU coreutils 9.11 and diff is GNU diffutils 3.12
// (the nixpkgs versions the spec-help-drift check puts on PATH); every flag
// below was read from that binary's `--help` and is cited per fact in
// internal/embeddedspecs/data/{readlink,date,diff,command}.json.

// readlinkSchema: `readlink [-f|-e|-m] [-n] [-q|-s] [-v] [-z] FILE...`.
// Prints a symlink's target or a canonical name; it never reads file CONTENT
// (lstat / realpath only), so the operand is a PathRead with a METADATA stdout,
// the same model ls uses for a directory listing.
var readlinkSchema = CommandSchema{
	Name:       "readlink",
	Provenance: "readlink (GNU coreutils) 9.11, readlink --help",
	Flags: map[string]FlagSpec{
		"-f": inert, "--canonicalize": inert,
		"-e": inert, "--canonicalize-existing": inert,
		"-m": inert, "--canonicalize-missing": inert,
		"-n": inert, "--no-newline": inert,
		"-q": inert, "--quiet": inert,
		"-s": inert, "--silent": inert,
		"-v": inert, "--verbose": inert,
		"-z": inert, "--zero": inert,
		"--help": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// diffSchema: `diff [OPTION]... FILES` compares two files (or directories) and
// writes only to stdout. Every modeled option selects an output format, a
// comparison mode or ignore rule; the operands are PathRead (`-` is stdin).
// Deliberately UNMODELED, so they abstain: `-l`/`--paginate` (pipes the output
// through `pr`, an executed child), the `--*-format` / `-D`/`--ifdef` printf
// family and `--palette` (format strings this model does not vet). The
// optional-argument spellings `--context[=NUM]`, `--unified[=NUM]` and
// `--color[=WHEN]` are modeled bare or glued (literalOpt).
var diffSchema = CommandSchema{
	Name:       "diff",
	Provenance: "diff (GNU diffutils) 3.12, diff --help",
	Flags: map[string]FlagSpec{
		"--normal": inert,
		"-q":       inert, "--brief": inert,
		"-s": inert, "--report-identical-files": inert,
		"-c": inert, "-C": literal1, "--context": literalOpt,
		"-u": inert, "-U": literal1, "--unified": literalOpt,
		"-e": inert, "--ed": inert,
		"-n": inert, "--rcs": inert,
		"-y": inert, "--side-by-side": inert,
		"-W": literal1, "--width": literal1,
		"--left-column": inert, "--suppress-common-lines": inert,
		"-p": inert, "--show-c-function": inert,
		"-F": literal1, "--show-function-line": literal1,
		"--label": literal1,
		"-t":      inert, "--expand-tabs": inert,
		"-T": inert, "--initial-tab": inert,
		"--tabsize": literal1, "--suppress-blank-empty": inert,
		"-r": inert, "--recursive": inert,
		"--no-dereference": inert,
		"-N":               inert, "--new-file": inert,
		"--unidirectional-new-file": inert,
		"--ignore-file-name-case":   inert, "--no-ignore-file-name-case": inert,
		"-x": literal1, "--exclude": literal1,
		"-X": {Arity: ArityOne, Operand: PathRead}, "--exclude-from": {Arity: ArityOne, Operand: PathRead},
		"-S": {Arity: ArityOne, Operand: PathRead}, "--starting-file": {Arity: ArityOne, Operand: PathRead},
		"--from-file": {Arity: ArityOne, Operand: PathRead}, "--to-file": {Arity: ArityOne, Operand: PathRead},
		"-i": inert, "--ignore-case": inert,
		"-E": inert, "--ignore-tab-expansion": inert,
		"-Z": inert, "--ignore-trailing-space": inert,
		"-b": inert, "--ignore-space-change": inert,
		"-w": inert, "--ignore-all-space": inert,
		"-B": inert, "--ignore-blank-lines": inert,
		"-I": literal1, "--ignore-matching-lines": literal1,
		"-a": inert, "--text": inert,
		"--strip-trailing-cr": inert,
		"-d":                  inert, "--minimal": inert,
		"--horizon-lines": literal1, "--speed-large-files": inert,
		"--color": literalOpt,
		"--help":  inert, "-v": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// dateSchema: `date [OPTION]... [+FORMAT]`. Prints the (or a described) time.
// `-s`/`--set` is deliberately UNMODELED (it sets the system clock) so it
// abstains. KNOWN, DOCUMENTED imprecision: the second synopsis,
// `date MMDDhhmm[[CC]YY][.ss]`, also SETS the clock, and this model has no
// positional-prefix role to tell it from `+FORMAT`, so a bare positional is a
// Literal. That form needs privilege (an unprivileged `date 10051200` fails
// "Operation not permitted") and `sudo` is not unwrapped to approve, so it is
// accepted as strictly no weaker than the old engine's blanket safecmds `date`.
var dateSchema = CommandSchema{
	Name:       "date",
	Provenance: "date (GNU coreutils) 9.11, date --help",
	Flags: map[string]FlagSpec{
		"-d": literal1, "--date": literal1,
		"--debug": inert,
		"-f":      {Arity: ArityOne, Operand: PathRead}, "--file": {Arity: ArityOne, Operand: PathRead},
		"-I": literalOpt, "--iso-8601": literalOpt,
		"--resolution": inert,
		"-R":           inert, "--rfc-email": inert,
		"--rfc-3339": literal1,
		"-r":         {Arity: ArityOne, Operand: PathRead}, "--reference": {Arity: ArityOne, Operand: PathRead},
		"-u": inert, "--utc": inert, "--universal": inert,
		"--help": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// commandSchema: the bash builtin `command`. ONLY the lookup forms
// `command -v NAME` / `command -V NAME` are modeled; they print how NAME
// resolves and execute nothing (the same PATH lookup `which` does). The
// builtin's real purpose, `command NAME ARGS...`, RUNS NAME; cmdparse already
// unwraps that spelling to a leaf for NAME (judged as NAME itself, never
// reaching this schema), and it leaves the -v/-V forms intact. -v/-V therefore
// take the looked-up NAME as their ONE operand (literal1) and the schema
// declares NO positional role (Rest Unmodeled), so `command -v a b` (b is a
// stray positional), `command -v` and `command -p -v pb` (-p is unregistered)
// are insufficient and no run-something spelling can be approved by name.
// `command` is a shell builtin with no on-PATH binary, so it is
// specdrift.Exempt (like cd / export) and its spec cites bash's `help command`.
var commandSchema = CommandSchema{
	Name:       "command",
	Provenance: "bash builtin, bash -c 'help command'",
	Flags: map[string]FlagSpec{
		"-v": literal1,
		"-V": literal1,
	},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: false,
}

func pluginProbeSchemas() []CommandSchema {
	return []CommandSchema{readlinkSchema, diffSchema, dateSchema, commandSchema}
}
