package cmddesc

// Schemas for the programs of the old safe-commands set that had no command
// schema (uniq, env, basename, ... and the shell builtins break, continue,
// return, type, set). Every flag below was read from the host's own `--help`
// (GNU coreutils 9.11 unless a schema says otherwise) or from bash's
// `help <builtin>`; each fact's citation lives in
// internal/embeddedspecs/data/<name>.json. A flag not listed is
// insufficient (UnknownFlagInsufficient), so it abstains.

func safecmdsNoSpecSchemas() []CommandSchema {
	return []CommandSchema{
		uniqSchema, envSchema, basenameSchema, dirnameSchema, realpathSchema,
		printenvSchema, idSchema, whoamiSchema, unameSchema,
		uptimeSchema, statSchema, base64Schema, pasteSchema, touchSchema,
		chmodSchema, mvSchema,
		breakSchema, continueSchema, returnSchema, typeSchema, setSchema,
	}
}

// uniqSchema: `uniq [OPTION]... [INPUT [OUTPUT]]`. The optional second
// operand is an OUTPUT file that is written, so it is a PathTruncate; the
// input is a read. Because one positional cannot be told from two without the
// count, the layout is: one operand = input; two operands = input then
// output (Trailing, via MinRest 0 cannot express that), so only the
// zero/one-operand forms are modeled and a second operand abstains.
var uniqSchema = CommandSchema{
	Name:       "uniq",
	Provenance: "uniq (GNU coreutils) 9.11, uniq --help",
	Flags: map[string]FlagSpec{
		"-c": inert, "--count": inert,
		"-d": inert, "--repeated": inert,
		"-D":             inert,
		"--all-repeated": literalOpt,
		"-f":             literal1, "--skip-fields": literal1,
		"--group": literalOpt,
		"-i":      inert, "--ignore-case": inert,
		"-s": literal1, "--skip-chars": literal1,
		"-u": inert, "--unique": inert,
		"-z": inert, "--zero-terminated": inert,
		"-w": literal1, "--check-chars": literal1,
	},
	Positionals:  PositionalSpec{Leading: []OperandRole{PathRead}, LeadingOptional: true, Rest: Unmodeled, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// envSchema: `env [OPTION]... [-] [NAME=VALUE]... [COMMAND [ARG]...]`. Only
// the print-the-environment form is modeled (no NAME=VALUE, no COMMAND): every
// positional is Unmodeled, so the run-a-command and set-a-variable forms
// abstain. -C/--chdir, -S/--split-string, -a/--argv0 and the signal flags are
// not modeled.
var envSchema = CommandSchema{
	Name:       "env",
	Provenance: "env (GNU coreutils) 9.11, env --help",
	Flags: map[string]FlagSpec{
		"-0": inert, "--null": inert,
		"-i": inert, "--ignore-environment": inert,
		"-u": literal1, "--unset": literal1,
		"-v": inert, "--debug": inert,
		"--list-signal-handling": inert,
	},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// basenameSchema: `basename NAME [SUFFIX]` / `basename OPTION... NAME...`;
// pure string manipulation, no file is touched.
var basenameSchema = CommandSchema{
	Name:       "basename",
	Provenance: "basename (GNU coreutils) 9.11, basename --help",
	Flags: map[string]FlagSpec{
		"-a": inert, "--multiple": inert,
		"-s": literal1, "--suffix": literal1,
		"-z": inert, "--zero": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// dirnameSchema: `dirname [OPTION] NAME...`; pure string manipulation.
var dirnameSchema = CommandSchema{
	Name:         "dirname",
	Provenance:   "dirname (GNU coreutils) 9.11, dirname --help",
	Flags:        map[string]FlagSpec{"-z": inert, "--zero": inert},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// realpathSchema: `realpath [OPTION]... FILE...`; resolves names (lstat), never
// reads content, so the operands are PathRead with a metadata stdout, like
// readlink. --relative-to/--relative-base take a directory used only for
// string arithmetic.
var realpathSchema = CommandSchema{
	Name:       "realpath",
	Provenance: "realpath (GNU coreutils) 9.11, realpath --help",
	Flags: map[string]FlagSpec{
		"-E": inert, "--canonicalize": inert,
		"-e": inert, "--canonicalize-existing": inert,
		"-m": inert, "--canonicalize-missing": inert,
		"-L": inert, "--logical": inert,
		"-P": inert, "--physical": inert,
		"-q": inert, "--quiet": inert,
		"--relative-to":   {Arity: ArityOne, Operand: PathRead},
		"--relative-base": {Arity: ArityOne, Operand: PathRead},
		"-s":              inert, "--strip": inert, "--no-symlinks": inert,
		"-z": inert, "--zero": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// printenvSchema: `printenv [OPTION] [VARIABLE]...`; prints environment values.
var printenvSchema = CommandSchema{
	Name:         "printenv",
	Provenance:   "printenv (GNU coreutils) 9.11, printenv --help",
	Flags:        map[string]FlagSpec{"-0": inert, "--null": inert},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// idSchema: `id [OPTION]... [USER]...`; prints user and group identity.
var idSchema = CommandSchema{
	Name:       "id",
	Provenance: "id (GNU coreutils) 9.11, id --help",
	Flags: map[string]FlagSpec{
		"-a": inert,
		"-Z": inert, "--context": inert,
		"-g": inert, "--group": inert,
		"-G": inert, "--groups": inert,
		"-n": inert, "--name": inert,
		"-r": inert, "--real": inert,
		"-u": inert, "--user": inert,
		"-z": inert, "--zero": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// whoamiSchema: `whoami [OPTION]...`; no operands.
var whoamiSchema = CommandSchema{
	Name:         "whoami",
	Provenance:   "whoami (GNU coreutils) 9.11, whoami --help",
	Flags:        map[string]FlagSpec{},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// unameSchema: `uname [OPTION]...`; prints system information, no operands.
var unameSchema = CommandSchema{
	Name:       "uname",
	Provenance: "uname (GNU coreutils) 9.11, uname --help",
	Flags: map[string]FlagSpec{
		"-a": inert, "--all": inert,
		"-s": inert, "--kernel-name": inert,
		"-n": inert, "--nodename": inert,
		"-r": inert, "--kernel-release": inert,
		"-v": inert, "--kernel-version": inert,
		"-m": inert, "--machine": inert,
		"-p": inert, "--processor": inert,
		"-i": inert, "--hardware-platform": inert,
		"-o": inert, "--operating-system": inert,
	},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// uptimeSchema: `uptime [OPTION]... [FILE]`; reads a utmp-format file.
var uptimeSchema = CommandSchema{
	Name:         "uptime",
	Provenance:   "uptime (GNU coreutils) 9.11, uptime --help",
	Flags:        map[string]FlagSpec{},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// statSchema: `stat [OPTION]... FILE...`; file metadata only.
var statSchema = CommandSchema{
	Name:       "stat",
	Provenance: "stat (GNU coreutils) 9.11, stat --help",
	Flags: map[string]FlagSpec{
		"-L": inert, "--dereference": inert,
		"-f": inert, "--file-system": inert,
		"--cached": literal1,
		"-c":       literal1, "--format": literal1,
		"--printf": literal1,
		"-t":       inert, "--terse": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// base64Schema: `base64 [OPTION]... [FILE]`; a content reader of FILE or stdin.
var base64Schema = CommandSchema{
	Name:       "base64",
	Provenance: "base64 (GNU coreutils) 9.11, base64 --help",
	Flags: map[string]FlagSpec{
		"-d": inert, "--decode": inert,
		"-i": inert, "--ignore-garbage": inert,
		"-w": literal1, "--wrap": literal1,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// pasteSchema: `paste [OPTION]... [FILE]...`; a content reader of its files.
var pasteSchema = CommandSchema{
	Name:       "paste",
	Provenance: "paste (GNU coreutils) 9.11, paste --help",
	Flags: map[string]FlagSpec{
		"-d": literal1, "--delimiters": literal1,
		"-s": inert, "--serial": inert,
		"-z": inert, "--zero-terminated": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// touchSchema: `touch [OPTION]... FILE...`; creates FILE when absent (a create
// unless -c), otherwise updates its times (a modify). It is modeled as
// PathCreate, the same role mkdir uses; -r/--reference reads a file whose
// times are copied.
var touchSchema = CommandSchema{
	Name:       "touch",
	Provenance: "touch (GNU coreutils) 9.11, touch --help",
	Flags: map[string]FlagSpec{
		"-a": inert,
		"-c": inert, "--no-create": inert,
		"-d": literal1, "--date": literal1,
		"-f": inert,
		"-h": inert, "--no-dereference": inert,
		"-m": inert,
		"-r": {Arity: ArityOne, Operand: PathRead}, "--reference": {Arity: ArityOne, Operand: PathRead},
		"-t":     literal1,
		"--time": literal1,
	},
	Positionals:  PositionalSpec{Rest: PathCreate, MinRest: 1},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// chmodSchema: `chmod [OPTION]... MODE[,MODE]... FILE...` or with
// --reference=RFILE (which replaces the MODE operand). Each FILE is modified.
var chmodSchema = CommandSchema{
	Name:       "chmod",
	Provenance: "chmod (GNU coreutils) 9.11, chmod --help",
	Flags: map[string]FlagSpec{
		"-c": inert, "--changes": inert,
		"-f": inert, "--silent": inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
		"--dereference": inert,
		"-h":            inert, "--no-dereference": inert,
		"--no-preserve-root": inert, "--preserve-root": inert,
		"--reference": {Arity: ArityOne, Operand: PathRead},
		"-R":          inert, "--recursive": inert,
		"-H": inert, "-L": inert, "-P": inert,
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Literal},
		LeadingSkippedByFlags: []string{"--reference"},
		Rest:                  PathModify,
		MinRest:               1,
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// mvSchema: sources are removed from their place (PathDelete) and the LAST
// positional is the destination (truncated), unless -t named a target
// directory, in which case that directory is modified. At least one source is
// required. --backup/-b/-S (backup files), --exchange, --no-copy, --update/-u,
// -Z/--context and --debug are not modeled and abstain.
var mvSchema = CommandSchema{
	Name:       "mv",
	Provenance: "mv (GNU coreutils) 9.11, mv --help",
	Flags: map[string]FlagSpec{
		"-f": inert, "--force": inert,
		"-i": inert, "--interactive": inert,
		"-n": {Transform: EffectTransform{Kind: TransformNoClobber}}, "--no-clobber": {Transform: EffectTransform{Kind: TransformNoClobber}},
		"--strip-trailing-slashes": inert,
		"-t":                       {Arity: ArityOne, Operand: PathModify}, "--target-directory": {Arity: ArityOne, Operand: PathModify},
		"-T": inert, "--no-target-directory": inert,
		"-v": inert, "--verbose": inert,
	},
	Positionals: PositionalSpec{
		Rest:                   PathDelete,
		MinRest:                1,
		Trailing:               []OperandRole{PathTruncate},
		TrailingSkippedByFlags: []string{"-t", "--target-directory"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// breakSchema, continueSchema, returnSchema: shell control-flow builtins whose
// only operand is a count or exit status.
var (
	breakSchema    = shellControlSchema("break")
	continueSchema = shellControlSchema("continue")
	returnSchema   = shellControlSchema("return")
)

func shellControlSchema(name string) CommandSchema {
	return CommandSchema{
		Name:         name,
		Provenance:   "bash builtin, bash -c 'help " + name + "'",
		Flags:        map[string]FlagSpec{},
		Positionals:  PositionalSpec{Rest: Literal},
		Stdin:        StdinNever,
		Stdout:       StdoutNone,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
}

// typeSchema: the bash builtin `type [-afptP] name...`; describes how each name
// resolves and runs nothing, like which/command -v.
var typeSchema = CommandSchema{
	Name:       "type",
	Provenance: "bash builtin, bash -c 'help type'",
	Flags: map[string]FlagSpec{
		"-a": inert, "-f": inert, "-p": inert, "-P": inert, "-t": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// setSchema: the bash builtin `set`, only its shell-option flags: -e -u -x -f
// -v -E -T -B -H -C -h -m -b -t -P -k (inert), and `-o OPTION` / `+o OPTION`
// are NOT modeled beyond -o taking one option name. A positional operand
// (`set -- a b c`, `set VAR`) assigns shell parameters and abstains.
var setSchema = CommandSchema{
	Name:       "set",
	Provenance: "bash builtin, bash -c 'help set'",
	Flags: map[string]FlagSpec{
		"-e": inert, "-u": inert, "-x": inert, "-f": inert, "-v": inert,
		"-E": inert, "-T": inert, "-B": inert, "-H": inert, "-C": inert,
		"-o": literal1,
	},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}
