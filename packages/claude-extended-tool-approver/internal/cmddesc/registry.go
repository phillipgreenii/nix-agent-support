package cmddesc

import "sort"

// Registry maps a command basename to its schema value. It is the ONLY place
// a command name appears in the spike: adding a command is adding an entry.
type Registry struct {
	schemas map[string]CommandSchema
}

// NewRegistry builds a registry from schema values, keyed by Name.
func NewRegistry(schemas ...CommandSchema) Registry {
	r := Registry{schemas: make(map[string]CommandSchema, len(schemas))}
	for _, s := range schemas {
		r.schemas[s.Name] = s
	}
	return r
}

// Lookup returns the schema for a basename.
func (r Registry) Lookup(basename string) (CommandSchema, bool) {
	s, ok := r.schemas[basename]
	return s, ok
}

// Names lists the registered basenames, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r.schemas))
	for n := range r.schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DefaultRegistry returns the spike's registry as plain values. head, rm and
// tee are the proof that another command is ONLY a registry entry; sh is the
// proof that a second NAME for the same semantics is only a second key.
func DefaultRegistry() Registry {
	return NewRegistry(
		catSchema, headSchema, sedSchema, rmSchema, cpSchema, teeSchema,
		bashSchema, renamed(bashSchema, "sh"),
		xargsSchema, curlSchema,
	)
}

// renamed returns a copy of s registered under another basename. The Flags
// map is shared, which is fine: schemas are read-only values.
func renamed(s CommandSchema, name string) CommandSchema {
	s.Name = name
	return s
}

// Schema-author shorthands for the common flag shapes.
var (
	// inert is the arity-0, transform-free flag spec.
	inert = FlagSpec{}
	// literal1 takes one inert value.
	literal1 = FlagSpec{Arity: ArityOne, Operand: Literal}
	// literalOpt takes an inert value only when glued (`--backup[=CONTROL]`).
	literalOpt = FlagSpec{Arity: ArityOptionalGlued, Operand: Literal}
)

var catSchema = CommandSchema{
	Name:       "cat",
	Provenance: "GNU coreutils 9.x cat --help",
	Flags: map[string]FlagSpec{
		"-n": inert, "--number": inert,
		"-b": inert, "--number-nonblank": inert,
		"-A": inert, "--show-all": inert,
		"-E": inert, "--show-ends": inert,
		"-T": inert, "--show-tabs": inert,
		"-s": inert, "--squeeze-blank": inert,
		"-v": inert, "--show-nonprinting": inert,
		"-e": inert, "-t": inert, "-u": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var headSchema = CommandSchema{
	Name:       "head",
	Provenance: "GNU coreutils 9.x head --help",
	Flags: map[string]FlagSpec{
		"-n": {Arity: 1, Operand: Literal}, "--lines": {Arity: 1, Operand: Literal},
		"-c": {Arity: 1, Operand: Literal}, "--bytes": {Arity: 1, Operand: Literal},
		"-q": inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// sedSchema: the first positional is the program unless -e/-f supplied one;
// the rest are input files, which -i upgrades from read to modify. sed has no
// `-` stdin token (a file named `-` is a file), so stdin is the
// no-path-operands rule alone.
var sedSchema = CommandSchema{
	Name:       "sed",
	Provenance: "sed (GNU sed) 4.10, sed --help",
	Flags: map[string]FlagSpec{
		"-n": inert, "--quiet": inert, "--silent": inert,
		"-E": inert, "-r": inert, "--regexp-extended": inert,
		"-s": inert, "--separate": inert,
		"-z": inert, "--null-data": inert,
		"-u": inert, "--unbuffered": inert,
		"--posix": inert, "--debug": inert, "--sandbox": inert,
		"--follow-symlinks": inert,
		"-e":                {Arity: ArityOne, Operand: Program("sed")}, "--expression": {Arity: ArityOne, Operand: Program("sed")},
		"-f": {Arity: ArityOne, Operand: PathRead}, "--file": {Arity: ArityOne, Operand: PathRead},
		"-i":         {Arity: ArityOptionalGlued, Operand: Literal, Transform: EffectTransform{Kind: TransformInPlace}},
		"--in-place": {Arity: ArityOptionalGlued, Operand: Literal, Transform: EffectTransform{Kind: TransformInPlace}},
		"-l":         literal1, "--line-length": literal1,
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Program("sed")},
		LeadingSkippedByFlags: []string{"-e", "--expression", "-f", "--file"},
		Rest:                  PathRead,
	},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// rmSchema: every positional is deleted. `--preserve-root[=all]` is
// optional-glued per --help (its value is inert either way).
var rmSchema = CommandSchema{
	Name:       "rm",
	Provenance: "rm (GNU coreutils) 9.11, rm --help",
	Flags: map[string]FlagSpec{
		"-f": inert, "--force": inert,
		"-i": inert, "-I": inert,
		"--interactive": literalOpt,
		"-r":            inert, "-R": inert, "--recursive": inert,
		"-d": inert, "--dir": inert,
		"-v": inert, "--verbose": inert,
		"--one-file-system":  inert,
		"--preserve-root":    literalOpt,
		"--no-preserve-root": inert,
	},
	Positionals:  PositionalSpec{Rest: PathDelete},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// cpSchema: sources are read and the LAST positional is the destination
// (truncated, or created under -n) unless -t named a target directory, in
// which case that directory is modified. At least one source is required.
// Flags on this host's --help that change WHAT is written (-l/-s link instead
// of copy, -b and --backup+-S naming a backup file, --remove-destination,
// --attributes-only, --reflink, --sparse, -d, -Z, --context, --copy-contents,
// --keep-directory-symlink, --strip-trailing-slashes, --no-preserve, --debug)
// are left unmodeled beyond what the brief named, so they abstain.
var cpSchema = CommandSchema{
	Name:       "cp",
	Provenance: "cp (GNU coreutils) 9.11, cp --help",
	Flags: map[string]FlagSpec{
		"-r": inert, "-R": inert, "--recursive": inert,
		"-a": inert, "--archive": inert,
		"-p": inert,
		"-v": inert, "--verbose": inert,
		"-f": inert, "--force": inert,
		"-i": inert, "--interactive": inert,
		"-u": inert,
		"-L": inert, "--dereference": inert,
		"-P": inert, "--no-dereference": inert,
		"-H":        inert,
		"-x":        inert,
		"--parents": inert,
		"-n":        {Transform: EffectTransform{Kind: TransformNoClobber}}, "--no-clobber": {Transform: EffectTransform{Kind: TransformNoClobber}},
		"-t": {Arity: ArityOne, Operand: PathModify}, "--target-directory": {Arity: ArityOne, Operand: PathModify},
		"-T": inert, "--no-target-directory": inert,
		"-S": literal1, "--suffix": literal1,
		"--backup":   literalOpt,
		"--preserve": literalOpt,
		"--update":   literalOpt,
	},
	Positionals: PositionalSpec{
		Rest:                   PathRead,
		MinRest:                1,
		Trailing:               []OperandRole{PathTruncate},
		TrailingSkippedByFlags: []string{"-t", "--target-directory"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// teeSchema: stdin is copied to stdout and to every file operand, which is
// truncated — or appended to (modify) under -a.
var teeSchema = CommandSchema{
	Name:       "tee",
	Provenance: "tee (GNU coreutils) 9.11, tee --help",
	Flags: map[string]FlagSpec{
		"-a": {Transform: EffectTransform{Kind: TransformAppend}}, "--append": {Transform: EffectTransform{Kind: TransformAppend}},
		"-i": inert, "--ignore-interrupts": inert,
		"-p":             inert,
		"--output-error": literalOpt,
	},
	Positionals:  PositionalSpec{Rest: PathTruncate},
	Stdin:        StdinAlways,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// bashSchema (also registered as sh): `-c` carries a shell program that the
// shell dialect hands back for recursion; the positionals after it are $0,
// $1... (inert). WITHOUT `-c` the first positional is a SCRIPT FILE that will
// be executed — role Program("shell-file"), a dialect with no interpreter, so
// it is insufficient: we cannot read the file, and Abstain is the honest
// answer. Options end at the first positional (a later `-x` belongs to the
// script). Stdin is StdinNever for the modeled forms; the no-`-c`, no-file
// form reads a script from stdin, which resolveRoles already makes
// insufficient (too few positionals) — an accepted gap, not a modeled stdin
// read. `-n` (noexec) is deliberately left out so it abstains. `+O` is listed
// for completeness but the generic scanner only recognises `-`-prefixed
// tokens, so it currently lands as a positional (inert under -c; the
// script-file slot otherwise — insufficient either way).
var bashSchema = CommandSchema{
	Name:       "bash",
	Provenance: "GNU bash 5.3.9, bash --help / help set",
	Flags: map[string]FlagSpec{
		"-c": {Arity: ArityOne, Operand: Program("shell")},
		"-x": inert, "-e": inert, "-u": inert, "-v": inert,
		"-l": inert, "--login": inert,
		"-i":          inert,
		"--norc":      inert,
		"--noprofile": inert,
		"--posix":     inert,
		"-o":          literal1,
		"-O":          literal1, "+O": literal1,
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Program("shell-file")},
		LeadingSkippedByFlags: []string{"-c"},
		Rest:                  Literal,
	},
	Stdin:                 StdinNever,
	Stdout:                StdoutNone,
	UnknownFlag:           UnknownFlagInsufficient,
	EndOfOptions:          true,
	PositionalsEndOptions: true,
}

// xargsSchema: the positionals are the argv of the command xargs runs, which
// only the xargs interpreter can give meaning to (Rest is Literal for the
// generic layer). Options end at the first positional (GNU getopt `+`: a
// later `-f` belongs to the child). Stdin is Always even under `-a FILE`,
// which actually replaces it — an over-report in the fail-closed direction.
// `-p`/`--interactive` and `--process-slot-var` are left out (abstain). Host
// spellings differ from the brief: `-E END` is short-only and `-e`/`--eof`
// take an OPTIONAL glued value.
var xargsSchema = CommandSchema{
	Name:        "xargs",
	Provenance:  "xargs (GNU findutils) 4.10.0, xargs --help",
	Interpreter: "xargs",
	Flags: map[string]FlagSpec{
		"-0": inert, "--null": inert,
		"-r": inert, "--no-run-if-empty": inert,
		"-t": inert, "--verbose": inert,
		"-x": inert, "--exit": inert,
		"-o": inert, "--open-tty": inert,
		"-n": literal1, "--max-args": literal1,
		"-L": literal1, "--max-lines": literal1,
		"-l": literalOpt,
		"-P": literal1, "--max-procs": literal1,
		"-s": literal1, "--max-chars": literal1,
		"-d": literal1, "--delimiter": literal1,
		"-E": literal1,
		"-e": literalOpt, "--eof": literalOpt,
		"-I": literal1,
		"-i": literalOpt, "--replace": literalOpt,
		"-a": {Arity: ArityOne, Operand: PathRead}, "--arg-file": {Arity: ArityOne, Operand: PathRead},
	},
	Positionals:           PositionalSpec{Rest: Literal},
	Stdin:                 StdinAlways,
	Stdout:                StdoutNone,
	UnknownFlag:           UnknownFlagInsufficient,
	EndOfOptions:          true,
	PositionalsEndOptions: true,
}

// curlSchema: URLs are positionals (or --url values) that the curl
// interpreter turns into net effects; the data flags use the generic
// data-or-@file convention; -o truncates, -T reads. Deliberately left out so
// they abstain: -k/--insecure, -u/--user, -O/--remote-name, -b/-c cookies,
// -K/--config, -x/--proxy, and -H's own `@file` form (modeled as a literal).
// `--include` is the pre-8.10 spelling of `--show-headers`; this host still
// accepts it.
var curlSchema = CommandSchema{
	Name:        "curl",
	Provenance:  "curl 8.21.0, curl --help all",
	Interpreter: "curl",
	Flags: map[string]FlagSpec{
		"-s": inert, "--silent": inert,
		"-S": inert, "--show-error": inert,
		"-L": inert, "--location": inert,
		"-f": inert, "--fail": inert,
		"-i": inert, "--include": inert, "--show-headers": inert,
		"-I": inert, "--head": inert,
		"-v": inert, "--verbose": inert,
		"-G": inert, "--get": inert,
		"--compressed": inert,
		"-N":           inert, "--no-buffer": inert,
		"-o": {Arity: ArityOne, Operand: PathTruncate}, "--output": {Arity: ArityOne, Operand: PathTruncate},
		"-H": literal1, "--header": literal1,
		"-A": literal1, "--user-agent": literal1,
		"-X": literal1, "--request": literal1,
		"-m": literal1, "--max-time": literal1,
		"--connect-timeout": literal1,
		"--retry":           literal1,
		"-w":                literal1, "--write-out": literal1,
		"-d": {Arity: ArityOne, Operand: DataOrAtFile}, "--data": {Arity: ArityOne, Operand: DataOrAtFile},
		"--data-binary":    {Arity: ArityOne, Operand: DataOrAtFile},
		"--data-raw":       {Arity: ArityOne, Operand: DataOrAtFile},
		"--data-urlencode": {Arity: ArityOne, Operand: DataOrAtFile},
		"-F":               {Arity: ArityOne, Operand: DataOrAtFile}, "--form": {Arity: ArityOne, Operand: DataOrAtFile},
		"-T": {Arity: ArityOne, Operand: PathRead}, "--upload-file": {Arity: ArityOne, Operand: PathRead},
		"--url": literal1,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: false,
}
