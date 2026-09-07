package cmddesc

// Slice 3n registry breadth (tc-q9ak item 3, "no decision needed"): the
// schemas below are DATA verified against this host's tools; the only code
// this slice added is ArityN (jq's two-operand flags) and
// CommandSchema.DefaultSubcommand (yq's implicit `eval`). Ordered by corpus
// frequency: bd (11,077 rows, the second most common unregistered basename
// after cd), jq (7,144), ps (869), sleep (858), which (781), gofmt (594),
// yq (587), pgrep (570). prettier is NOT here: it is not installed on this
// host, so there is no `--help` to verify a schema against (the registry's
// Provenance rule).

// ---- bd (beads) -----------------------------------------------------------

// bdSchema: subcommand dispatch. Every subcommand touches ONE remote
// resource — the beads database on the remote Dolt server
// (.claude/rules/beads-remote-server.md) — modeled as an implicit
// EffectRemote{Resource: "beads"} whose Operation is "read" for the
// listing/showing verbs (Permitted by RemoteMutation's data), "mutate" for
// anything that writes an issue (Unknown: needs consent, exactly like a git
// push), and "dolt-server" for the Dolt server lifecycle verbs, which are
// Forbidden: this machine forbids starting or stopping a Dolt server
// (~/.claude/CLAUDE.md "Beads / Dolt: no rogue auto-start"; the
// beads-remote-server rule's "Never run bd dolt start"). `bd dolt show /
// status / test` are READS of the connection configuration — the
// beads-remote-server rule itself tells an agent to run `bd dolt show` when
// a connection fails — so they are "read", not "dolt-server"; the resume
// bead's shorthand "dolt * => Forbidden" is refined to the lifecycle verbs
// only and that refinement is flagged on tc-q9ak.
//
// Read subcommands use UnknownFlagInert: their flags are filters and output
// formats (--json, --status, --label, -n) and cannot add an effect. Mutation
// subcommands are Unknown whatever their flags, so they are inert too — a
// flag cannot make a consent-requiring write need MORE than consent. An
// unlisted subcommand (`bd config`, `bd epic`, ...) is an unmodeled
// subcommand => Abstain. Global flags: `-C`/`--directory` (chdir) and
// `--profile` (writes a profile file) are deliberately unmodeled. Verified
// against this host's `bd --help` (beads with Dolt backend).
var bdSchema = CommandSchema{
	Name:       "bd",
	Provenance: "bd --help (beads, Dolt backend, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"--actor":            literal1,
		"--db":               {Arity: ArityOne, Operand: PathRead},
		"--dolt-auto-commit": literal1,
		"--global":           inert,
		"--json":             inert,
		"-q":                 inert, "--quiet": inert,
		"--readonly": inert,
		"--sandbox":  inert,
		"-v":         inert, "--verbose": inert,
		"--ignore-schema-skew": inert,
		"-V":                   inert, "--version": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands:  bdSubcommands(),
}

// bdSubcommands builds the dispatch table from three verb lists so the
// classification of every verb is visible in one place.
func bdSubcommands() map[string]CommandSchema {
	m := map[string]CommandSchema{}
	for _, v := range []string{
		"list", "show", "ready", "search", "count", "query", "children", "comments",
		"graph", "history", "stale", "status", "statuses", "types", "info", "where",
		"context", "human", "quickstart", "prime", "onboard", "memories", "recall",
		"state", "lint", "export",
	} {
		m[v] = bdRemote(v, "read")
	}
	for _, v := range []string{
		"create", "update", "close", "reopen", "assign", "comment", "note", "delete",
		"priority", "tag", "link", "promote", "q", "set-state", "todo", "duplicate",
		"duplicates", "supersede", "swarm", "batch", "import", "backup", "restore",
		"compact", "flatten", "gc", "doctor", "bootstrap", "init", "hooks", "forget",
		"remember", "setup", "kv", "merge-slot", "gate", "branch", "federation", "vc",
		"edit", "create-form", "epic",
	} {
		m[v] = bdRemote(v, "mutate")
	}
	m["dep"] = bdNested("dep", map[string]string{
		"list": "read", "tree": "read", "cycles": "read",
		"add": "mutate", "remove": "mutate", "relate": "mutate", "unrelate": "mutate",
	})
	m["label"] = bdNested("label", map[string]string{
		"list": "read", "list-all": "read",
		"add": "mutate", "remove": "mutate", "propagate": "mutate",
	})
	m["dolt"] = bdNested("dolt", map[string]string{
		"show": "read", "status": "read", "test": "read",
		"start": "dolt-server", "stop": "dolt-server", "killall": "dolt-server",
		"commit": "mutate", "push": "mutate", "pull": "mutate", "remote": "mutate",
		"set": "mutate", "clean-databases": "mutate",
	})
	return m
}

// bdRemote is one bd verb: every positional is a Literal (an issue id, a
// query, a title), and the verb's whole effect is the implicit remote
// operation on the beads database.
func bdRemote(name, operation string) CommandSchema {
	stdout := StdoutMetadata
	if operation == "read" {
		stdout = StdoutContent // issue text flows: `bd show x | curl -d @-` must reach the flow policy
	}
	target := "beads"
	if operation == "dolt-server" {
		target = "dolt"
	}
	return CommandSchema{
		Name:       name,
		Provenance: "bd " + name + " --help",
		Flags:      map[string]FlagSpec{},
		Positionals: PositionalSpec{
			Rest: Literal,
		},
		ImplicitEffects: []ImplicitEffect{
			{Role: Remote(operation), Target: target},
		},
		Stdin:        StdinNever,
		Stdout:       stdout,
		UnknownFlag:  UnknownFlagInert,
		EndOfOptions: true,
	}
}

// bdNested is a bd verb with its own subcommands (dep, label, dolt).
func bdNested(name string, verbs map[string]string) CommandSchema {
	subs := map[string]CommandSchema{}
	for v, op := range verbs {
		subs[v] = bdRemote(v, op)
	}
	return CommandSchema{
		Name:         name,
		Provenance:   "bd " + name + " --help",
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands:  subs,
	}
}

// ---- trivial inert commands ------------------------------------------------

// sleepSchema: the only operands are durations. No flags besides
// --help/--version, so an unknown flag is insufficient. Verified against
// GNU coreutils 9.11 `sleep --version`.
var sleepSchema = CommandSchema{
	Name:         "sleep",
	Provenance:   "sleep (GNU coreutils) 9.11",
	Flags:        map[string]FlagSpec{"--version": inert, "--help": inert},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// whichSchema: prints the PATH resolution of each COMMAND name (metadata).
// `-i`/`--read-alias` (reads alias definitions from stdin) is deliberately
// unmodeled. Verified against GNU which v2.23 `which --help`.
var whichSchema = CommandSchema{
	Name:       "which",
	Provenance: "GNU which v2.23, which --help",
	Flags: map[string]FlagSpec{
		"-a": inert, "--all": inert,
		"-v": inert, "-V": inert, "--version": inert, "--help": inert,
		"--skip-dot": inert, "--skip-tilde": inert,
		"--show-dot": inert, "--show-tilde": inert,
		"--tty-only": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// pgrepSchema: lists PIDs matching a pattern (metadata). Every selector flag
// is a filter over /proc; `-F`/`--pidfile` reads a real file. `--signal` is
// accepted by pgrep's shared parser but only meaningful to pkill, and pkill
// itself is NOT registered — it sends signals; `--signal` is left unmodeled
// here so its presence abstains rather than silently passing. Verified
// against procps-ng 4.0.6 `pgrep --help`.
var pgrepSchema = CommandSchema{
	Name:       "pgrep",
	Provenance: "pgrep from procps-ng 4.0.6, pgrep --help",
	Flags: map[string]FlagSpec{
		"-d": literal1, "--delimiter": literal1,
		"-l": inert, "--list-name": inert,
		"-a": inert, "--list-full": inert,
		"--quiet": inert,
		"-v":      inert, "--inverse": inert,
		"-w": inert, "--lightweight": inert,
		"-c": inert, "--count": inert,
		"-f": inert, "--full": inert,
		"-g": literal1, "--pgroup": literal1,
		"-G": literal1, "--group": literal1,
		"-i": inert, "--ignore-case": inert,
		"-n": inert, "--newest": inert,
		"-o": inert, "--oldest": inert,
		"-O": literal1, "--older": literal1,
		"-p": literal1, "--pid": literal1,
		"-P": literal1, "--parent": literal1,
		"-s": literal1, "--session": literal1,
		"-t": literal1, "--terminal": literal1,
		"-u": literal1, "--euid": literal1,
		"-U": literal1, "--uid": literal1,
		"-x": inert, "--exact": inert,
		"-F": {Arity: ArityOne, Operand: PathRead}, "--pidfile": {Arity: ArityOne, Operand: PathRead},
		"-L": inert, "--logpidfile": inert,
		"-r": literal1, "--runstates": literal1,
		"-A": inert, "--ignore-ancestors": inert,
		"-Q": inert, "--shell-quote": inert,
		"--cgroup": literal1, "--ns": literal1, "--nslist": literal1, "--env": literal1,
		"-h": inert, "--help": inert, "-V": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// psSchema: process listing. UnknownFlagInert is a DELIBERATE, JUSTIFIED
// CHOICE (the same justification as echoSchema's): ps accepts three option
// dialects (BSD `aux`, Unix `-ef`, GNU `--sort`) whose every option selects
// or formats rows read from /proc — there is no ps option that writes a
// file, sends a signal, or opens a connection, so an unmodeled flag cannot
// unlock an effect this schema does not already emit (stdout metadata). The
// BSD-style bare `aux` is a positional and is Literal for the same reason.
// Verified against procps-ng 4.0.6.
var psSchema = CommandSchema{
	Name:         "ps",
	Provenance:   "ps from procps-ng 4.0.6, ps --help all",
	Flags:        map[string]FlagSpec{},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInert,
	EndOfOptions: false,
}

// ---- jq / yq ----------------------------------------------------------------

// jqSchema: the filter is a Leading Literal positional, skipped by
// `-f`/`--from-file` (which reads the filter FROM a path); every other
// positional is an input file (PathRead) — except under `--args` /
// `--jsonargs`, where the remaining positionals are string values, not
// files (RestOverride to Literal; jq then reads stdin). The filter text is
// INERT: jq 1.8 has no operator that writes a file or executes a command
// (`input_filename`, `$__loc__`, `env`/`$ENV` read the environment;
// `import`/`include` load modules from `-L` directories, which are modeled
// as PathRead of the directory). `--arg`/`--argjson` take two Literal
// values; `--slurpfile`/`--rawfile` take a name and a FILE (PathRead) —
// the ArityN shape. Stdin is read when no file operand was given (`-n` also
// suppresses it, an over-report this schema accepts). Verified against
// jq 1.8.2 `jq --help`.
var jqSchema = CommandSchema{
	Name:       "jq",
	Provenance: "jq 1.8.2, jq --help",
	Flags: map[string]FlagSpec{
		"-n": inert, "--null-input": inert,
		"-R": inert, "--raw-input": inert,
		"-s": inert, "--slurp": inert,
		"-c": inert, "--compact-output": inert,
		"-r": inert, "--raw-output": inert,
		"--raw-output0": inert,
		"-j":            inert, "--join-output": inert,
		"-a": inert, "--ascii-output": inert,
		"-S": inert, "--sort-keys": inert,
		"-C": inert, "--color-output": inert,
		"-M": inert, "--monochrome-output": inert,
		"--tab":           inert,
		"--indent":        literal1,
		"--unbuffered":    inert,
		"--stream":        inert,
		"--stream-errors": inert,
		"--seq":           inert,
		"-f":              {Arity: ArityOne, Operand: PathRead}, "--from-file": {Arity: ArityOne, Operand: PathRead},
		"-L": {Arity: ArityOne, Operand: PathRead}, "--library-path": {Arity: ArityOne, Operand: PathRead},
		"--arg":       {Arity: ArityN, Operands: []OperandRole{Literal, Literal}},
		"--argjson":   {Arity: ArityN, Operands: []OperandRole{Literal, Literal}},
		"--slurpfile": {Arity: ArityN, Operands: []OperandRole{Literal, PathRead}},
		"--rawfile":   {Arity: ArityN, Operands: []OperandRole{Literal, PathRead}},
		"--args":      inert,
		"--jsonargs":  inert,
		"-e":          inert, "--exit-status": inert,
		"-V": inert, "--version": inert,
		"-h": inert, "--help": inert,
		"--build-configuration": inert,
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Literal},
		LeadingSkippedByFlags: []string{"-f", "--from-file"},
		Rest:                  PathRead,
		RestOverride:          RestOverride{Flags: []string{"--args", "--jsonargs"}, Role: Literal},
	},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// yqSchema (mikefarah yq v4): `yq [eval|e|eval-all|ea] EXPR [FILE...]`, with
// `eval` implied when the first positional is not a subcommand key
// (DefaultSubcommand). The flags are accepted before or after the
// subcommand word, so the same table serves as the global table and the
// eval table (a shared map; the parent's transforms — `-i` — apply over the
// combined effects). The expression is a Leading Literal skipped by
// `--from-file` (a PathRead) or `--expression`; every other positional is a
// PathRead; `-i`/`--inplace` is TransformInPlace (rewrites the FIRST file —
// modeled as all file operands, an over-approximation in the safe
// direction). Deliberately unmodeled (Abstain): `-s`/`--split-exp` and
// `--split-exp-file` (write result files named by an expression) and
// `--security-enable-system-operator` (lets the expression execute
// commands). Accepted gap: with file operations enabled (the default) an
// expression's `load("path")` reads a file the schema cannot see — a read,
// never a write or exec. Verified against yq v4.53.2 `yq --help`.
var yqSchema = CommandSchema{
	Name:              "yq",
	Provenance:        "yq (mikefarah) v4.53.2, yq --help",
	Flags:             yqFlags,
	UnknownFlag:       UnknownFlagInsufficient,
	EndOfOptions:      true,
	DefaultSubcommand: "eval",
	Subcommands: map[string]CommandSchema{
		"eval": yqEvalSchema, "e": renamed(yqEvalSchema, "e"),
		"eval-all": renamed(yqEvalSchema, "eval-all"), "ea": renamed(yqEvalSchema, "ea"),
	},
}

var yqEvalSchema = CommandSchema{
	Name:       "eval",
	Provenance: "yq (mikefarah) v4.53.2, yq eval --help",
	Flags:      yqFlags,
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Literal},
		LeadingSkippedByFlags: []string{"--from-file", "--expression"},
		Rest:                  PathRead,
	},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var yqFlags = map[string]FlagSpec{
	"-i": {Transform: EffectTransform{Kind: TransformInPlace}}, "--inplace": {Transform: EffectTransform{Kind: TransformInPlace}},
	"--from-file":  {Arity: ArityOne, Operand: PathRead},
	"--expression": literal1,
	"-f":           literal1, "--front-matter": literal1,
	"-I": literal1, "--indent": literal1,
	"-p": literal1, "--input-format": literal1,
	"-o": literal1, "--output-format": literal1,
	"-C": inert, "--colors": inert,
	"-M": inert, "--no-colors": inert,
	"-N": inert, "--no-doc": inert,
	"-0": inert, "--nul-output": inert,
	"-n": inert, "--null-input": inert,
	"-P": inert, "--prettyPrint": inert,
	"-r": inert, "--unwrapScalar": inert,
	"-v": inert, "--verbose": inert,
	"-V": inert, "--version": inert,
	"-e": inert, "--exit-status": inert,
	"-c": inert, "--yaml-compact-seq-indent": inert,
	"-h": inert, "--help": inert,
	"--csv-auto-parse": inert, "--csv-separator": literal1,
	"--tsv-auto-parse":                inert,
	"--debug-node-info":               inert,
	"--header-preprocess":             inert,
	"--lua-globals":                   inert,
	"--lua-prefix":                    literal1,
	"--lua-suffix":                    literal1,
	"--lua-unquoted":                  inert,
	"--properties-array-brackets":     inert,
	"--properties-separator":          literal1,
	"--security-disable-env-ops":      inert,
	"--security-disable-file-ops":     inert,
	"--shell-key-separator":           literal1,
	"--string-interpolation":          inert,
	"--xml-attribute-prefix":          literal1,
	"--xml-content-name":              literal1,
	"--xml-directive-name":            literal1,
	"--xml-keep-namespace":            inert,
	"--xml-proc-inst-prefix":          literal1,
	"--xml-raw-token":                 inert,
	"--xml-skip-directives":           inert,
	"--xml-skip-proc-inst":            inert,
	"--xml-strict-mode":               inert,
	"--yaml-fix-merge-anchor-to-spec": inert,
}

// ---- formatters --------------------------------------------------------------

// gofmtSchema: `-w` rewrites each path operand in place (TransformInPlace);
// `-l`/`-d`/`-e`/`-s` only change what is printed; `-r` is a rewrite rule
// (a Literal); `-cpuprofile FILE` writes a profile file (PathTruncate).
// With no path operand gofmt formats stdin. Verified against this host's
// `gofmt -h` (go1.26).
var gofmtSchema = CommandSchema{
	Name:       "gofmt",
	Provenance: "gofmt (go1.26), gofmt -h",
	Flags: map[string]FlagSpec{
		"-w":          {Transform: EffectTransform{Kind: TransformInPlace}},
		"-l":          inert,
		"-d":          inert,
		"-e":          inert,
		"-s":          inert,
		"-r":          literal1,
		"-cpuprofile": {Arity: ArityOne, Operand: PathTruncate},
	},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: false,
}
