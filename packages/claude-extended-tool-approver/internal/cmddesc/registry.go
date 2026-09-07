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

// DefaultRegistry returns the spike's registry as plain values. head and rm
// are the proof that another command is ONLY a registry entry.
func DefaultRegistry() Registry {
	return NewRegistry(catSchema, headSchema, sedSchema, rmSchema, cpSchema)
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
