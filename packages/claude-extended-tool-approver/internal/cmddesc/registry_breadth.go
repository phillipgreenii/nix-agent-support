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
// push), and "dolt-server" for the Dolt server lifecycle verbs (start/stop/
// killall; Resource is "dolt", not "beads", for these three — see bdRemote).
// `bd dolt show / status / test` are READS of the connection configuration —
// the beads-remote-server rule itself tells an agent to run `bd dolt show`
// when a connection fails — so they are "read", not "dolt-server"; the
// resume bead's shorthand "dolt * => Forbidden" is refined to the lifecycle
// verbs only and that refinement is flagged on tc-q9ak.
//
// Slice 3n originally made "dolt-server" unconditionally Forbidden. Slice 3u
// REVISED this per an operator ruling (Phillip, 2026-09-07, verbatim,
// recorded on tc-vn5z): "for bd dolt, the default foe stsrt/stop/killall
// should be to abstain, but my persoanl confog on this would be yo reject."
// (typos corrected, meaning unambiguous from context: default Abstain/
// Unknown, with Reject available as the operator's OWN configuration, not a
// hard-coded default). The schema here is UNCHANGED by that ruling — "dolt"
// is still the right effect vocabulary target and "dolt-server" the right
// operation; only RemoteMutation's POLICY handling of "dolt-server" changed
// (see internal/effectpolicy/policy.go's RemoteMutation doc comment and
// evalcontract.Request.RemoteLifecycle for where the operator's
// configuration now lives, as DATA rather than a hard-coded verdict).
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

// ---- cd (slice 3o) -----------------------------------------------------------

// cdSchema: the shell builtin that changes the working directory of every
// LATER command in the same list — the single most common unregistered
// basename in the corpus (26,763 rows). Its one optional positional is a
// KindChdir operand (a metadata read of the directory plus an EffectChdir
// the graph builder threads through the rest of the list); with no operand
// the target is `~` (an implicit KindChdir of "~"). A second positional is
// bash's `cd OLD NEW` string-substitution form, whose target depends on the
// current directory's text — Unmodeled, so it abstains. `-` (the previous
// directory) is Dynamic. Flags are the four bash accepts, all inert
// (`-L`/`-P` symlink handling, `-e` exit status, `-@` extended attributes).
// pushd/popd are deliberately NOT registered (a directory stack is runtime
// state this slice does not model). Verified against this host's
// `help cd` (bash 5.x).
var cdSchema = CommandSchema{
	Name:       "cd",
	Provenance: "bash builtin, help cd",
	Flags: map[string]FlagSpec{
		"-L": inert, "-P": inert, "-e": inert, "-@": inert,
	},
	Positionals: PositionalSpec{
		Leading:         []OperandRole{Chdir},
		LeadingOptional: true,
		Rest:            Unmodeled,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: Chdir, Target: "~", WhenNoPositionals: true},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
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

// ---- awk / gawk (slice 3p) --------------------------------------------------

// awkSchema: the program text is a Leading positional unless -f/-e/-E
// supplied it via a flag (mirrors sedSchema's LeadingSkippedByFlags); the
// program text itself is classified by dialect_awk.go, registered as
// dialect "awk" regardless of which BINARY (awk, gawk) ran it — gawkSchema
// below is a plain rename of this value, exactly like head/rm/tee prove a
// registry entry is data. Verified against GNU Awk 5.4.1 on this host
// (`gawk --version`, `gawk --help`).
//
// -f/--file and -E/--exec both read a FILE that HOLDS the program (PathRead,
// exactly like sed's -f); -e/--source supplies program TEXT on the command
// line (Program("awk"), like sed's -e). -F/--field-separator and -v/--assign
// each take one inert value; -L/--lint takes an optional glued value
// ([fatal|invalid|no-ext]). Every other flag `gawk --help` lists is a
// boolean mode switch that cannot itself read, write, or execute anything
// (traditional/posix/optimize/lint-old/csv/bignum/trace/... toggles, plus
// --version/--help/--copyright), so all are inert.
//
// DELIBERATELY UNMODELED — left OUT of Flags entirely, so an unrecognised
// spelling abstains via UnknownFlagInsufficient rather than being silently
// approved: -d/-D/-o/-p (--dump-variables/--debug/--pretty-print/--profile)
// each write a dump or profile file named by an optional glued argument;
// -i/--include loads a gawk extension library BY NAME — and the "inplace"
// extension is exactly how `gawk -i inplace '{...}' file` rewrites its file
// operands in place, so modeling -i as an ordinary path read would be
// fail-open (it would hide a write inside what looks like a read); -l/--load
// loads a native (compiled) extension. This host's `gawk --help` output is
// itself the provenance for the flag set below.
var awkSchema = CommandSchema{
	Name:       "awk",
	Provenance: "GNU Awk 5.4.1 (gawk --version, gawk --help), this host 2026-09-07",
	Flags: map[string]FlagSpec{
		"-f": {Arity: ArityOne, Operand: PathRead}, "--file": {Arity: ArityOne, Operand: PathRead},
		"-E": {Arity: ArityOne, Operand: PathRead}, "--exec": {Arity: ArityOne, Operand: PathRead},
		"-e": {Arity: ArityOne, Operand: Program("awk")}, "--source": {Arity: ArityOne, Operand: Program("awk")},
		"-F": literal1, "--field-separator": literal1,
		"-v": literal1, "--assign": literal1,
		"-L": literalOpt, "--lint": literalOpt,
		"-b": inert, "--characters-as-bytes": inert,
		"-c": inert, "--traditional": inert,
		"-C": inert, "--copyright": inert,
		"-g": inert, "--gen-pot": inert,
		"-h": inert, "--help": inert,
		"-I": inert, "--trace": inert,
		"-k": inert, "--csv": inert,
		"-M": inert, "--bignum": inert,
		"-N": inert, "--use-lc-numeric": inert,
		"-n": inert, "--non-decimal-data": inert,
		"-O": inert, "--optimize": inert,
		"-P": inert, "--posix": inert,
		"-r": inert, "--re-interval": inert,
		"-s": inert, "--no-optimize": inert,
		"-S": inert, "--sandbox": inert,
		"-t": inert, "--lint-old": inert,
		"-V": inert, "--version": inert,
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Program("awk")},
		LeadingSkippedByFlags: []string{"-f", "--file", "-e", "--source", "-E", "--exec"},
		Rest:                  PathRead,
	},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
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

// treefmtSchema: `treefmt [paths...] [flags]` — the formatter multiplexer
// this repo's own pre-commit/nix-fmt workflow invokes (flake.nix's
// treefmt-nix input; CLAUDE.md's "Use `nix fmt` for formatting Nix files").
// tc-8og1 item 3 sub-slice 2 of 5 (build-tool family design, ruled on
// tc-vn5z Q1-Q5, 2026-09-08): a "data-first cmddesc schema" case, not one of
// the three live-static-parsing targets (justfile/package.json/devbox.json,
// slice 3ag) — treefmt has its own vetted binary and CLI, so it schematizes
// directly like gofmt/yq above rather than needing project-file discovery.
//
// Every configured formatter runs IN PLACE, UNCONDITIONALLY: this version
// has no --check/--dry-run flag — `--fail-on-change` still performs the
// rewrite and only exits nonzero if the rewrite actually changed something
// (verified live: `treefmt --help`'s own flag description, "Exit with error
// if any changes were made"). That makes treefmt's path operands closer to
// goFmtVerbSchema's own precedent ("go fmt": `Positionals.Rest: PathModify`
// unconditionally, "not PathRead" per its doc comment, because it too has no
// gating flag) than to gofmtSchema's/yqSchema's flag-gated
// `TransformInPlace` shape (`-w`/`-i`), which needs an explicit opt-in flag
// treefmt does not have.
//
// Path operands (Rest) are PathModify — an over-approximation in the safe
// direction: a given path only actually changes if some CONFIGURED
// formatter's include glob matches it (unknowable from argv alone; this
// slice does not parse treefmt.toml/flake.nix's treefmt-nix block), the same
// documented over-approximation yqSchema's `-i` takes for "the first file
// operand" and gofmtSchema's `-w` takes for every operand regardless of
// whether reformatting was actually needed.
//
// With ZERO path operands, treefmt walks the WHOLE tree from `--tree-root`
// (defaults to the git/jj worktree root, else the config file's own
// directory) and can rewrite any tracked file a configured formatter
// claims — modeled as an implicit PathModify of "." WhenNoPositionals,
// mirroring goFmtVerbSchema's own "go fmt" (no packages) implicit effect.
//
// `--stdin` is treefmt's editor-integration mode: the single positional
// becomes a FILENAME HINT used only to pick a formatter, actual content is
// read from stdin and the formatted result is written to STDOUT — the named
// path is never opened for read or write. Modeled via RestOverride (the
// same mechanism gitBranchSchema's `--list`/gitConfigSchema's `--get` use to
// swap Rest's role under a flag): under `--stdin`, Rest becomes Literal
// instead of PathModify. Accepted, documented gap: `--stdin` with ZERO
// positionals (not a real usage this schema has seen documented) still
// falls through to the ordinary WhenNoPositionals whole-tree implicit
// effect rather than being special-cased — real usage always pairs
// `--stdin` with exactly one filename-hint positional.
//
// `--cpu-profile FILE` writes a pprof profile (PathTruncate — gofmtSchema's
// own `-cpuprofile` precedent). `-i`/`--init` creates a NEW treefmt.toml in
// the CURRENT directory (implicit PathCreate of "treefmt.toml", gated
// WhenFlags). `--config-file FILE` only READS an alternate config path
// (PathRead).
//
// DELIBERATELY ABSENT from Flags (so their presence makes the WHOLE
// invocation Insufficient rather than being silently inert or guessed at —
// the same convention goBuildSchema's own `-C`/`-toolexec`/`-overlay`
// omissions and bdSchema's own `-C` use): `--tree-root`/`--tree-root-file`/
// `-C`/`--working-dir` each relocate what "." or a relative path operand
// actually resolves to, which this slice does not resolve; `--tree-root-cmd`
// additionally names an ARBITRARY COMMAND treefmt shells out to first to
// determine the root — an unvetted execution wrapper, the same shape as `go
// test`'s `-exec`; `-c`/`--clear-cache` resets treefmt's OWN evaluation
// cache, a directory this spike has no declared deletable Kind for (unlike
// GOCACHE/GOMODCACHE's goKind roots) — accurately modeling a cache-clear
// would need a new Kind, out of scope for this registry-entry-only slice.
//
// Sub-slice 2 of tc-8og1 item 3's 5-slice order (workspace verb-discovery
// facet [3ag, done], treefmt schema [this slice], request/rules.json
// wiring, cmddesc child-expression descriptor, nix run installable vetting):
// nothing here is wired into evalcontext.Request or a rules.json approval
// policy yet — TrustedCheckoutExec/DeleteAccess/NoWriteToReadOnlyPath/
// NoWriteToSecretPath already judge the PathModify/PathCreate/PathTruncate/
// PathRead effects this schema emits via their existing, command-name-blind
// path-access machinery, the same as any other producer of those roles.
//
// Verified against this host's installed treefmt v2.6.0
// (/nix/store/rma6wcl0qzq86fhsns9f6sahni4y5rx2-treefmt-2.6.0/bin/treefmt
// --version, --help — this repo's own treefmt-nix flake input tracks a
// close version, flake.nix's treefmt-nix input), 2026-09-08.
var treefmtSchema = CommandSchema{
	Name:       "treefmt",
	Provenance: "treefmt v2.6.0 (this host, treefmt --version / --help), 2026-09-08",
	Flags: map[string]FlagSpec{
		"--allow-missing-formatter": inert,
		"--ci":                      inert,
		"--completion":              literal1,
		"--config-file":             {Arity: ArityOne, Operand: PathRead},
		"--cpu-profile":             {Arity: ArityOne, Operand: PathTruncate},
		"--excludes":                literal1,
		"--fail-on-change":          inert,
		"-f":                        literal1, "--formatters": literal1,
		"-h": inert, "--help": inert,
		"-i": inert, "--init": inert,
		"--no-cache": inert,
		"-u":         literal1, "--on-unmatched": literal1,
		"-q": inert, "--quiet": inert,
		"--stdin": inert,
		"-v":      inert, "--verbose": inert,
		"--version": inert,
		"--walk":    literal1,
	},
	Positionals: PositionalSpec{
		Rest:         PathModify,
		RestOverride: RestOverride{Flags: []string{"--stdin"}, Role: Literal},
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".", WhenNoPositionals: true},
		{Role: PathCreate, Target: "treefmt.toml", WhenFlags: []string{"-i", "--init"}},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- find (slice 3q) ---------------------------------------------------------

// findSchema: interpreter-dispatched to findInterpreter (interpreter_find.go),
// since find's grammar (leading options, then starting-point operands, then
// an expression of tests/actions that mixes `-`-prefixed primaries with bare
// grouping tokens `(`/`)`/`!`) does not fit the Flags/Positionals table — see
// findInterpreter's doc comment for why. Flags/Positionals/ImplicitEffects
// below are therefore UNUSED (the interpreter does not call scan()); they are
// left at their zero value rather than populated with something the
// interpreter would ignore.
//
// On this host `find` is a shell FUNCTION (`type find`) wrapping Claude
// Code's bundled bfs, invoked as `exec -a bfs "$CC" -S dfs
// -regextype findutils-default "$@"` — so the hook sees the typed `find ...`
// verbatim, and `bfs --version` (2026-09-07) reports "bfs 4.1.1". bfs bills
// itself as findutils-compatible, so the primary vocabulary modeled here
// (interpreter_find.go's arity tables) is the GNU findutils spelling; the two
// flags Claude Code's wrapper injects (-S, -regextype) are bfs-only search-
// strategy/regex-dialect switches with no effect on the schema's vocabulary
// and are not modeled (an unrecognised leading `-S`/`-regextype` would only
// ever be typed by the wrapper, never by a modeled invocation this schema is
// asked to judge, since the hook sees the post-wrapper argv either way).
//
// Stdout: find's own output (bare `-print`, or the default when no action is
// named) is PATH NAMES ONLY, never file content — the same shape as ls, so
// StdoutMetadata; -ls/-printf/-fprintf format additional metadata (sizes,
// permissions) but still never file content. -fprint/-fprint0/-fls truncate
// a FILE (PathTruncate, findTruncateArgPrimaries) instead of using stdout.
var findSchema = CommandSchema{
	Name:         "find",
	Provenance:   "bfs 4.1.1 via Claude Code's find shell function (`type find`; `bfs --version`), GNU findutils-compatible primaries, this host 2026-09-07",
	Interpreter:  "find",
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	EndOfOptions: false,
}

// ---- go (slice 3x: tc-lc8f item 4e, tc-vn5z item 1) -------------------------

// goSchema: `go <command> [arguments]` — subcommand dispatch, exactly like
// gitSchema/bdSchema; `go help`'s own synopsis has no global flag before the
// subcommand, so Flags is empty here (verified on this host, `go version
// go1.26.6 linux/amd64`, `go help`, 2026-09-07).
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on tc-vn5z item
// 1): "go test and go generate are fine. go run is trickier. i would like
// it to be parsed, but i dont think there will be a definitition of the
// gonrun for the spexifox situatikn. so abstoan on it." Normalized: `go
// test`/`go generate` execute the checkout's OWN code — a PERMITTED class,
// citing `docs/adr/0053-ceta-threat-model.md`'s "2. What is trusted vs.
// what is screened" (the CWD/project tree is trusted state) and matching
// what production already approves today (internal/rules/buildtools.go's
// baseApprovedTools unconditionally lists "go", line ~31) — judged by
// TrustedCheckoutExec (internal/effectpolicy/policy.go), never by command
// name. `go run` is PARSED (goRunSchema below models its flags and target
// operand, visible in the interpreted graph) but its target's role is
// KindUnmodeled, so it always Abstains regardless of what the target or its
// arguments are — "no definition of what a go run target does exists for
// the specific situation" (goRunSchema's own doc comment records this).
// Everything else is the ORIGINAL proposal this ruling did not revise:
// build/vet/fmt/list/env/version/mod are reads plus build-cache writes
// (also EffectExec, since both classes reduce to the SAME question — is
// this CWD a recognised checkout — see effect.go's EffectExec doc comment);
// install/get are Unknown (never Permitted: they write GOBIN/GOPATH/bin or
// fetch modules over the network); clean's -cache/-modcache are ordinary
// PathDelete effects judged by the EXISTING DeleteAccess policy against the
// SAME cache roots goKind (internal/deletable/workspace.go) already
// declares deletable.
//
// `doc`, `tool`, `work` are DELIBERATELY ABSENT from Subcommands: the
// simplest correct model for all three (per the brief) is "insufficient",
// which an absent key already gives for free via interpretSubcommand's own
// "unmodeled subcommand" fallback — exactly gitWorktreeSchema's add/remove/
// prune precedent, no schema needed. `go tool vet`/`go tool cover`/`go tool
// pprof` reading files is a real, narrower exception `go help tool` alone
// cannot resolve without parsing the wrapped tool's OWN argv (out of scope
// here); omitting `tool` entirely fails closed for it too.
var goSchema = CommandSchema{
	Name:         "go",
	Provenance:   "go version go1.26.6 linux/amd64 (`go version`), go help / go help <command>, this host 2026-09-07",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"test":     goTestSchema,
		"generate": goGenerateSchema,
		"run":      goRunSchema,
		"build":    goBuildSchema,
		"vet":      goVetSchema,
		"fmt":      goFmtVerbSchema,
		"list":     goListSchema,
		"env":      goEnvSchema,
		"version":  goVersionSchema,
		"mod":      goModSchema,
		"clean":    goCleanSchema,
		"install":  goInstallSchema,
		"get":      goGetSchema,
	},
}

// mergeFlags returns a new map combining base with overrides layered on top
// (overrides win on key collision) — lets a `go` subcommand's OWN flags
// (go test's -run) sit alongside the shared "build flags" table
// (goBuildFlags) without repeating the shared set literally at every call
// site, the same "one schema borrows another's table" shape yqEvalSchema's
// yqFlags already uses, generalised to allow additions.
func mergeFlags(base map[string]FlagSpec, overrides map[string]FlagSpec) map[string]FlagSpec {
	m := make(map[string]FlagSpec, len(base)+len(overrides))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range overrides {
		m[k] = v
	}
	return m
}

// goBuildFlags: the subset of "go help build"'s shared build-flag table this
// slice models — go help build's own words: "The build flags are shared by
// the build, clean, get, install, list, run, and test commands" (go vet and
// go generate accept most of the same set too, per their own usage lines).
// Every one modeled here is inert or a single literal value; NONE of them
// names a filesystem path this schema tracks (the tool's own writes land in
// its build cache, represented separately as an EffectExec, not a path
// effect — see goTestSchema's ImplicitEffects). Verified against this
// host's `go help build` (go1.26.6, 2026-09-07).
//
// DELIBERATELY ABSENT (fails closed to Insufficient rather than silently
// inert), each a distinct trust boundary this slice's ruling does not
// cover:
//   - -C dir: changes CWD before the rest of argv runs — the same shape as
//     bd's -C/--directory (bdSchema's own comment) and cd's KindChdir role;
//     unmodeled here, not silently ignored.
//   - -toolexec 'cmd args': wraps EVERY toolchain step (compile/link/vet/
//     asm) in an arbitrary external program named verbatim on the command
//     line — an unvetted execution wrapper, the same shape as go test's
//     -exec (goTestSchema's own comment) and go vet's -vettool
//     (goVetSchema's own comment).
//   - -overlay file: a JSON mapping that can transparently substitute the
//     CONTENT of any disk path during the build — mirrors yqSchema's
//     omission of --security-enable-system-operator.
var goBuildFlags = map[string]FlagSpec{
	"-a": inert, "-n": inert, "-x": inert, "-v": inert, "-work": inert,
	"-p":    literal1,
	"-race": inert, "-msan": inert, "-asan": inert,
	"-cover": inert, "-covermode": literal1, "-coverpkg": literal1,
	"-asmflags": literal1, "-buildmode": literal1, "-buildvcs": literal1,
	"-compiler": literal1, "-gccgoflags": literal1, "-gcflags": literal1,
	"-installsuffix": literal1, "-json": inert,
	"-ldflags": literal1, "-linkshared": inert,
	"-mod": literal1, "-modcacherw": inert, "-modfile": literal1,
	"-pgo": literal1, "-pkgdir": literal1,
	"-tags": literal1, "-trimpath": inert,
}

// goTestSchema: `go test [build/test flags] [packages]`. Every invocation
// emits an EffectExec ("go test") judged by TrustedCheckoutExec — the
// compiled test binary runs the package's OWN code, the ruling's "go test
// ... are fine" — plus a PathRead for each package/file operand (./...,
// ./internal/x, a bare .go file). Flags with values are modeled per the
// brief's named list: -run/-bench/-count/-timeout/-cpu/-shuffle are
// inert-value (they select/repeat/seed, never touch a path); -coverprofile
// writes a coverage profile FILE (PathTruncate — the one flag whose value
// can name an arbitrary path, hence Reject-capable, exactly like
// gofmtSchema's -cpuprofile); -short/-failfast are boolean.
//
// -exec CMD is DELIBERATELY ABSENT: it wraps the compiled test BINARY's own
// execution in an external program name taken verbatim off the command
// line — verified LIVE on this host: `go test -exec frobnicate ./...`
// attempts to exec "frobnicate" and fails only because it is not on PATH
// (`exec: "frobnicate": executable file not found in $PATH`), confirming
// -exec is a real, accepted go test flag despite not being itself listed in
// `go help testflag`'s own flag table (it is documented under `go help
// run`, which go test's flag parser shares). An unvetted execution wrapper,
// so its presence is Insufficient (Abstain), never silently inert or
// rejected.
//
// Verified against this host's `go help testflag` (go1.26.6, 2026-09-07).
var goTestSchema = CommandSchema{
	Name:       "test",
	Provenance: "go help testflag, go help test (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-run": literal1, "-bench": literal1, "-count": literal1, "-timeout": literal1,
		"-cpu": literal1, "-shuffle": literal1,
		"-short": inert, "-failfast": inert,
		"-coverprofile": {Arity: ArityOne, Operand: PathTruncate},
	}),
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go test"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goGenerateSchema: `go generate [-run regexp] [-n] [-v] [-x] [build flags]
// [file.go... | packages]`. Every //go:generate directive in the matched
// files runs an ARBITRARY local executable — the ruling's own "go
// generate ... are fine", the same EffectExec as go test. Verified against
// this host's `go help generate` (go1.26.6, 2026-09-07).
var goGenerateSchema = CommandSchema{
	Name:       "generate",
	Provenance: "go help generate (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-run": literal1, "-n": inert, "-v": inert, "-x": inert,
	},
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go generate"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goRunSchema: `go run [build flags] [-exec xprog] package [arguments...]`.
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on tc-vn5z item
// 1): "go run is trickier. i would like it to be parsed, but i dont think
// there will be a definitition of the gonrun for the spexifox situatikn. so
// abstoan on it." Normalized: flags and the package/file TARGET are parsed
// (visible in the interpreted graph — PositionalsEndOptions stops flag
// scanning at the target, exactly like a shell handing every LATER `-x` to
// the program it runs rather than to itself: the same convention
// CommandSchema.PositionalsEndOptions's own doc comment already names for
// xargs), but the target's role is KindUnmodeled, which fails the
// interpretation closed (builder-level Insufficient => Abstain, NEVER
// Reject — no policy ever runs on an Unmodeled-role operand) regardless of
// what the target or its trailing arguments are. "No definition of what a
// go run target does exists for the specific situation" is recorded HERE,
// in the schema's own doc comment, per this spike's established convention
// for a deliberately-unmodeled positional (gitBranchSchema/gitConfigSchema
// put the SAME kind of "why" in prose while the RUNTIME Insufficiency text
// stays the generic "unmodeled operand role" message — see operand()'s
// default case, interpreter.go).
//
// Verified against this host's `go help run` (go1.26.6, 2026-09-07).
var goRunSchema = CommandSchema{
	Name:       "run",
	Provenance: "go help run (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-exec": literal1,
	}),
	Positionals: PositionalSpec{
		Leading: []OperandRole{Unmodeled},
		Rest:    Unmodeled,
	},
	PositionalsEndOptions: true,
	Stdin:                 StdinNever,
	Stdout:                StdoutMetadata,
	UnknownFlag:           UnknownFlagInsufficient,
	EndOfOptions:          true,
}

// goBuildSchema: `go build [-o output] [build flags] [packages]`. Reads
// package operands plus ordinary build-cache traffic (EffectExec), per the
// operator's original proposal ("build/vet/fmt/mod tidy/list/env/version
// are reads plus build-cache writes"); -o writes the compiled binary/object
// to an EXPLICIT path (PathTruncate — the one build-family path this slice
// tracks by name, exactly like gofmtSchema's -cpuprofile). Verified against
// this host's `go help build` (go1.26.6, 2026-09-07).
var goBuildSchema = CommandSchema{
	Name:       "build",
	Provenance: "go help build (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-o": {Arity: ArityOne, Operand: PathTruncate},
	}),
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go build"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goVetSchema: `go vet [build flags] [-vettool prog] [vet flags]
// [packages]`. -vettool names an ALTERNATE analysis tool binary go vet
// execs in place of its own — the same unvetted-execution-wrapper shape as
// go test's -exec/go build's -toolexec — and -fix/-diff (cmd/vet's own
// flags, `go help vet`) apply the tool's suggested fixes DIRECTLY to source
// files, a real in-place write this slice does not track; all three are
// DELIBERATELY ABSENT so their presence fails closed to Insufficient.
// Verified against this host's `go help vet` (go1.26.6, 2026-09-07).
var goVetSchema = CommandSchema{
	Name:       "vet",
	Provenance: "go help vet (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-c": literal1,
	}),
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go vet"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goFmtVerbSchema: `go fmt [-n] [-x] [packages]` — the `go fmt` SUBCOMMAND
// (distinct from the standalone `gofmt` binary already registered as
// gofmtSchema above). It runs `gofmt -l -w` UNCONDITIONALLY (`go help
// fmt`'s own words: "Fmt runs the command 'gofmt -l -w'"), so every package
// operand is a real in-place rewrite regardless of flags (PathModify, not
// PathRead — unlike every other verb in this family). -n/-x only change
// what is PRINTED (dry-run/echo), not whether the rewrite happens, so they
// stay inert. Verified against this host's `go help fmt` (go1.26.6,
// 2026-09-07).
var goFmtVerbSchema = CommandSchema{
	Name:       "fmt",
	Provenance: "go help fmt (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-n": inert, "-x": inert, "-mod": literal1,
	},
	Positionals: PositionalSpec{Rest: PathModify},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".", WhenNoPositionals: true},
		{Role: Exec, Target: "go fmt"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// goListSchema: `go list [-f format] [-json] [-m] [list flags] [build
// flags] [packages]` — a read-only report (StdoutContent: the caller may
// pipe it into a template/JSON consumer). Verified against this host's
// `go help list` (go1.26.6, 2026-09-07).
var goListSchema = CommandSchema{
	Name:       "list",
	Provenance: "go help list (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-m": inert, "-f": literal1, "-e": inert, "-deps": inert,
	}),
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go list"}},
	Stdin:           StdinNever,
	Stdout:          StdoutContent,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goEnvSchema: `go env [-json] [-changed] [-u] [-w] [var ...]`. A bare read
// (`go env GOPATH`) prints an environment VALUE and is always safe (Literal
// positionals — an env var NAME is inert, not a path). -w/-u are
// DELIBERATELY ABSENT: they PERSIST a change to $GOENV (typically
// ~/.config/go/env), a real write to a config file outside every root this
// slice declares deletable — a distinct trust boundary from a plain read,
// so their presence fails closed to Insufficient rather than being modeled
// as just another inert flag. Verified against this host's `go help env`
// (go1.26.6, 2026-09-07).
var goEnvSchema = CommandSchema{
	Name:       "env",
	Provenance: "go help env (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-json": inert, "-changed": inert,
	},
	Positionals:     PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go env"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goVersionSchema: `go version [-m] [-v] [-json] [file ...]`. With zero
// file operands (this slice's only golden, bare `go version`) it reports
// the go TOOL's own version; file operands are read for their embedded
// build-info, hence PathRead. Verified against this host's `go help
// version` (go1.26.6, 2026-09-07).
var goVersionSchema = CommandSchema{
	Name:       "version",
	Provenance: "go help version (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-m": inert, "-v": inert, "-json": inert,
	},
	Positionals:     PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go version"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// goModSchema: `go mod <command> [arguments]` — a second, nested
// Subcommands dispatch (interpretSubcommand recurses on the same code path
// at any depth, exactly like git's `worktree`/bd's `dep`/`label`/`dolt`).
// Every registered verb (tidy/download/verify/why/graph/vendor/edit) is
// part of the operator's original proposal's "reads plus build-cache
// writes" approved class; `edit` additionally writes go.mod DIRECTLY (`go
// help mod edit`), which is fine — go.mod sits inside the project's own RW
// zone, judged like any other project file, no different from `go fmt`
// rewriting a source file. Verified against this host's `go help mod`
// (go1.26.6, 2026-09-07).
var goModSchema = CommandSchema{
	Name:         "mod",
	Provenance:   "go help mod (go1.26.6, this host 2026-09-07)",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"tidy": goModVerbSchema("tidy", map[string]FlagSpec{
			"-e": inert, "-v": inert, "-x": inert, "-diff": inert, "-go": literal1, "-compat": literal1,
		}),
		"download": goModVerbSchema("download", map[string]FlagSpec{
			"-x": inert, "-json": inert, "-reuse": literal1,
		}),
		"verify": goModVerbSchema("verify", map[string]FlagSpec{}),
		"why": goModVerbSchema("why", map[string]FlagSpec{
			"-m": inert, "-vendor": inert,
		}),
		"graph": goModVerbSchema("graph", map[string]FlagSpec{
			"-go": literal1, "-x": inert,
		}),
		"vendor": goModVerbSchema("vendor", map[string]FlagSpec{
			"-e": inert, "-v": inert, "-o": literal1,
		}),
		"edit": goModEditSchema,
	},
}

// goModVerbSchema builds one `go mod <verb>` schema: package/module pattern
// positionals are Literal (verify/tidy take none; why/download take
// patterns; the pattern text itself is inert), and every verb emits the
// same EffectExec ("go mod <verb>") judged by TrustedCheckoutExec.
func goModVerbSchema(verb string, flags map[string]FlagSpec) CommandSchema {
	return CommandSchema{
		Name:            verb,
		Provenance:      "go help mod " + verb + " (go1.26.6, this host 2026-09-07)",
		Flags:           flags,
		Positionals:     PositionalSpec{Rest: Literal},
		ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "go mod " + verb}},
		Stdin:           StdinNever,
		Stdout:          StdoutMetadata,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// goModEditSchema: `go mod edit [editing flags] [-fmt|-print|-json]
// [go.mod]` writes go.mod directly for every editing flag (-module,
// -require, -godebug, ...; a full enumeration of go help mod edit's many
// editing flags is out of scope for this slice) — modeled as an
// UNCONDITIONAL implicit PathModify of "go.mod" (its own default target
// file, same shape as goFmtVerbSchema's rewrite) rather than per-flag,
// since every editing flag shares the identical write target. -print/-json
// (read-only, print the result instead of writing) are an accepted
// over-approximation: this schema still reports the write, which merely
// costs an unnecessary (but harmless, since the write lands inside the
// project's own RW zone) PathModify finding.
var goModEditSchema = CommandSchema{
	Name:       "edit",
	Provenance: "go help mod edit (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-fmt": inert, "-print": inert, "-json": inert,
		"-module": literal1, "-go": literal1, "-toolchain": literal1,
		"-require": literal1, "-droprequire": literal1,
		"-replace": literal1, "-dropreplace": literal1,
		"-exclude": literal1, "-dropexclude": literal1,
		"-retract": literal1, "-dropretract": literal1,
		"-godebug": literal1, "-dropgodebug": literal1,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: "go.mod"},
		{Role: Exec, Target: "go mod edit"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// goCleanSchema: `go clean [-i] [-r] [-cache] [-testcache] [-modcache]
// [-fuzzcache] [build flags] [packages]`. -cache/-modcache each delete an
// ENTIRE cache root goKind (internal/deletable/workspace.go) already
// declares Deletable — DeleteAccess judges the resulting PathDelete exactly
// like `rm -rf` would (tc-z806's own DeleteAccess ladder), so this schema
// need not special-case go clean's verdict: whatever DeleteAccess concludes
// for that root today is what this slice's golden RECORDS (its own case
// comment says so explicitly — this slice does not force an expected
// verdict for either flag). -testcache/-fuzzcache remove narrower SUBSETS
// of the SAME GOCACHE tree with no separate root of their own — left
// unmodeled (Insufficient) rather than mapped onto the whole-cache delete,
// which would over-report. -i (also removes the installed binary from
// GOBIN) and a bare `go clean` with no cache flag (removes stray object
// files INSIDE the package source directories themselves, per `go help
// clean`) are also left unmodeled — real but narrow gaps, accepted for this
// slice.
//
// Verified against this host's `go help clean` (go1.26.6, 2026-09-07); the
// literal cache roots below match this host's actual `go env
// GOCACHE`/`GOMODCACHE` (linux default derivation, no GOCACHE/GOMODCACHE/
// GOPATH override set on this host) — see goKind's own doc comment for the
// documented XDG_CACHE_HOME/GOPATH override gap this schema inherits rather
// than re-solves (a host with either variable set to something outside
// $HOME would see DeleteAccess's verdict for these two paths diverge from
// goKind's actual declared root, exactly as goKind's own GOMODCACHE/
// patheval zone-conflict gap is already documented, not re-litigated here).
var goCleanSchema = CommandSchema{
	Name:       "clean",
	Provenance: "go help clean (go1.26.6, this host 2026-09-07)",
	Flags: map[string]FlagSpec{
		"-i": inert, "-r": inert, "-cache": inert, "-testcache": inert,
		"-modcache": inert, "-fuzzcache": inert,
		"-n": inert, "-x": inert, "-v": inert,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathDelete, Target: "~/.cache/go-build", WhenFlags: []string{"-cache"}},
		{Role: PathDelete, Target: "~/go/pkg/mod", WhenFlags: []string{"-modcache"}},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// goInstallSchema / goGetSchema: `go install [build flags] [packages]` /
// `go get [-t] [-u] [-tool] [build flags] [packages]`. Both are Unknown
// (insufficient), NEVER Permitted, per the brief: install compiles AND
// WRITES an executable into GOBIN/GOPATH/bin (outside every root this slice
// declares deletable or otherwise vouches for); get downloads arbitrary
// remote module CODE over the network and rewrites go.mod/go.sum to
// require it — a network-reached, unreviewed write, not a build-cache
// write. The unconditional ImplicitEffect below (Role: Unmodeled, no
// WhenNoPositionals/WhenFlags condition) forces every invocation
// insufficient regardless of flags or operand count — including the
// zero-positional `go install`/`go get` forms no golden here exercises —
// via emitImplicit's own fail-closed default case (interpreter.go), the
// same mechanism gitBranchSchema/gitConfigSchema use for an unmodeled
// POSITIONAL, applied here to the WHOLE invocation instead of one operand.
// Package/module operands are still modeled as Literal (visible in the
// graph) even though they cannot change the (always-Abstain) verdict.
//
// Verified against this host's `go help install` / `go help get` (go1.26.6,
// 2026-09-07).
var goInstallSchema = CommandSchema{
	Name:        "install",
	Provenance:  "go help install (go1.26.6, this host 2026-09-07)",
	Flags:       goBuildFlags,
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: Unmodeled, Target: "go install writes GOBIN/GOPATH/bin; not modeled"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var goGetSchema = CommandSchema{
	Name:       "get",
	Provenance: "go help get (go1.26.6, this host 2026-09-07)",
	Flags: mergeFlags(goBuildFlags, map[string]FlagSpec{
		"-t": inert, "-u": inert, "-tool": inert,
	}),
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: Unmodeled, Target: "go get downloads modules and rewrites go.mod/go.sum; not modeled"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- kubectl (slice 3y, tc-lc8f item 4f; tc-vn5z item 3) ------------------
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on bead tc-vn5z):
// "kubectl should be configured to vary per context. ie, there could be a
// "dev" cluster which would allow most anythkng vs a "prod" which could be
// more restricted." Normalized: kubectl reads are NOT permitted
// unconditionally (unlike bd's own "read" Operation, which IS — see
// bdSchema's doc comment); the verdict for every EffectRemote a kubectl
// invocation produces depends on BOTH the kube CONTEXT (`--context NAME`,
// captured by kubectlInterpreter — see interpreter_kubectl.go) and an
// Operation CLASS this schema assigns per subcommand: "read" (get/describe/
// logs/top/explain/version/api-resources/api-versions/cluster-info/diff/
// `config view|get-contexts|get-clusters|get-users|current-context`),
// "mutation" (apply/create/delete/patch/edit/replace/scale/rollout's every
// sub-verb/label/annotate/set/expose/run/cordon/uncordon/drain/taint/`config
// use-context|set-context|set-cluster|set-credentials|set|unset|delete-
// context|delete-cluster|delete-user|rename-context`), or "exec" (exec/
// port-forward/attach/debug/proxy/cp — see kubectlExecClassVerb and
// interpreter_kubectl.go's kubectlCpInterpreter for why this whole class
// stays unconditionally insufficient regardless of context/class). The
// per-context, per-class verdict itself is effectpolicy.KubeContextPolicy's
// job (internal/effectpolicy/policy.go); this schema's only responsibility
// is producing an EffectRemote{Operation: class, Family: "kubectl"} per
// invocation for that policy to judge — nothing here computes a verdict.
//
// `doc`/`kuberc`/`plugin`/`certificate`/`autoscale`/`wait`/`events`/`auth`/
// `kustomize`/`completion` are deliberately ABSENT: an unmodeled subcommand
// already Abstains for free via interpretSubcommand's own fallback (no
// schema needed, gitWorktreeSchema's add/remove/prune precedent, go's own
// doc/tool/work precedent).
//
// Verified against this host's `kubectl --help` / `kubectl <verb> --help` /
// `kubectl options` (Client Version v1.36.3, this host 2026-09-07).
const kubectlProvenance = "kubectl v1.36.3 (client), kubectl --help / kubectl <verb> --help / kubectl options, this host 2026-09-07"

// kubectlSchema: subcommand dispatch via a BESPOKE interpreter (kubectlInterpreter,
// interpreter_kubectl.go) rather than the plain generic one — see that type's
// doc comment for why: the per-context policy needs the ACTUAL --context
// VALUE, which is a global flag captured here and threaded onto every
// Family=="kubectl" EffectRemote the chosen subcommand produces, however deep
// (`config X` nests one further level through the ordinary interpretSubcommand
// recursion kubectlInterpreter delegates to for the subcommand itself).
//
// Global flags: `--context` is captured (see interpreter_kubectl.go's
// kubectlContextValue), never itself emitting an effect (Literal — its VALUE
// is read directly off the scanned pendingOp, not through the operand()
// path). `--kubeconfig FILE` is a genuine PathRead (kubectlInterpreter calls
// resolve() on the parent scan specifically so this flag's operand effect is
// not silently dropped — see that type's own doc comment for why a
// Subcommands-shaped schema's global flags do not normally get this). Every
// other global flag (-n/--namespace, -o/--output, --as/--as-group/--as-uid,
// --server/-s, --cluster, --token/--user/--username/--password, -v/--v) is
// inert to this model: `--server`/`--cluster` naming the cluster SOME OTHER
// way than `--context` do not make the context "known" under some other
// name — they simply leave `--context` absent, which is already Unknown by
// construction (kubectlContextValue never reads their values), matching the
// ruling's "treat --server/--cluster as making the context Unknown unless
// --context is also given" exactly.
var kubectlSchema = CommandSchema{
	Name:       "kubectl",
	Provenance: kubectlProvenance,
	Flags: map[string]FlagSpec{
		"--context":    literal1,
		"--kubeconfig": {Arity: ArityOne, Operand: PathRead},
		"-n":           literal1, "--namespace": literal1,
		"-o": literal1, "--output": literal1,
		"--as": literal1, "--as-group": literal1, "--as-uid": literal1,
		"-s": literal1, "--server": literal1,
		"--cluster": literal1,
		"--token":   literal1, "--user": literal1, "--username": literal1, "--password": literal1,
		"-v": literal1, "--v": literal1,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Interpreter:  "kubectl",
	Subcommands:  kubectlSubcommands(),
}

// kubectlSubcommands builds the dispatch table from the class lists so each
// verb's classification is visible in one place (bdSubcommands' own
// precedent).
func kubectlSubcommands() map[string]CommandSchema {
	m := map[string]CommandSchema{}
	for _, v := range []string{
		"get", "describe", "logs", "top", "explain", "version",
		"api-resources", "api-versions", "cluster-info",
	} {
		m[v] = kubectlRemoteVerb(v, "read")
	}
	m["diff"] = kubectlManifestVerb("diff", "read", false)
	for _, v := range []string{
		"edit", "scale", "label", "annotate", "set", "expose", "run",
		"cordon", "uncordon", "drain", "taint",
	} {
		m[v] = kubectlRemoteVerb(v, "mutation")
	}
	for _, v := range []string{"apply", "create", "delete", "replace", "patch"} {
		m[v] = kubectlManifestVerb(v, "mutation", true)
	}
	m["rollout"] = kubectlRolloutSchema()
	m["config"] = kubectlConfigSchema()
	for _, v := range []string{"exec", "port-forward", "attach", "debug", "proxy"} {
		m[v] = kubectlExecClassVerb(v)
	}
	m["cp"] = kubectlCpSchema
	return m
}

// kubectlRemoteVerb is one FLAT kubectl leaf whose whole effect is the
// implicit per-context, per-class EffectRemote: every positional is an inert
// Literal (a resource type, a name, a label selector — none of this schema's
// verbs write anything the graph can name beyond the remote effect itself),
// and every flag is UnknownFlagInert, mirroring bdRemote's own justification
// ("Mutation subcommands are Unknown whatever their flags... a flag cannot
// make a consent-requiring write need MORE than consent"): here the argument
// transfers because the classification is COARSE (read/mutation/exec, not
// per-flag), so an unmodeled flag (`-o wide`, `--show-labels`, ...) cannot
// change which class already governs the verdict. Target is left "" — the
// value is overwritten unconditionally by kubectlInterpreter's context stamp,
// so an empty starting value is the SAFE fallback (Unknown) if that stamping
// were ever somehow skipped.
func kubectlRemoteVerb(name, operation string) CommandSchema {
	return CommandSchema{
		Name:       name,
		Provenance: kubectlProvenance,
		Flags:      map[string]FlagSpec{},
		Positionals: PositionalSpec{
			Rest: Literal,
		},
		ImplicitEffects: []ImplicitEffect{
			{Role: Remote(operation), RemoteFamily: "kubectl"},
		},
		Stdin:        StdinNever,
		Stdout:       StdoutContent,
		UnknownFlag:  UnknownFlagInert,
		EndOfOptions: true,
	}
}

// kubectlManifestVerb is kubectlRemoteVerb's sibling for the verbs that also
// accept a manifest operand: `-f FILE`/`--filename FILE` and `-k DIR`/
// `--kustomize DIR` are real PathRead effects (kubectl reads and applies
// their CONTENT; `apply -f -`'s stdin special case lives in
// kubectlManifestInterpreter, interpreter_kubectl.go, since the generic
// StdinToken convention only ever fires for a POSITIONAL path operand — see
// that type's own doc comment). dryRunCapable wires kubectl's OWN
// `--dry-run='none'|'server'|'client'` enum (verified against `kubectl apply
// --help` / `kubectl delete --help`: no bare `--dry-run` spelling exists,
// always `=value`) as three EXACT flag-spelling keys rather than one
// value-taking flag, because the schema's transform mechanism keys on FLAG
// SPELLING, not flag VALUE — `--dry-run=client` alone carries
// Transform:{Kind: TransformDryRun} (cmddesc/transform.go's remoteMutationOps
// now includes "mutation", slice 3y), marking the EffectRemote DryRun exactly
// like gitPushSchema's own `-n`/`--dry-run`; `--dry-run=server` still
// contacts the API server for admission/validation (kubectl's own
// documented semantics: "submit server-side request without persisting the
// resource") so it stays an ORDINARY, un-marked "mutation" — slice 3w's
// dry-run precedent, applied to a third Operation vocabulary.
func kubectlManifestVerb(name, operation string, dryRunCapable bool) CommandSchema {
	flags := map[string]FlagSpec{
		"-f": {Arity: ArityOne, Operand: PathRead}, "--filename": {Arity: ArityOne, Operand: PathRead},
		"-k": {Arity: ArityOne, Operand: PathRead}, "--kustomize": {Arity: ArityOne, Operand: PathRead},
	}
	if dryRunCapable {
		flags["--dry-run=client"] = FlagSpec{Transform: EffectTransform{Kind: TransformDryRun}}
		flags["--dry-run=server"] = FlagSpec{}
		flags["--dry-run=none"] = FlagSpec{}
	}
	return CommandSchema{
		Name:       name,
		Provenance: kubectlProvenance,
		Flags:      flags,
		Positionals: PositionalSpec{
			Rest: Literal,
		},
		ImplicitEffects: []ImplicitEffect{
			{Role: Remote(operation), RemoteFamily: "kubectl"},
		},
		// Stdin stays StdinNever here: `-f -` is handled by
		// kubectlManifestInterpreter's own special case, not the generic
		// StdinWhenNoPathOperands convention (a manifest verb's positionals
		// are resource-type/name Literals, not path operands, so that
		// convention does not apply to this family regardless).
		Stdin:        StdinNever,
		Stdout:       StdoutContent,
		UnknownFlag:  UnknownFlagInert,
		EndOfOptions: true,
		Interpreter:  "kubectl-manifest",
	}
}

// kubectlRolloutSchema: `kubectl rollout SUBVERB` is itself a nested
// subcommand table (status/history/undo/pause/resume/restart). The brief
// classifies "rollout (all verbs)" as mutation UNIFORMLY, including
// status/history (which are, in isolation, reads) — a deliberate
// simplification (documented, not a mistake): splitting rollout's own
// sub-verbs by read/mutation would need a THIRD level of per-verb data this
// slice's scope does not ask for, and folding status/history into "mutation"
// is the SAFE direction (a context that allows only "read" would then
// Forbid/Unknown a rollout status it could otherwise have Permitted — never
// the reverse).
func kubectlRolloutSchema() CommandSchema {
	sub := map[string]CommandSchema{}
	for _, v := range []string{"status", "history", "undo", "pause", "resume", "restart"} {
		sub[v] = kubectlRemoteVerb(v, "mutation")
	}
	return CommandSchema{
		Name:         "rollout",
		Provenance:   kubectlProvenance,
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands:  sub,
	}
}

// kubectlConfigSchema: `kubectl config SUBVERB` per the brief's own split —
// view/get-contexts/get-clusters/get-users/current-context read the
// kubeconfig FILE (not the cluster), use-context/set-context/set-cluster/
// set-credentials/set/unset/delete-context/delete-cluster/delete-user/
// rename-context write it. Every sub-verb's own flags/positionals are
// modeled as inert Literals (bdNested's own precedent for a nested table
// whose leaves are all "the verb's whole effect is the implicit remote
// operation") — WHAT a `set-context` call actually changes is not modeled,
// only THAT it is a mutation-class kubeconfig write.
func kubectlConfigSchema() CommandSchema {
	sub := map[string]CommandSchema{}
	for _, v := range []string{"view", "get-contexts", "get-clusters", "get-users", "current-context"} {
		sub[v] = kubectlRemoteVerb(v, "read")
	}
	for _, v := range []string{
		"use-context", "set-context", "set-cluster", "set-credentials", "set",
		"unset", "delete-context", "delete-cluster", "delete-user", "rename-context",
	} {
		sub[v] = kubectlRemoteVerb(v, "mutation")
	}
	return CommandSchema{
		Name:         "config",
		Provenance:   kubectlProvenance,
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands:  sub,
	}
}

// kubectlExecClassVerb models exec/port-forward/attach/debug/proxy — every
// one starts an interactive/streaming session INSIDE the cluster (a
// container's shell, a forwarded port, a debug pod) whose actual content this
// schema cannot see. It declares TWO implicit effects, both unconditional
// (no WhenNoPositionals/WhenFlags guard, go's own install/get precedent):
// a Remote("exec") effect (so the class is visible in the graph and judged
// per context/class like everything else — a config that already forbids
// "exec" for this context Forbids it outright), AND an Unmodeled effect
// (Role: Unmodeled, interpreter.go's own fail-closed default case), which
// UNCONDITIONALLY marks the whole invocation insufficient regardless of what
// the first effect says — "the leaf is insufficient/Unknown" per the brief,
// because the actual argv run INSIDE the container (`exec ... -- sh`) is not
// itself recursed into as a real child: doing so would hand it to the LOCAL
// registry, which would wrongly judge remote container code as if it were a
// local command. Modeling that properly (a genuinely remote child scope) is
// a documented follow-up, not attempted this slice.
func kubectlExecClassVerb(name string) CommandSchema {
	return CommandSchema{
		Name:         name,
		Provenance:   kubectlProvenance,
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInert,
		EndOfOptions: true,
		ImplicitEffects: []ImplicitEffect{
			{Role: Remote("exec"), RemoteFamily: "kubectl"},
			{Role: Unmodeled, Target: name + "'s remote container/session content is not modeled"},
		},
	}
}

// kubectlCpSchema: `kubectl cp` is interpreted entirely by
// kubectlCpInterpreter (interpreter_kubectl.go) — its two positionals
// (source, destination) are classified LOCAL vs REMOTE by their own text
// (kubectl's `[namespace/]pod:path` colon convention), which no
// PositionalSpec/OperandRole shape can express, so Positionals/
// ImplicitEffects here are unused (the custom interpreter builds effects
// directly).
var kubectlCpSchema = CommandSchema{
	Name:       "cp",
	Provenance: kubectlProvenance,
	Flags: map[string]FlagSpec{
		"-c": literal1, "--container": literal1,
		"--no-preserve": inert,
		"--retries":     literal1,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Interpreter:  "kubectl-cp",
}

// ---- ssh (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4) ---------------------
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on bead tc-vn5z):
// "for ssh, abstain for paths should be thr default. however, we should
// allow some way to spexify a list of categorized paths." Normalized: (a)
// every PATH effect that lives in a REMOTE scope (this schema's own remote
// command child, and anything nested inside it — a `bash -c` the remote
// command itself runs, an xargs/find argv it reconstructs) abstains by
// default, regardless of which policy would otherwise judge it — see
// effectpolicy.remotePathGuard, the single guard that implements this ONCE
// for every path policy. (b) A per-host CATEGORIZED-path override is a
// REQUIRED future capability whose rules.json shape is not yet ruled on;
// this slice implements only the HOOK (evalcontract.Request.RemotePaths /
// PolicyContext.RemotePaths) the guard consults before falling back to (a).
//
// This schema's own job is narrower: turn ssh's own argv into (1) an
// EffectNet describing the CONNECTION itself (judged by the existing
// VettedHosts-driven NetworkAccess policy — production's per-host allowlist,
// internal/rules/ssh's AllowedUsers/read-only-command tables, is the
// analogue) and (2) — when a remote command was given — ONE "shell"-dialect
// ChildInvocation tagged Remote: host, so effectgraph's builder gives it (and
// anything nested inside it) the remote scope (a) above needs. See
// interpreter_ssh.go's sshInterpreter for how the positionals split into
// these two things; the FLAGS below are ordinary schema data the generic
// scan/resolve machinery already knows how to turn into effects.
//
// Verified against THIS HOST's installed ssh client, 2026-09-07: `ssh -V`
// itself is intercepted by this repo's OWN PreToolUse hook (a configured
// production internal/rules/ssh rule Rejects "ssh with no host" before the
// binary ever runs — confirmatory evidence the production rule is live on
// this machine), so provenance is taken instead from the resolved binary's
// nix store path: `/run/current-system/sw/bin/ssh` ->
// `/nix/store/28hprrw9sdi4iarzyxa3r1a22b3dq5pn-openssh-10.5p1/bin/ssh`
// (openssh-10.5p1), cross-checked against `ssh(1)`'s documented option
// table for that release (no live `--help`/`-V` capture was possible on
// this host for the reason above).
const sshProvenance = "OpenSSH 10.5p1 (openssh-10.5p1, /nix/store/28hprrw9sdi4iarzyxa3r1a22b3dq5pn-openssh-10.5p1/bin/ssh, resolved via /run/current-system/sw/bin/ssh), ssh(1) option table for that release; this host 2026-09-07 (ssh -V itself is intercepted by this repo's own configured ssh rule, \"ssh with no host\" — see this schema's own doc comment)"

// sshSchema. Value-taking flags: `-i FILE` is KeyMaterial (a credential
// REFERENCE, not a content read — see cmddesc.KindKeyMaterial's own doc
// comment for why NoReadOfSecretPath must never see it as an ordinary read);
// `-F FILE` is an ordinary PathRead (ssh reads and applies the config
// file's CONTENT); `-E FILE` is a PathTruncate (ssh's own `-E` truncates and
// writes a debug log to it, per ssh(1)). Every other value-taking flag
// (`-p -l -o -J -L -R -D -W -b -c -m -I -Q -S -w -B -e`) is Literal: none of
// their values is a filesystem path THIS schema models (a port, a user
// name, a forward spec, a cipher/MAC/kex-algorithm name, an escape
// character) — `-o` in particular can carry `ProxyCommand=...`, which could
// itself run an arbitrary LOCAL command, but that is a documented,
// out-of-scope limitation this slice does not attempt (matching kubectl's
// own `-o`/`--output` precedent of leaving a flag's value opaque when
// modeling its full semantics is a separate, larger undertaking).
//
// Boolean flags (`-4 -6 -A -a -C -f -G -g -K -k -M -N -n -q -s -T -t -V -v
// -X -x -Y -y`) are inert. `-n` (redirect stdin from /dev/null) is
// deliberately NOT given a TransformNone-style special case despite
// actually suppressing ssh's stdin forwarding: the brief's own flag
// classification lists it among the inert booleans, and modeling its
// interaction with Stdin below is a documented simplification, not an
// oversight — a false "still consumes stdin" costs an extra Abstain
// (Unknown upstream node), never a missed Forbidden.
//
// PositionalsEndOptions is set: the FIRST positional (HOST) ends ssh's own
// flag scanning, so a remote command word that happens to start with `-`
// (`ssh host -rf /`) is never mistaken for an unmodeled ssh flag — the same
// getopt `+`/POSIXLY_CORRECT convention xargs/a wrapper needs
// (PositionalSpec's own doc comment).
//
// Stdin: StdinAlways — ssh forwards the LOCAL terminal/stdin to the remote
// command by default (suppressed only by `-n`, modeled inert above), which
// is exactly the shape NoContentFlowToUnvettedNetwork's upstream Flow-edge
// walk needs to catch `cat ~/.ssh/id_rsa | ssh host 'cat > file'` piping a
// LOCAL secret into the connection (see sshInterpreter's sshConnection doc
// comment on why Direction is Outbound for the same reason). Stdout:
// StdoutContent — the remote command's output returns over the same
// connection and may itself flow onward (`ssh host cat x | tee y`).
var sshSchema = CommandSchema{
	Name:       "ssh",
	Provenance: sshProvenance,
	Flags: map[string]FlagSpec{
		"-i": {Arity: ArityOne, Operand: KeyMaterial},
		"-F": {Arity: ArityOne, Operand: PathRead},
		"-E": {Arity: ArityOne, Operand: PathTruncate},
		"-p": literal1, "-l": literal1, "-o": literal1, "-J": literal1,
		"-L": literal1, "-R": literal1, "-D": literal1, "-W": literal1,
		"-b": literal1, "-c": literal1, "-m": literal1, "-I": literal1,
		"-Q": literal1, "-S": literal1, "-w": literal1, "-B": literal1, "-e": literal1,
		"-4": inert, "-6": inert, "-A": inert, "-a": inert, "-C": inert, "-f": inert,
		"-G": inert, "-g": inert, "-K": inert, "-k": inert, "-M": inert, "-N": inert,
		"-n": inert, "-q": inert, "-s": inert, "-T": inert, "-t": inert, "-V": inert,
		"-v": inert, "-X": inert, "-x": inert, "-Y": inert, "-y": inert,
	},
	Positionals:           PositionalSpec{Rest: Literal},
	Stdin:                 StdinAlways,
	Stdout:                StdoutContent,
	UnknownFlag:           UnknownFlagInsufficient,
	EndOfOptions:          true,
	PositionalsEndOptions: true,
	Interpreter:           "ssh",
}

// ---- scp (slice 3ad, tc-lc8f item 4i; tc-vn5z item 4 follow-up) -----------
//
// scp was DEFERRED by slice 3aa (sshSchema's own doc comment, before this
// edit): its two-or-more positionals need the SAME local/remote-by-colon
// split kubectlCpInterpreter already does for `kubectl cp`
// (interpreter_kubectl.go), plus a THIRD shape kubectl cp never has to
// consider — remote-to-remote (`scp host1:/a host2:/b`, optionally `-3`
// through the local host, which is the default anyway) — and, unlike
// kubectl cp's pod:path operand (left entirely unmodeled), the operator's
// ruling on ssh applies here too, so the remote side must be REPRESENTED,
// not skipped.
//
// Operator ruling this extends (Phillip, 2026-09-07, verbatim, recorded on
// bead tc-vn5z, ssh's own ruling from slice 3aa): "for ssh, abstain for
// paths should be thr default. however, we should allow some way to spexify
// a list of categorized paths." scp follows the SAME model: a remote-side
// path effect abstains by default (the categorized-path hook,
// PolicyContext.RemotePaths, may override it — same mechanism, same
// remotePathGuard, no new policy code); a local-side path effect is judged
// by the ORDINARY local policies exactly like `cp`'s own positionals.
//
// scp's own job is narrower than kubectl cp's: EVERY positional operand
// (not just a fixed source/destination pair) needs its OWN local/remote
// classification — scp accepts one-or-more sources followed by ONE
// destination — and the classification is scp's OWN colon-vs-slash
// convention, matching production's existing classifier
// (internal/rules/ssh/ssh.go's isRemoteToken, cited here as the precedent
// this schema's interpreter mirrors): a `:` that appears before any `/` in
// the token makes it remote ([user@]host:path); a `scp://host[:port]/path`
// URI is remote too (its own `:` after "scp" also precedes the URI's first
// `/`, so the SAME colon-before-slash test classifies it correctly without
// a separate URL-scheme special case — only the host/path EXTRACTION needs
// scheme-aware handling, in scpRemoteHostPath). See interpreter_scp.go's
// scpInterpreter for the full positional dispatch (the FLAGS below are
// ordinary schema data the generic scan/resolve machinery already knows how
// to turn into effects, exactly as sshSchema's flags are).
//
// Verified against THIS HOST's installed scp client, 2026-09-07: `scp`
// itself is intercepted by this repo's OWN PreToolUse hook the same way
// sshSchema's own provenance comment documents for `ssh -V` — every flag
// combination tried (`-s`, `-R`, `-X foo`, `-Z`, no args at all) produced
// the IDENTICAL canned message "scp requires source and destination"
// (internal/rules/ssh's own evaluateSCP Reject text for len(positionals) <
// 2), rather than the real binary's own flag-specific errors — confirmatory
// evidence the production ssh rule intercepts scp too. Provenance is taken
// instead from the resolved binary's nix store path (the SAME OpenSSH
// package ssh itself resolves to: `/run/current-system/sw/bin/scp` ->
// `/nix/store/28hprrw9sdi4iarzyxa3r1a22b3dq5pn-openssh-10.5p1/bin/scp`),
// cross-checked against `scp(1)`'s SYNOPSIS/OPTIONS sections for that
// release (`man scp`, read directly since no live `--help`/usage capture
// was possible for the reason above).
const scpProvenance = "OpenSSH 10.5p1 (same package tree as ssh — /nix/store/28hprrw9sdi4iarzyxa3r1a22b3dq5pn-openssh-10.5p1/bin/scp, resolved via /run/current-system/sw/bin/scp), scp(1) man page for that release; this host 2026-09-07 (scp itself is intercepted by this repo's own configured ssh rule before the binary ever runs, identically to ssh -V — see this schema's own doc comment)"

// scpSchema. Value-taking flags: `-i FILE` is KeyMaterial (a credential
// REFERENCE, not a content read — the identical rationale ssh's own `-i`
// doc comment gives, cmddesc.KindKeyMaterial); `-F FILE` is an ordinary
// PathRead (scp passes it to ssh, which reads and applies the config
// file's CONTENT, exactly like ssh's own `-F`). Every other value-taking
// flag (`-P -o -J -c -l -S -D -X`) is Literal: none of their values is a
// filesystem path this schema models (a port, an ssh_config(5) option
// string, a jump-host spec, a cipher name, a bandwidth limit, a program
// name, an sftp server path, an sftp protocol option) — `-o` carries the
// identical ProxyCommand=... out-of-scope limitation ssh's own `-o` doc
// comment documents. `-D sftp_server_path` and `-S program` both NAME a
// local program/path but are left Literal rather than PathRead for the same
// reason ssh's own `-S`/`-D` (control-socket/forward specs) are Literal:
// modeling every flag's value as a meaningful filesystem access is a
// separate, larger undertaking this slice does not attempt.
//
// Boolean flags (`-3 -4 -6 -A -B -C -O -p -q -R -r -s -T -v`) are inert.
// `-r` (recursive) is deliberately NOT given special tree-write handling:
// per the operator's own delete-access ruling elsewhere in this policy set
// ("breadth is NOT a factor... the class is per path" —
// effectpolicy.DeleteAccess's own doc comment, extended here by the same
// reasoning), a recursive copy's source/destination get the SAME per-path
// classification a single-file copy would. `-3` (copy through the local
// host — scp's OWN default already) and `-R` (copy directly between two
// remote hosts, bypassing the local host) do not change this schema's own
// effects either: whichever mode scp uses, the REMOTE-to-REMOTE shape this
// process observes from its own argv is identical (two remote operands,
// neither touching this filesystem) — `-R`'s actual behavioural difference
// (whether the bytes transit the local host or not) is invisible to a
// static reading of the command line and is not modeled. Deviation from the
// brief that scoped this slice, recorded per its own "do not force, record
// any actual difference": the brief's own flag enumeration omitted `-R` and
// `-s` from the boolean list; both are in THIS host's `scp(1)` SYNOPSIS
// (`-s` has no OPTIONS-section body at all in this release's man page — a
// residual bundle character, most plausibly stale from an older release,
// per the man page's own HISTORY section noting the OpenSSH 9.0 SFTP-by-
// default cutover) and are added here for completeness, modeled inert like
// every other boolean.
//
// PositionalsEndOptions is set for the same reason ssh's own doc comment
// gives: scp's own flag scanning (BSD/OpenSSH getopt, not GNU-permissive)
// stops at the first non-option argument, so a source/destination operand
// that happens to start with `-` is never mistaken for an unmodeled scp
// flag — the operator must escape it (`./-file`), matching real scp/getopt
// behaviour.
//
// No Stdin/Stdout spec: unlike ssh, scp does not forward the local
// terminal's stdin to anything, nor does file content flow over its own
// stdout — it copies named files directly, so both stay their zero values
// (StdinNever/StdoutNone).
var scpSchema = CommandSchema{
	Name:       "scp",
	Provenance: scpProvenance,
	Flags: map[string]FlagSpec{
		"-i": {Arity: ArityOne, Operand: KeyMaterial},
		"-F": {Arity: ArityOne, Operand: PathRead},
		"-P": literal1, "-o": literal1, "-J": literal1, "-c": literal1,
		"-l": literal1, "-S": literal1, "-D": literal1, "-X": literal1,
		"-3": inert, "-4": inert, "-6": inert, "-A": inert, "-B": inert,
		"-C": inert, "-O": inert, "-p": inert, "-q": inert, "-R": inert,
		"-r": inert, "-s": inert, "-T": inert, "-v": inert,
	},
	// Rest: Literal (inert at the generic level) so resolve() does not
	// double-emit an effect for a positional slot scpInterpreter's own
	// per-operand dispatch already handles directly — the identical reason
	// sshSchema's own Positionals is PositionalSpec{Rest: Literal}.
	Positionals:           PositionalSpec{Rest: Literal},
	UnknownFlag:           UnknownFlagInsufficient,
	EndOfOptions:          true,
	PositionalsEndOptions: true,
	Interpreter:           "scp",
}

// ---- build-tool family: verb-dispatch wrappers (slice 3aj, tc-8og1 item 3
// sub-slice 4; tc-vn5z Q4, ruled 2026-09-08) ---------------------------------
//
// just/npm run/devbox run: WRAPPER commands whose real effect depends on
// WHICH VERB (recipe/script) they are told to run — the three targets
// slice 3ag's workspace verb-discovery facet covers (justfile recipes,
// package.json scripts, devbox.json shell.scripts). Each schema below sets
// VerbFamily (schema.go) to route through interpretVerbDispatch
// (interpreter_subcommand.go) instead of the ordinary flag-table scan: the
// verb positional becomes an EffectExec{Family, Operation}, judged by
// effectpolicy.TrustedCheckoutExec's judgeBuildToolVerb (slice 3ai) against
// operator-declared evalcontract.Request.BuildToolVerbs AND
// deletable.DiscoveredVerbs (slice 3ag) — BOTH must agree, per Q3's ruling,
// before a verb Permits.
//
// Everything AFTER the verb positional is opaque to this model — an
// argument to the verb's own body, which this package cannot see the
// contents of — so none of these schemas attempts to further interpret
// trailing tokens, even ones shaped like flags (`just deploy --prod`, `npm
// run test -- --grep=x`); see interpretVerbDispatch's own doc comment for
// why scanGlobal (which stops at the first positional) is the right tool
// for this, unlike the ordinary interleaved scan().
//
// nix (nix run's installable-reference child) and a name-lookup child
// shape are DELIBERATELY OUT OF SCOPE here — tc-8og1 item 3 sub-slice 5's
// job ("nix run installable vetting"), per Q4's own ruling. Nothing below
// forecloses it: a future nix schema can set its own VerbFamily/whatever
// new descriptor sub-slice 5 needs without touching this file's three
// schemas.

// justSchema: `just [OPTIONS] [ARGUMENTS]...` — verified against this
// host's installed just v1.51.0 (`just --version`, `just --help`,
// 2026-09-08). just's own synopsis is broader than this schema models:
// ARGUMENTS may mix `NAME=VALUE` variable OVERRIDES with one or more recipe
// names to run in SEQUENCE (`just foo=bar build test` sets foo, then runs
// build, then test). This schema deliberately models only the simple,
// single-verb-dispatch shape — the first positional is treated as THE
// verb. A leading override token (`foo=bar`) is not specially detected: it
// is captured as Operation="foo=bar", which simply never matches any
// operator-declared (Tool, Verb) pair or any deletable.DiscoveredVerbs
// entry (both require an EXACT name match), so judgeBuildToolVerb always
// abstains on it — a false NEGATIVE (a missed verb capture when overrides
// precede the real recipe name), never a false positive, the same
// fail-safe direction slice 3ag's justfileVerbs already documents for its
// own known limitations. A SECOND/THIRD chained recipe name (`build test`
// above) is likewise not separately captured or judged — out of scope for
// this slice's "make Family/Operation reachable" job.
//
// Bare `just` (zero positionals) does NOT dispatch: real `just` alone
// lists the justfile's recipes (equivalent to `--list`) — modeled here via
// the schema's own top-level Stdout (StdoutMetadata) and Stdin
// (StdinNever), which interpretVerbDispatch falls back to when no verb
// positional is found (and which also covers `just --help`/`just
// --version`/`just -n`, none of which dispatch a recipe either).
//
// Global flags modeled below are boolean/inert or single-literal-value
// ones that provably cannot redirect WHICH justfile or working directory
// verb discovery resolves against. `--set VARIABLE VALUE` is the
// flag-spelled override form (ArityN, both inert — same non-detection
// rationale as the positional override form above).
//
// DELIBERATELY ABSENT (each makes the WHOLE invocation Insufficient rather
// than silently inert or misread), each a distinct trust boundary this
// slice does not cover:
//   - -f/--justfile FILE, -d/--working-directory DIR, --justfile-name
//     NAME, --ceiling DIR: each can point verb discovery at a DIFFERENT
//     justfile than the one deletable.DiscoveredVerbs would find by
//     walking CWD's ancestors — modeling them would need this schema to
//     feed that alternate path back into DiscoveredVerbs, out of scope
//     here.
//   - -c/--command, --shell, --shell-arg, --shell-command, -e/--edit: each
//     names or invokes an ARBITRARY external program verbatim off the
//     command line — the same unvetted-execution-wrapper shape as go
//     test's -exec / go build's -toolexec (registry_breadth.go's own
//     comments on those).
//   - --choose, --chooser, --list/-l, --show/-s, --summary, --usage,
//     --variables, --groups, --changelog, --man, --completions, --dump,
//     --evaluate, --json, --fmt, --init: introspection/formatting MODES
//     that do not dispatch a recipe at all — a genuinely different
//     invocation shape this slice does not model (unlike treefmtSchema,
//     whose entire CLI IS one shape); left for a future slice if agent
//     usage shows they matter.
var justSchema = CommandSchema{
	Name:       "just",
	Provenance: "just 1.51.0 (this host, just --version / just --help), 2026-09-08",
	VerbFamily: "just",
	Flags: map[string]FlagSpec{
		"-n": inert, "--dry-run": inert,
		"-q": inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
		"-h": inert, "--help": inert,
		"-V": inert, "--version": inert,
		"--yes":           inert,
		"--unstable":      inert,
		"--no-deps":       inert,
		"--no-dotenv":     inert,
		"--explain":       inert,
		"--highlight":     inert,
		"--no-highlight":  inert,
		"--allow-missing": inert,
		"--color":         literal1,
		"--command-color": literal1,
		"--alias-style":   literal1,
		"--set":           {Arity: ArityN, Operands: []OperandRole{Literal, Literal}},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// npmSchema: `npm <command> [args]` — subcommand dispatch; this slice only
// models the "run" subcommand's verb-dispatch shape (`npm run <verb>`, the
// target slice 3ag's npmKind covers). Every OTHER npm subcommand (install,
// ci, test, start, publish, ...) is DELIBERATELY ABSENT — falls through to
// interpretSubcommand's own "unmodeled subcommand" Insufficiency, exactly
// like goSchema's own doc/tool/work precedent — not this slice's job
// (`npm test`/`npm start` as their OWN verb-dispatch shortcuts, a plausible
// future extension, are likewise out of scope: the brief names "npm run
// <verb>" specifically). npm's own global config flags (--prefix,
// --registry, ...) can appear before "run" too; leaving Flags empty here
// means any of them also makes the invocation Insufficient, the same
// conservative direction.
//
// Verified against this host's installed npm 11.17.0 (`npm --version`,
// `npm help run`, `npm run --help`), 2026-09-08.
var npmSchema = CommandSchema{
	Name:         "npm",
	Provenance:   "npm 11.17.0 (this host, npm --version / npm help run), 2026-09-08",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"run": npmRunSchema,
	},
}

// npmRunSchema: `npm run <command> [-- <args>]` (npm help run's own
// synopsis) — the SUBCOMMAND schema npmSchema.Subcommands["run"] dispatches
// to via the ordinary interpretSubcommand recursion; its OWN Interpret call
// then routes through interpretVerbDispatch (VerbFamily: "npm", NOT "run"
// — see VerbFamily's own doc comment, schema.go: it must match
// npmKind.Name/evalcontract.VerbScopedApproval.Tool, not the dispatching
// subcommand key). Positional arguments after <command> (and everything
// after a literal `--`) are passed to the script verbatim — opaque to this
// model, per interpretVerbDispatch's own contract.
//
// Bare `npm run` (zero positionals) lists the package's scripts —
// StdoutMetadata, the same bare-invocation fallback shape as justSchema.
//
// Flags modeled are npm run's OWN documented options (`npm run --help`,
// this host): -w/--workspace NAME (repeatable, literal), --workspaces,
// --include-workspace-root, --if-present, --ignore-scripts,
// --foreground-scripts (all boolean/inert). --script-shell SHELL is
// DELIBERATELY ABSENT: it substitutes the interpreter that runs the
// script, an arbitrary external program named on the command line — the
// same unvetted-execution-wrapper shape justSchema's --shell exclusion
// documents.
var npmRunSchema = CommandSchema{
	Name:       "run",
	Provenance: "npm 11.17.0 (this host, npm help run / npm run --help), 2026-09-08",
	VerbFamily: "npm",
	Flags: map[string]FlagSpec{
		"-w": literal1, "--workspace": literal1,
		"--workspaces":             inert,
		"--include-workspace-root": inert,
		"--if-present":             inert,
		"--ignore-scripts":         inert,
		"--foreground-scripts":     inert,
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// devboxSchema: `devbox <command> [args]` — subcommand dispatch; this
// slice only models "run" (`devbox run <script>`, the target slice 3ag's
// devboxKind covers), the same scope decision npmSchema makes for npm.
//
// NOT VERIFIED LIVE: devbox is not installed on this host (2026-09-08,
// `which devbox` finds nothing) — modeled conservatively from devbox.json's
// well-documented `shell.scripts`/`devbox run <script>` CLI shape (the
// same shape slice 3ag's devboxVerbs already parses from devbox.json)
// rather than a live `devbox --help` capture. Flags are therefore left
// minimal/empty here deliberately — narrower coverage than justSchema's/
// npmSchema's, not a missing case: an unmodeled devbox flag still fails
// closed to Insufficient, never guesses.
var devboxSchema = CommandSchema{
	Name:         "devbox",
	Provenance:   "NOT VERIFIED LIVE (devbox not installed on this host, 2026-09-08); modeled from devbox.json's documented shell.scripts / `devbox run` CLI shape",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"run": devboxRunSchema,
	},
}

// devboxRunSchema: `devbox run [flags] <script> [-- args]` — VerbFamily:
// "devbox" (matching devboxKind.Name), the same npmRunSchema shape. Only
// `-q`/`--quiet` is modeled (a documented, unambiguously inert devbox
// global flag); everything else is deliberately left unmodeled given the
// no-live-verification caveat above (devboxSchema's own doc comment).
var devboxRunSchema = CommandSchema{
	Name:       "run",
	Provenance: "NOT VERIFIED LIVE (devbox not installed on this host, 2026-09-08)",
	VerbFamily: "devbox",
	Flags: map[string]FlagSpec{
		"-q": inert, "--quiet": inert,
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- nix run: the installable-reference wrapper (slice 3ak, tc-8og1 item 3
// sub-slice 5 of 5, the FINAL sub-slice of the build-tool family design;
// tc-vn5z Q1-Q4, ruled 2026-09-08) -------------------------------------------
//
// `nix run <installable> [args...]` — the shape Q4's ruling named
// explicitly as needing NEW descriptor work beyond interpreter_subcommand.go's
// plain argv recursion ("installable-reference" child), because an
// installable is a FLAKE REFERENCE (`.`, `.#foo`, `nixpkgs#hello`,
// `github:owner/repo#app`, ...), not a simple project-defined verb NAME the
// way a justfile recipe or a package.json script is.
//
// WHAT TURNED OUT TO NEED NEW WORK, AND WHAT DID NOT (recorded here because
// it differs from what the brief anticipated): CAPTURING the installable
// text needs NO new cmddesc machinery at all — `nix run <installable>` is,
// argv-shape-wise, IDENTICAL to `just <verb>`: one opaque positional token
// after the wrapper's own global flags, everything after it inert. The
// EXISTING VerbFamily/interpretVerbDispatch recursion (slice 3aj) captures
// it verbatim as Effect.Operation without modification. What DID need new
// work: (1) DefaultVerb (schema.go, interpreter_subcommand.go) — nix run's
// BARE form does not merely list things the way just/npm run/devbox run's
// bare forms do; it still EXECUTES (`nix run` alone behaves exactly like
// `nix run .`, verified live against this host's nix, see
// nixRunSchema.DefaultVerb's own comment below) — a genuine gap in the
// verb-dispatch recursion, since every prior VerbFamily schema's bare form
// was safe-by-construction; (2) classifying the CAPTURED installable text as
// a local vs. non-local flake reference — this is NOT a cmddesc/
// interpretation-layer concern (the text is captured as-is regardless), it
// is a POLICY-layer concern (effectpolicy.judgeBuildToolVerb's new
// VerbClassInstallableReference branch, evalcontract/contract.go), because
// deciding what "trusted" means for a given installable spelling is exactly
// the operator-declared-eligibility question the rest of the build-tool
// family policy already lives in, not a parsing concern this package should
// own.
//
// nixSchema only models the "run" subcommand (the target this sub-slice's
// job names); every other nix subcommand (build, develop, shell, flake,
// eval, ...) is DELIBERATELY ABSENT — falls through to interpretSubcommand's
// own "unmodeled subcommand" Insufficiency, the SAME scope decision
// npmSchema/devboxSchema make for their own tool's much larger CLI surface.
// `nix build`/`nix flake check`/`nix eval` etc. are read-plus-daemon-side-
// store-writes territory (tc-vn5z's own 2026-09-07 design note, item 3) —
// a DIFFERENT effect shape from a wrapper's verb dispatch, and explicitly
// named there as a gap this sub-slice does not close (no declared
// deletable.Kind for the Nix store exists either). `nix shell ... -c <cmd>`
// is ALSO deliberately out of scope: production's own internal/rules/nix
// already handles it by a DIFFERENT mechanism entirely (recursively
// evaluating the inner `-c` command as a shell-dialect child, the same
// shape `nix develop -c`/`nix-shell --run` use) — not installable vetting
// at all, since `nix shell`'s installable only provisions a PATH, it is not
// itself executed. Folding it into this slice would conflate two distinct
// trust questions; a future slice can add it without touching anything
// here.
//
// Verified against this host's installed nix (Nix) 2.34.8 (`nix --version`,
// `nix run --help`), 2026-09-08.
var nixSchema = CommandSchema{
	Name:       "nix",
	Provenance: "nix (Nix) 2.34.8 (this host, nix --version / nix run --help), 2026-09-08",
	// Deliberately EMPTY, like npmSchema's own top-level table: nix's global
	// flags (-L/--print-build-logs, -v/--verbose, --debug, --offline,
	// --refresh, --option, --arg/--argstr/--expr/--file, ...) can appear
	// BEFORE the subcommand, and several of them (--expr/--file reinterpret
	// what "installable" even MEANS; --offline/--refresh change fetch
	// behaviour) are exactly the trust-relevant surface this sub-slice does
	// not model — any of them makes the whole invocation Insufficient, the
	// conservative direction.
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"run": nixRunSchema,
	},
}

// nixRunSchema: `nix run [option...] installable args...` (nix run --help's
// own synopsis, this host's nix 2.34.8) — VerbFamily: "nix" (matching
// evalcontract.VerbScopedApproval.Tool). Positional 0 after nix run's own
// (here: zero) modeled flags is the INSTALLABLE, captured verbatim as
// Effect.Operation by interpretVerbDispatch (slice 3aj), unmodified —
// classifying it as local/non-local happens at the POLICY layer (see this
// file's own "---- nix run ----" section doc comment above).
//
// DefaultVerb: "." — `nix run` with ZERO positional arguments resolves and
// EXECUTES the current directory's own flake default app/package, exactly
// as if `.` had been given explicitly (nix run --help's own "Run the
// default app from the current directory" example; confirmed live,
// 2026-09-08: `nix run` alone in a directory with no flake.nix fails
// immediately with "could not find a flake.nix file", proving installable
// resolution — not a safe listing — is what actually happens). This is WHY
// nixRunSchema needs DefaultVerb where justSchema/npmRunSchema/
// devboxRunSchema do not: their bare forms only LIST recipes/scripts (an
// inert introspection, safely left to the ordinary top-level
// Stdout/ImplicitEffects fallback); nix run's bare form is not analogous.
//
// Deliberately EMPTY Flags, the SAME conservative choice as the parent
// nixSchema and for the SAME reason: essentially every documented `nix run`
// flag either changes what "installable" resolves to (--impure allows
// mutable/impure evaluation; --override-input/--override-flake/
// --inputs-from redirect a flake input or registry entry to something
// else; --expr/--file reinterpret installables as attribute paths against
// arbitrary Nix expression text) or changes fetch/trust behaviour
// (--offline/--refresh/--repair) — none of them is a case this sub-slice's
// deliberately narrow scope (bare `.`/`.#attr` local references only, see
// evalcontract.VerbClassInstallableReference's own doc comment) can vouch
// for safely; any of them fails the whole invocation closed.
//
// Stdin/Stdout are deliberately left StdinNever/StdoutNone (the zero
// values) rather than StdoutMetadata: unlike just/npm run/devbox run's bare
// listing (whose stdout genuinely IS just names — safe metadata),
// nix run's stdout — bare OR with an explicit installable — is whatever
// the EXECUTED PROGRAM writes, unknowable statically and not bounded to
// metadata. This schema declares no stdio effect at all for either shape,
// matching interpretVerbDispatch's own explicit-verb path (which likewise
// never calls stdio() — see its own doc comment).
var nixRunSchema = CommandSchema{
	Name:         "run",
	Provenance:   "nix (Nix) 2.34.8 (this host, nix run --help), 2026-09-08",
	VerbFamily:   "nix",
	DefaultVerb:  ".",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}
