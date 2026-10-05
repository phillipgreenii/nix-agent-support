package cmddesc

// pg2-cjfpy.2: the CLIs the agent-support marketplace plugins' skills,
// commands and agents instruct (bgcheck — bgrun is unwrapped by cmdparse — integrate-branch-support,
// wtdone, handoff-create, session-mode, pg-go-mutate, pg-ccaudit, pg-wi-flow,
// pg-hooks, prek, bats, gh, pn workspace, claude-extended-tool-approver, the
// plugins' own helper scripts) plus the ancillary read-only tools those
// skills call (rg, fd, du, lsof, launchctl list, man) and the `nix build` /
// `nix flake` verbs of their landing and validation recipes.
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim):
// "any command which is supposed to work as part of a skill should be
// autoapproved". Every schema here still APPROVES only the shape the skills
// use: an unlisted flag is insufficient (UnknownFlagInsufficient everywhere
// below), and a subcommand the plugins do not instruct is simply absent
// (unmodeled subcommand → abstain). Where the OLD engine (internal/rules/*,
// frozen, ADR 0075 R1) already carried a reviewed classification for a tool,
// the schema ports that classification and names it; flags and synopses were
// verified against the host's installed binaries (2026-10-05) and are cited
// per fact in internal/embeddedspecs/data/*.json.
//
// Effects the tools perform that this model cannot see are stated, not
// hidden: a first-party tool that writes only its OWN state (session-mode's
// record, pg-ccaudit's index and ledger, bgrun's job directory) declares no
// path effect for it, exactly as the old engine's alwaysSafe entries did.

// flagBoth registers one flag under both Go-flag spellings (`-name` and
// `--name`): the pg-ccaudit subcommands are Go `flag`-package tools, which
// accept either dash count for every flag.
func flagBoth(m map[string]FlagSpec, name string, spec FlagSpec) {
	m["-"+name] = spec
	m["--"+name] = spec
}

// readsBeads / writesBeads are the implicit remote effects of a wrapper whose
// whole effect is a read of / bookkeeping write to the beads database (the
// same resource bdRemote uses), so RemoteMutation judges them identically.
var (
	readsBeads  = ImplicitEffect{Role: Remote("read"), Target: "beads"}
	writesBeads = ImplicitEffect{Role: Remote("tracker-write"), Target: "beads"}
)

// ---- bgrun / bgcheck (packages/bg-tools) -------------------------------

// bgcheckSchema: `bgcheck [OPTIONS] [NAME]` — "Strictly read-only (ps +
// tail)". Output includes the tail of the job's log, so it is content.
// Ported from the old engine's safecmds alwaysSafe entry ("a read-only
// wrapper over ps + tail designed for blanket approval").
var bgcheckSchema = CommandSchema{
	Name:       "bgcheck",
	Provenance: "bgcheck --help (bg-tools; 'Strictly read-only (ps + tail)'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-n": literal1, "--lines": literal1,
		"-d": {Arity: ArityOne, Operand: PathRead}, "--dir": {Arity: ArityOne, Operand: PathRead},
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- integrate-branch-support (packages/integrate-branch-support) ------

// integrateBranchSupportSchema: `integrate-branch-support [--facts |
// --prek-branch-diff | --bundle-refresh <old-sha>]`. The bare and --facts
// forms only read git plumbing (old engine: "no mutation surface at all",
// safecmds). --prek-branch-diff runs `pg-hooks run pre-land <FB>` (the repo's
// own hooks) and --bundle-refresh starts the clone's hook-bundle `reinstall`
// through bgrun: both execute checkout code, which the KindExec implicit
// effect states so TrustedCheckoutExec judges the working directory.
var integrateBranchSupportSchema = CommandSchema{
	Name:       "integrate-branch-support",
	Provenance: "integrate-branch-support --help ('Advisory: report a repo's integration facts + recommended strategy'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"--facts":            inert,
		"--prek-branch-diff": inert,
		"--bundle-refresh":   literal1,
		"-h":                 inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals: PositionalSpec{Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: "."},
		{Role: Exec, Target: "integrate-branch-support", WhenFlags: []string{"--prek-branch-diff", "--bundle-refresh"}},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- wtdone (packages/wtdone) -------------------------------------------

// wtdoneSchema: `wtdone <bead-or-branch> [--cc <canonical-dir>]` — guarded
// worktree teardown: a liveness guard (refuses while a process is anchored in
// the worktree), then `git worktree remove` (never forced; refuses a dirty
// worktree) and `git branch -d` (never -D). The worktree path is resolved
// from the branch INSIDE the tool, so no path operand exists to model.
// Ported from the old engine's safecmds alwaysSafe entry (pg2-hel4i): "safe-
// listed anyway because both effects are liveness-guarded and bounded to what
// a plain, non-force branch delete allows".
var wtdoneSchema = CommandSchema{
	Name:       "wtdone",
	Provenance: "wtdone --help ('Guarded worktree teardown'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"--cc": {Arity: ArityOne, Operand: PathRead},
		"-h":   inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- handoff-create (packages/handoff-create) ---------------------------

// handoffCreateSchema: `handoff-create (--attended|--unattended)
// --session-id ID --title TEXT --body-file FILE [--label L]... [--actor ID]
// [--bd-dir DIR]` — one `bd create` (P0 handoff bead) plus a read-back. A
// beads bookkeeping write, plus a read of --body-file (copied into the bead
// body, so held to the path-read policy, as the old engine's
// handoffCreatePathIssue did) and of --bd-dir (the tracker root handed to
// `bd -C`).
var handoffCreateSchema = CommandSchema{
	Name:       "handoff-create",
	Provenance: "handoff-create --help ('Create a handoff bead correctly'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"--attended": inert, "--unattended": inert,
		"--session-id": literal1, "--title": literal1, "--label": literal1, "--actor": literal1,
		"--body-file": {Arity: ArityOne, Operand: PathRead},
		"--bd-dir":    {Arity: ArityOne, Operand: PathRead},
		"-h":          inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals:     PositionalSpec{Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{writesBeads},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// ---- session-mode (packages/session-mode) --------------------------------

// sessionModeSchema: `session-mode start KIND [--detail TEXT] [--force]`,
// `set-status running|stopping|finished [--handoff-bead ID]`, `show`. Each
// "only starts/refreshes/reads a local status-line state file ... pure local
// state, no filesystem access outside that one record" (old engine, safecmds
// alwaysSafe, pg2-hel4i); the record's location is chosen by the tool, not by
// an argument, so no path effect is declared. The internal `hook
// session-end` form (fed by Claude Code's SessionEnd hook, never by an agent)
// is deliberately absent.
var sessionModeSchema = CommandSchema{
	Name:       "session-mode",
	Provenance: "session-mode --help ('Track which named mode is running in THIS Claude Code session'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"start": {
			Name:       "start",
			Provenance: "session-mode --help, start KIND [--detail TEXT] [--force]",
			Flags: map[string]FlagSpec{
				"--detail": literal1, "--force": inert,
			},
			Positionals:  PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"set-status": {
			Name:       "set-status",
			Provenance: "session-mode --help, set-status running|stopping|finished [--handoff-bead ID]",
			Flags: map[string]FlagSpec{
				"--handoff-bead": literal1,
			},
			Positionals:  PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"show": {
			Name:         "show",
			Provenance:   "session-mode --help, show",
			Flags:        map[string]FlagSpec{},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// ---- pg-go-mutate (packages/pg-go-mutate) --------------------------------

// pgGoMutateSchema: `pg-go-mutate [PATH] [--tags L] [--json] [--timeout S]
// [--workers N] [--keep-report]` — a mutation-testing diagnostic: for each
// mutant it rewrites a source file under PATH, runs that package's tests and
// restores it ("reads the live worktree"). So PATH is a PathModify (default
// "."), and the tests it runs are checkout code (KindExec). Ported from the
// old engine's buildtools approved-by-basename entry (pg2-xu4aq).
var pgGoMutateSchema = CommandSchema{
	Name:       "pg-go-mutate",
	Provenance: "pg-go-mutate --help ('report which assertions a Go package's tests are missing'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"--tags": literal1, "--json": inert, "--timeout": literal1, "--workers": literal1,
		"--keep-report": inert,
		"-h":            inert, "--help": inert,
	},
	Positionals: PositionalSpec{
		Leading:         []OperandRole{PathModify},
		LeadingOptional: true,
		Rest:            Unmodeled,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: ".", WhenNoPositionals: true},
		{Role: Exec, Target: "pg-go-mutate"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- pg-ccaudit (packages/pg-ccaudit) ------------------------------------

// pgCcauditSub is one pg-ccaudit subcommand with ITS OWN flag set (each
// subcommand's `-h` lists a different one), spelled both ways — Go's `flag`
// package accepts `-x` and `--x` alike. The path-taking flags (-db, -root,
// -gold, -ledger, -feedback, -memory-root) are deliberately absent from every
// subcommand: a caller-chosen database/ledger/gold path is a write to an
// arbitrary file, so they stay unmodeled → abstain.
func pgCcauditSub(name string, stdout StdoutKind, valueFlags, boolFlags []string) CommandSchema {
	m := map[string]FlagSpec{}
	for _, n := range valueFlags {
		flagBoth(m, n, literal1)
	}
	for _, n := range boolFlags {
		flagBoth(m, n, inert)
	}
	return CommandSchema{
		Name:         name,
		Provenance:   "pg-ccaudit " + name + " -h, this host 2026-10-05",
		Flags:        m,
		Positionals:  PositionalSpec{Rest: Literal},
		Stdin:        StdinNever,
		Stdout:       stdout,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
}

// pgCcauditCensusValueFlags are the value flags the census tiers (candidates,
// classify, report, evaluate, gold) share.
var pgCcauditCensusValueFlags = []string{"batch", "churn-min", "classifier", "format", "max", "retry-gap", "signals", "since", "until"}

// pgCcauditSchema: the read-only band of `pg-ccaudit`'s own usage text
// ("MISTAKE CENSUS (read-only; the three tiers, in order)": candidates,
// classify, report, evaluate, gold, cost, plus status/query/queries/schema/
// version). classify/report spend model calls and `gold seed|sample` grow the
// gold-set cache, but all of it stays inside pg-ccaudit's own SQLite index
// and cost ledger (old engine, internal/rules/pgccaudit, pg2-xu4aq). `ingest`
// is DELIBERATELY ABSENT: the tool-error-waste-review skill requires the
// operator to authorize it ("do not run `pg-ccaudit ingest` yourself unless
// the operator asks"), which the old engine recorded as Ask — a ruling
// collision this schema does not override (unmodeled subcommand → abstain).
var pgCcauditSchema = CommandSchema{
	Name:         "pg-ccaudit",
	Provenance:   "pg-ccaudit --help ('index Claude Code transcripts into SQLite and query them'), this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"status":     pgCcauditSub("status", StdoutMetadata, nil, []string{"json"}),
		"query":      pgCcauditSub("query", StdoutContent, []string{"format", "since", "until"}, []string{"no-staleness"}),
		"queries":    pgCcauditSub("queries", StdoutMetadata, nil, []string{"verbose"}),
		"schema":     pgCcauditSub("schema", StdoutMetadata, nil, nil),
		"version":    pgCcauditSub("version", StdoutMetadata, nil, nil),
		"candidates": pgCcauditSub("candidates", StdoutContent, pgCcauditCensusValueFlags, nil),
		"classify":   pgCcauditSub("classify", StdoutContent, pgCcauditCensusValueFlags, nil),
		"report":     pgCcauditSub("report", StdoutContent, pgCcauditCensusValueFlags, []string{"no-evaluation"}),
		"evaluate":   pgCcauditSub("evaluate", StdoutContent, pgCcauditCensusValueFlags, nil),
		"gold":       pgCcauditSub("gold", StdoutContent, append(append([]string(nil), pgCcauditCensusValueFlags...), "sample"), nil),
		"cost":       pgCcauditSub("cost", StdoutContent, []string{"since", "until"}, []string{"json"}),
	},
}

// ---- pg-wi-flow (packages/pg-wi-flow) ------------------------------------

// pgWiFlowVerb is one pg-wi-flow verb: every one routes its bead access
// through the tracker adapter (`bd`), so its whole effect is a read of, or a
// bookkeeping write to, the beads database — the same resource and
// operations bdRemote models. flags are the verb's own options (all
// value-taking options carry free text or ids, never paths).
func pgWiFlowVerb(name string, write bool, valueFlags, boolFlags []string) CommandSchema {
	flags := map[string]FlagSpec{"-h": inert, "--help": inert}
	for _, f := range valueFlags {
		flags[f] = literal1
	}
	for _, f := range boolFlags {
		flags[f] = inert
	}
	effect := readsBeads
	if write {
		effect = writesBeads
	}
	return CommandSchema{
		Name:            name,
		Provenance:      "pg-wi-flow --help, " + name + " (packages/pg-wi-flow/pg-wi-flow.sh), this repo 2026-10-05",
		Flags:           flags,
		Positionals:     PositionalSpec{Rest: Literal},
		ImplicitEffects: []ImplicitEffect{effect},
		Stdin:           StdinNever,
		Stdout:          StdoutContent,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// pgWiFlowSchema: the verbs the /drain skill and the dispatcher / worker /
// resolver agents instruct (next, list, explain, claim, release, round,
// escalate, resolve) plus the read verbs beside them (query, history,
// duplicates, docs, context). The other write verbs (annotate, advance,
// create-child, merge, close, close-duplicate, record-verdict) are not
// instructed by any plugin and stay unmodeled.
var pgWiFlowSchema = CommandSchema{
	Name:       "pg-wi-flow",
	Provenance: "pg-wi-flow --help ('bead-workflow CLI framework'), packages/pg-wi-flow/pg-wi-flow.sh, this repo 2026-10-05",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"query":      pgWiFlowVerb("query", false, []string{"--stage"}, []string{"--attended"}),
		"list":       pgWiFlowVerb("list", false, []string{"--stage", "--days", "--reserved-hours"}, []string{"--attended", "--questions", "--unpooled", "--stale"}),
		"explain":    pgWiFlowVerb("explain", false, nil, nil),
		"history":    pgWiFlowVerb("history", false, nil, nil),
		"duplicates": pgWiFlowVerb("duplicates", false, nil, nil),
		"docs":       pgWiFlowVerb("docs", false, nil, nil),
		"context":    pgWiFlowVerb("context", false, []string{"--role", "--concern", "--gap"}, []string{"--render"}),
		"next":       pgWiFlowVerb("next", true, []string{"--stage", "--id"}, nil),
		"claim":      pgWiFlowVerb("claim", true, nil, nil),
		"release":    pgWiFlowVerb("release", true, nil, nil),
		"round":      pgWiFlowVerb("round", true, nil, nil),
		"escalate":   pgWiFlowVerb("escalate", true, []string{"--question", "--trigger"}, nil),
		"resolve":    pgWiFlowVerb("resolve", true, []string{"--decision", "--rationale", "--answer", "--reason-code", "--defer"}, []string{"--abandon"}),
	},
}

// ---- pg-hooks / prek (the per-clone hook bundle) -------------------------

func hookSub(name string, flags map[string]FlagSpec, pos PositionalSpec, implicit []ImplicitEffect, stdout StdoutKind) CommandSchema {
	if flags == nil {
		flags = map[string]FlagSpec{}
	}
	return CommandSchema{
		Name:            name,
		Provenance:      "pg-hooks --help, " + name + ", this host 2026-10-05",
		Flags:           flags,
		Positionals:     pos,
		ImplicitEffects: implicit,
		Stdin:           StdinNever,
		Stdout:          stdout,
		UnknownFlag:     UnknownFlagInsufficient,
		// EndOfOptions is deliberately false for the hook-running verbs: a
		// literal `--` hands everything after it to prek ("-- prek-args"),
		// which includes `--config <file>` (hooks from an arbitrary file), so
		// it must stay an unknown flag → abstain. status/list/explain have no
		// such passthrough but share the shape.
	}
}

// pgHooksSchema: `pg-hooks status [--porcelain] | list | explain <stage> |
// run <stage> [files...|--all-files] | run pre-land [<ref>] | fix`. status/
// list/explain read the clone's bundle pointer; run executes the repo's
// configured hooks over files (checkout code, KindExec); fix applies the
// fixers to the staged files and restages them (a working-tree + index
// write). Ported from the old engine's buildtools entry (pg2-pla9d.17):
// "pg-hooks has exactly five ... approval is by basename".
var pgHooksSchema = CommandSchema{
	Name:       "pg-hooks",
	Provenance: "pg-hooks --help ('inspect and run the per-clone hook bundle'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: false,
	Subcommands: map[string]CommandSchema{
		"status": hookSub("status", map[string]FlagSpec{"--porcelain": inert},
			PositionalSpec{Rest: Unmodeled}, []ImplicitEffect{{Role: PathRead, Target: ".git"}}, StdoutMetadata),
		"list": hookSub("list", nil,
			PositionalSpec{Rest: Unmodeled}, []ImplicitEffect{{Role: PathRead, Target: ".git"}}, StdoutMetadata),
		"explain": hookSub("explain", nil,
			PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled}, []ImplicitEffect{{Role: PathRead, Target: ".git"}}, StdoutMetadata),
		"run": hookSub("run", map[string]FlagSpec{"--all-files": inert},
			PositionalSpec{Leading: []OperandRole{Literal}, Rest: PathRead},
			[]ImplicitEffect{{Role: PathRead, Target: "."}, {Role: Exec, Target: "pg-hooks run"}}, StdoutContent),
		"fix": hookSub("fix", nil,
			PositionalSpec{Rest: Unmodeled},
			[]ImplicitEffect{{Role: PathModify, Target: "."}, {Role: PathModify, Target: ".git"}, {Role: Exec, Target: "pg-hooks fix"}}, StdoutContent),
	},
}

// prekSchema: `prek run [--files F... | --all-files | -a | --from-ref R
// --to-ref R ...] [HOOK|PROJECT]...` and `prek list` — the hook runner behind
// pg-hooks. `run` executes the repo's configured hooks over the selected
// files (checkout code, KindExec). `--config/-c` (hooks from an arbitrary
// file) and `--log-file` are unmodeled → abstain. `--files` is modeled inert
// so the file names that follow it resolve as the verb's PathRead positionals
// (its only operand form). Old engine: buildtools approved-by-basename.
var prekSchema = CommandSchema{
	Name:       "prek",
	Provenance: "prek --help ('A fast Git hook manager written in Rust'), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert, "-V": inert, "--version": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"run": {
			Name:       "run",
			Provenance: "prek run --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"-a": inert, "--all-files": inert, "--files": inert,
				"-s": literal1, "--from-ref": literal1, "-o": literal1, "--to-ref": literal1,
				"--last-commit": inert, "--stage": literal1, "--skip": literal1,
				"--show-diff-on-failure": inert, "--fail-fast": inert, "--dry-run": inert,
				"-d": {Arity: ArityOne, Operand: PathRead}, "--directory": {Arity: ArityOne, Operand: PathRead},
				"-C": {Arity: ArityOne, Operand: CommandDir}, "--cd": {Arity: ArityOne, Operand: CommandDir},
				"--color": literal1, "--no-progress": inert, "--refresh": inert,
				"-q": inert, "--quiet": inert, "-v": inert, "--verbose": inert,
				"-h": inert, "--help": inert,
			},
			Positionals: PositionalSpec{Rest: PathRead},
			ImplicitEffects: []ImplicitEffect{
				{Role: PathRead, Target: "."},
				{Role: Exec, Target: "prek run"},
			},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"list": {
			Name:       "list",
			Provenance: "prek list --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"-h": inert, "--help": inert,
			},
			Positionals:     PositionalSpec{Rest: Unmodeled},
			ImplicitEffects: []ImplicitEffect{{Role: PathRead, Target: "."}},
			Stdin:           StdinNever,
			Stdout:          StdoutMetadata,
			UnknownFlag:     UnknownFlagInsufficient,
			EndOfOptions:    true,
		},
	},
}

// ---- bats ---------------------------------------------------------------

// batsSchema: `bats [options] <tests>...` — runs the named bats files/
// directories: checkout code (KindExec). Options modeled are the output and
// selection ones; `--formatter` (a custom formatter PATH is executed) and
// every unlisted flag are unmodeled → abstain.
var batsSchema = CommandSchema{
	Name:       "bats",
	Provenance: "bats --help (Bats 1.x), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-r": inert, "--recursive": inert, "-t": inert, "--tap": inert,
		"-T": inert, "--timing": inert, "--print-output-on-failure": inert,
		"--show-output-of-passing-tests": inert, "--verbose-run": inert,
		"-j": literal1, "--jobs": literal1, "-f": literal1, "--filter": literal1,
		"--no-parallelize-across-files": inert, "--no-parallelize-within-files": inert,
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals: PositionalSpec{Rest: PathRead},
	ImplicitEffects: []ImplicitEffect{
		{Role: Exec, Target: "bats"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- gh -----------------------------------------------------------------

// ghPrReadSchema: the pull-request READ verbs (view, list, status, diff,
// checks): a read of the "github" remote resource. Each verb carries only the
// flags its own `gh pr <verb> --help` lists (valueFlags take one argument,
// boolFlags none); `-R/--repo` is inherited by all of them. The browser-
// opening `-w/--web`, `checks --watch` and `diff --allow-escape-sequences`
// are unmodeled → abstain. Ported from the old engine's gh rule
// (readOnlyPR).
func ghPrReadSchema(name string, valueFlags, boolFlags []string) CommandSchema {
	flags := map[string]FlagSpec{"-R": literal1, "--repo": literal1, "--help": inert}
	for _, f := range valueFlags {
		flags[f] = literal1
	}
	for _, f := range boolFlags {
		flags[f] = inert
	}
	return CommandSchema{
		Name:            name,
		Provenance:      "gh pr " + name + " --help, this host 2026-10-05",
		Flags:           flags,
		Positionals:     PositionalSpec{Rest: Literal},
		ImplicitEffects: []ImplicitEffect{{Role: Remote("read"), Target: "github"}},
		Stdin:           StdinNever,
		Stdout:          StdoutContent,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// ghPrCreateSchema: `gh pr create`. DRAFT-FIRST landing (operator ruling,
// Phillip, 2026-07-30, pg2-4yy4r item 2 / pg2-25oru): `--draft` upgrades the
// "pr-create" remote operation to "pr-create-draft" (Permitted); WITHOUT it
// the operation stays "pr-create", which RemoteMutation Forbids — exactly the
// old engine's table (create --draft Approve; create without --draft Reject).
// `--web` (the human picks draft-or-not in a browser) is unmodeled → abstain.
// The title/body text flags are Message operands; `-F/--body-file` is a path
// read.
var ghPrCreateSchema = CommandSchema{
	Name:       "create",
	Provenance: "gh pr create --help, this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-d":      {Transform: EffectTransform{Kind: TransformRetargetRemote, From: "pr-create", To: "pr-create-draft"}},
		"--draft": {Transform: EffectTransform{Kind: TransformRetargetRemote, From: "pr-create", To: "pr-create-draft"}},
		"-H":      literal1, "--head": literal1, "-B": literal1, "--base": literal1,
		"-t": {Arity: ArityOne, Operand: Message}, "--title": {Arity: ArityOne, Operand: Message},
		"-b": {Arity: ArityOne, Operand: Message}, "--body": {Arity: ArityOne, Operand: Message},
		"-F": {Arity: ArityOne, Operand: PathRead}, "--body-file": {Arity: ArityOne, Operand: PathRead},
		"-f": inert, "--fill": inert, "--fill-first": inert, "--fill-verbose": inert,
		"-l": literal1, "--label": literal1, "-r": literal1, "--reviewer": literal1,
		"-a": literal1, "--assignee": literal1, "-m": literal1, "--milestone": literal1,
		"-p": literal1, "--project": literal1, "--no-maintainer-edit": inert,
		"-R": literal1, "--repo": literal1, "--help": inert,
	},
	Positionals:     PositionalSpec{Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{{Role: Remote("pr-create"), Target: "github"}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// ghSchema is the ONE registered `gh` schema. It holds pg2-cjfpy.2's `gh pr
// view|list|status|diff|checks|create` and, folded in from
// registry_ziprecruiter.go (pg2-cjfpy.4), `gh auth status`, `gh extension
// list` and the local `gh stack` verbs (zrGhSubcommands). `gh pr ready`
// (Abstain, pg2-psiqh) and `gh pr merge` (human-only; the integrate-branch
// handler MUST NOT run it) are deliberately absent: ruled human steps, so a
// ruling collision this schema does not override.
var ghSchema = CommandSchema{
	Name:         "gh",
	Provenance:   "gh --help (GitHub CLI), this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"pr": {
			Name:       "pr",
			Provenance: "gh pr --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"-R": literal1, "--repo": literal1, "--help": inert,
			},
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
			Subcommands: map[string]CommandSchema{
				"view": ghPrReadSchema("view", []string{"--json", "-q", "--jq", "-t", "--template"}, []string{"-c", "--comments"}),
				"list": ghPrReadSchema("list", []string{
					"--json", "-q", "--jq", "-t", "--template", "-a", "--assignee", "-A", "--author",
					"-B", "--base", "-H", "--head", "-l", "--label", "-L", "--limit", "-S", "--search", "-s", "--state", "--app",
				}, []string{"-d", "--draft"}),
				"status": ghPrReadSchema("status", []string{"--json", "-q", "--jq", "-t", "--template"}, []string{"-c", "--conflict-status"}),
				"diff":   ghPrReadSchema("diff", []string{"--color", "-e", "--exclude"}, []string{"--name-only", "--patch"}),
				"checks": ghPrReadSchema("checks", []string{"--json", "-q", "--jq", "-t", "--template"}, []string{"--required"}),
				"create": ghPrCreateSchema,
			},
		},
		"auth":      zrGhSubcommands["auth"],
		"extension": zrGhSubcommands["extension"],
		"stack":     zrGhSubcommands["stack"],
	},
}

// ---- pn workspace: NOT defined here ----------------------------------------
//
// pn / pnwf are registered once, in registry_repo_base.go (pg2-cjfpy.3, the
// repo-base plugin sweep). The agent-support skills' pn forms (session-wrapup,
// integrate-branch: push, update, apply, doctor, workforest list|prune) are
// covered by that schema; the two forms it deliberately omits stay abstain here
// too: workforest remove (pg2-4zyqf: NOT relieved by the 2026-09-17 operator
// ruling) and doctor --fix (session-wrapup forbids it).

// ---- claude-extended-tool-approver (this package's own CLI) ---------------

// cetaEvalSchema etc.: the evaluation subcommands the identify-hook-misses,
// absorb-settings-rules and ceta-spec-gen skills instruct. They READ the
// decision log (evaluate opens it read-only, pg2-cbihz) and the files named
// by --settings/--corpus/--user-dir/--repo-dir/lint paths. The decision-DB-
// mutating subcommands (archive, mark-excluded, set-correct-decision,
// baseline) and `spec-drift-check --record` (writes help-hashes) are absent,
// per P5 (every ceta asks.db write is REJECT/consent) — not instructed by
// any skill, and a ruling this schema does not touch. Old engine: safecmds
// alwaysSafe approved the basename wholesale (a broader, since-narrowed
// reading).
var cetaSchema = CommandSchema{
	Name:       "claude-extended-tool-approver",
	Provenance: "claude-extended-tool-approver --help, this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"evaluate": {
			Name:       "evaluate",
			Provenance: "claude-extended-tool-approver evaluate --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"--days": literal1, "--since": literal1, "--format": literal1,
				"--approval-source": literal1, "--misses-only": inert,
				"--settings": {Arity: ArityOne, Operand: PathRead},
				"--corpus":   {Arity: ArityOne, Operand: PathRead},
				"-h":         inert, "--help": inert,
			},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"show": {
			Name:       "show",
			Provenance: "claude-extended-tool-approver show --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"--format": literal1, "-h": inert, "--help": inert,
			},
			Positionals:  PositionalSpec{Rest: Literal, MinRest: 1},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"lint": {
			Name:       "lint",
			Provenance: "claude-extended-tool-approver lint --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"--embedded": inert, "--format": literal1,
				"--user-dir": {Arity: ArityOne, Operand: PathRead},
				"--repo-dir": {Arity: ArityOne, Operand: PathRead},
				"-h":         inert, "--help": inert,
			},
			Positionals:  PositionalSpec{Rest: PathRead},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"spec-drift-check": {
			Name:       "spec-drift-check",
			Provenance: "claude-extended-tool-approver spec-drift-check --help, this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"--embedded": inert, "-h": inert, "--help": inert,
			},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// ---- plugin helper scripts, keyed by basename -----------------------------
//
// The plugins invoke their own helper scripts by REPO-RELATIVE path
// (`claude-marketplace/plan-decompose/scripts/create-packet.sh ...`). A
// relative argv0 is "exec of an in-checkout file" under R2 repo trust — the
// engine looks such a command up by basename and does NOT apply the P4
// absolute-path identity check to it (effectgraph/build.go) — so these
// schemas are registry entries by script name. An ABSOLUTE path to the same
// script fails the P4 identity check and abstains, by design.

// scriptSchema builds one helper-script schema. hasHelp says whether the
// script itself answers -h/--help (create-packet.sh, chunk-for-bd-field.sh,
// audit-docket-label-leak.sh, impl-traces.sh, name-collisions.sh,
// trace-extract.sh do; self-checks.sh, relocation-check.sh, resolve-imports.sh
// and capture-prefix-snapshots.sh take positionals only and treat `-h` as a bad
// option) — a flag the script does not have is not modeled.
func scriptSchema(name, summary string, hasHelp bool, flags map[string]FlagSpec, pos PositionalSpec, implicit []ImplicitEffect, stdout StdoutKind) CommandSchema {
	f := map[string]FlagSpec{}
	if hasHelp {
		f["-h"], f["--help"] = inert, inert
	}
	for k, v := range flags {
		f[k] = v
	}
	return CommandSchema{
		Name:            name,
		Provenance:      name + " --help / header comment (" + summary + "), this repo 2026-10-05",
		Flags:           f,
		Positionals:     pos,
		ImplicitEffects: implicit,
		Stdin:           StdinNever,
		Stdout:          stdout,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// pluginScriptSchemas: plan-decompose's bd-bookkeeping helpers and
// behavior-docs-conformance's read-only evaluators (+ the one fixture
// capture that writes a fresh --out-dir). resolve-links.sh (may fetch URLs)
// and reconcile-imports.sh are not instructed by a skill and stay unmodeled.
func pluginScriptSchemas() []CommandSchema {
	setDirs := PositionalSpec{Rest: PathRead, MinRest: 1}
	strict := map[string]FlagSpec{"--strict": inert}
	return []CommandSchema{
		scriptSchema("create-packet.sh", "creates one plan-decompose work-packet bead via bd create + bd defer", true,
			map[string]FlagSpec{
				"--parent": literal1, "--title": literal1, "--acceptance": literal1, "--label": literal1,
				"--actor": literal1, "--allow-inherit-labels": inert,
				"--body-file":       {Arity: ArityOne, Operand: PathRead},
				"--acceptance-file": {Arity: ArityOne, Operand: PathRead},
				"--metadata":        {Arity: ArityOne, Operand: DataOrAtFile},
			}, PositionalSpec{Rest: Unmodeled}, []ImplicitEffect{writesBeads}, StdoutMetadata),
		scriptSchema("chunk-for-bd-field.sh", "splits a file into byte-capped chunks <output-prefix>.N", true,
			map[string]FlagSpec{"-m": literal1, "--max-bytes": literal1},
			PositionalSpec{Leading: []OperandRole{PathRead, PathCreate}, Rest: Unmodeled}, nil, StdoutMetadata),
		scriptSchema("audit-docket-label-leak.sh", "read-only: bd list --label docket, flags non-epic holders", true,
			map[string]FlagSpec{"-j": inert, "--json": inert},
			PositionalSpec{Rest: Unmodeled}, []ImplicitEffect{readsBeads}, StdoutContent),
		scriptSchema("impl-traces.sh", "behavior-docs IMPL evaluator: read-only doc<->code ID reconciliation", true,
			strict, PositionalSpec{Leading: []OperandRole{PathRead, PathRead}, Rest: Unmodeled}, nil, StdoutContent),
		scriptSchema("resolve-imports.sh", "behavior-docs INTER evaluator: resolves an imports table by UUID, read-only", false,
			nil, PositionalSpec{Leading: []OperandRole{PathRead, PathRead}, Rest: Unmodeled}, nil, StdoutContent),
		scriptSchema("name-collisions.sh", "behavior-docs INTER evaluator: cross-set name collisions, read-only", true,
			strict, setDirs, nil, StdoutContent),
		scriptSchema("self-checks.sh", "behavior-docs INTRA evaluator: mechanical self-checks, read-only", false,
			nil, PositionalSpec{Leading: []OperandRole{PathRead}, Rest: Unmodeled}, nil, StdoutContent),
		scriptSchema("trace-extract.sh", "behavior-docs INTRA evaluator: INV-22 traceability extractor, read-only", true,
			strict, PositionalSpec{Leading: []OperandRole{PathRead}, Rest: Unmodeled}, nil, StdoutContent),
		scriptSchema("relocation-check.sh", "behavior-docs INTRA evaluator: USECASE-5 relocation check, read-only", false,
			nil, PositionalSpec{Leading: []OperandRole{PathRead}, Rest: Unmodeled}, nil, StdoutContent),
		scriptSchema("capture-prefix-snapshots.sh", "captures pre-fix git snapshots of behavior-docs sets into <out-dir>", false,
			nil, PositionalSpec{Leading: []OperandRole{PathCreate}, Rest: Unmodeled},
			[]ImplicitEffect{{Role: PathRead, Target: ".git"}}, StdoutMetadata),
	}
}

// ---- ancillary read-only tools the skills call ----------------------------

// rgSchema: ripgrep, the search form the beads-lifecycle premise-freshness
// probes use (`rg -uu -in PATTERN -g GLOB PATH`, `... | rg -o PATTERN`).
// Flags that run a program (--pre, -z/--search-zip's decompressors,
// --hostname-bin) or read a caller-chosen file (-f, --ignore-file) are NOT
// modeled → abstain. With no file operand rg searches "." (or stdin when
// piped), so both an implicit read of "." and the stdin rule are declared.
var rgSchema = CommandSchema{
	Name:       "rg",
	Provenance: "rg --help (ripgrep), this host 2026-10-05",
	Flags: func() map[string]FlagSpec {
		m := map[string]FlagSpec{}
		for _, f := range []string{
			"-i", "--ignore-case", "-S", "--smart-case", "-s", "--case-sensitive",
			"-n", "--line-number", "-N", "--no-line-number", "-H", "--with-filename",
			"-I", "--no-filename", "-o", "--only-matching", "-l", "--files-with-matches",
			"--files-without-match", "-c", "--count", "--count-matches", "-w", "--word-regexp",
			"-x", "--line-regexp", "-F", "--fixed-strings", "-v", "--invert-match",
			"-q", "--quiet", "-u", "--unrestricted", "--hidden", "--no-ignore", "-L", "--follow",
			"--no-messages", "--stats", "--json", "--no-heading", "--heading", "--column",
			"-0", "--null", "-a", "--text", "-U", "--multiline", "--vimgrep", "-p", "--pretty",
			"--trim", "-P", "--pcre2", "--no-config", "--files", "-h", "--help", "-V", "--version",
		} {
			m[f] = inert
		}
		for _, f := range []string{
			"-e", "--regexp", "-g", "--glob", "--iglob", "-t", "--type", "-T", "--type-not",
			"-m", "--max-count", "-A", "--after-context", "-B", "--before-context",
			"-C", "--context", "-d", "--max-depth", "-r", "--replace", "--color", "--sort", "--sortr",
		} {
			m[f] = literal1
		}
		return m
	}(),
	Positionals: PositionalSpec{
		Leading:               []OperandRole{Literal},
		LeadingSkippedByFlags: []string{"-e", "--regexp", "--files", "-h", "--help", "-V", "--version"},
		Rest:                  PathRead,
	},
	ImplicitEffects: []ImplicitEffect{{Role: PathRead, Target: "."}},
	Stdin:           StdinWhenNoPathOperands,
	Stdout:          StdoutContent,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// fdSchema: `fd [flags] [PATTERN] [PATH...]`. The program-running options
// (-x/--exec, -X/--exec-batch) and the file-reading ones (--ignore-file) are
// unmodeled → abstain. Output is path names.
var fdSchema = CommandSchema{
	Name:       "fd",
	Provenance: "fd --help, this host 2026-10-05",
	Flags: func() map[string]FlagSpec {
		m := map[string]FlagSpec{}
		for _, f := range []string{
			"-H", "--hidden", "-I", "--no-ignore", "-u", "--unrestricted", "-a", "--absolute-path",
			"-L", "--follow", "-g", "--glob", "-s", "--case-sensitive", "-i", "--ignore-case",
			"-F", "--fixed-strings", "-p", "--full-path", "-0", "--print0", "-1", "-h", "--help",
			"-V", "--version",
		} {
			m[f] = inert
		}
		for _, f := range []string{
			"-t", "--type", "-e", "--extension", "-d", "--max-depth", "--min-depth", "-E", "--exclude",
			"-c", "--color", "-S", "--size", "--changed-within", "--changed-before",
		} {
			m[f] = literal1
		}
		return m
	}(),
	Positionals: PositionalSpec{
		Leading:         []OperandRole{Literal},
		LeadingOptional: true,
		Rest:            PathRead,
	},
	ImplicitEffects: []ImplicitEffect{{Role: PathRead, Target: "."}},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// duSchema: `du [-s] [-h] [-a] [-c] [-k|-m|-b] [-x] [-d N] [PATH...]` —
// disk usage; a metadata read of each PATH ("." when none).
var duSchema = CommandSchema{
	Name:       "du",
	Provenance: "du --help (GNU coreutils), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-s": inert, "--summarize": inert, "-h": inert, "--human-readable": inert,
		"-a": inert, "--all": inert, "-c": inert, "--total": inert,
		"-k": inert, "-m": inert, "-b": inert, "--bytes": inert, "-x": inert, "--one-file-system": inert,
		"-L": inert, "--dereference": inert, "-P": inert, "--apparent-size": inert,
		"-d": literal1, "--max-depth": literal1, "--help": inert, "--version": inert,
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

// lsofSchema: `lsof [-n] [-P] [-t] [-l] [-w] [-a] [-i[SPEC]] [-p PID] [NAME...]`
// — lists open files/sockets (the beads-dolt-doctor port-holder probe,
// `lsof -nP -iTCP:25252`). Read-only; the NAME operands only select which
// files to report. Output is process/file metadata.
var lsofSchema = CommandSchema{
	Name:       "lsof",
	Provenance: "lsof -h, this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-n": inert, "-P": inert, "-t": inert, "-l": inert, "-w": inert, "-a": inert, "-h": inert,
		"-i": literalOpt, "-p": literal1, "-u": literal1,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// launchctlSchema: ONLY the read-only introspection verbs (`list [label]`,
// `print <target>`, `print-disabled <domain>`, `blame <target>`). The
// lifecycle verbs the beads-dolt-doctor recovery runbook also shows
// (bootout, bootstrap, kickstart, unload, ...) are NOT modeled → abstain:
// stopping or loading a launchd job is a service-lifecycle change this schema
// does not approve (R5: approve only when sure it is safe).
var launchctlSchema = CommandSchema{
	Name:         "launchctl",
	Provenance:   "launchctl help, this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: func() map[string]CommandSchema {
		m := map[string]CommandSchema{}
		for _, v := range []string{"list", "print", "print-disabled", "blame"} {
			m[v] = CommandSchema{
				Name:         v,
				Provenance:   "launchctl help " + v + ", this host 2026-10-05",
				Flags:        map[string]FlagSpec{},
				Positionals:  PositionalSpec{Rest: Literal},
				Stdin:        StdinNever,
				Stdout:       StdoutContent,
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
			}
		}
		return m
	}(),
}

// manSchema: `man [section] page...` — formats a manual page for reading.
// Every flag (notably -P pager, -M path, -C config) is unmodeled → abstain.
var manSchema = CommandSchema{
	Name:         "man",
	Provenance:   "man --help, this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	Positionals:  PositionalSpec{Rest: Literal, MinRest: 1},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- nix build / nix flake (the landing and validation recipes) -----------

// nixBuildSchema: `nix build [-L] [--no-link] [--print-out-paths] [--dry-run]
// [-j N] [-o LINK] [INSTALLABLE...]` — builds derivations from THIS
// directory's flake. The operand set is CLOSED to the local-flake spellings
// "." and ".#<attr>" (a path, registry or URL flakeref can point outside the
// project or fetch remote code → unmodeled → abstain); the evaluation runs
// the checkout's own nix code, which the KindExec implicit effect states so
// TrustedCheckoutExec judges the working directory. The default `result`
// symlink in the CWD is declared as a PathCreate (an explicit -o names its own
// path instead). `--impure`, `--option`, `--override-input`, `--expr`,
// `--file` change what is evaluated or trusted and are unmodeled. Old engine:
// nix rule approved `nix build` of local installables.
var nixBuildSchema = CommandSchema{
	Name:       "build",
	Provenance: "nix build --help (Nix 2.34), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"-L": inert, "--print-build-logs": inert, "--no-link": inert, "--print-out-paths": inert,
		"--dry-run": inert, "--json": inert,
		"-o": {Arity: ArityOne, Operand: PathCreate}, "--out-link": {Arity: ArityOne, Operand: PathCreate},
		"--help": inert,
	},
	Positionals: PositionalSpec{Rest: AllowedLiteral("local-flake-installable")},
	ImplicitEffects: []ImplicitEffect{
		{Role: Exec, Target: "nix build"},
		{Role: PathCreate, Target: "result"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// nixFlakeSchema: `nix flake check|update|lock`. `check` evaluates and builds
// every output of the local flake (checkout code, KindExec); `update` and
// `lock` rewrite flake.lock (PathModify) after fetching the named inputs.
// The old engine's nix rule approved `nix flake update` explicitly ("nix
// flake update is approved"); the landing skills run it (with the conflicted
// input names, or none) to repair a rebased flake.lock. `--commit-lock-file`,
// `--override-input`, `--impure` and every other flag are unmodeled →
// abstain.
var nixFlakeSchema = CommandSchema{
	Name:         "flake",
	Provenance:   "nix flake --help (Nix 2.34), this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"check": {
			Name:       "check",
			Provenance: "nix flake check --help (Nix 2.34), this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"-L": inert, "--print-build-logs": inert, "--no-build": inert, "--all-systems": inert,
				"--no-update-lock-file": inert,
				"--help":                inert,
			},
			Positionals:     PositionalSpec{Rest: AllowedLiteral("local-flake-installable")},
			ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "nix flake check"}},
			Stdin:           StdinNever,
			Stdout:          StdoutMetadata,
			UnknownFlag:     UnknownFlagInsufficient,
			EndOfOptions:    true,
		},
		"update": nixFlakeLockWriter("update"),
		"lock":   nixFlakeLockWriter("lock"),
	},
}

func nixFlakeLockWriter(name string) CommandSchema {
	return CommandSchema{
		Name:       name,
		Provenance: "nix flake " + name + " --help (Nix 2.34), this host 2026-10-05",
		Flags: map[string]FlagSpec{
			"-L": inert, "--print-build-logs": inert,
			"--help": inert,
		},
		Positionals:     PositionalSpec{Rest: Literal},
		ImplicitEffects: []ImplicitEffect{{Role: PathModify, Target: "flake.lock"}},
		Stdin:           StdinNever,
		Stdout:          StdoutMetadata,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// pluginToolSchemas lists every schema this file adds to DefaultRegistry().
func pluginToolSchemas() []CommandSchema {
	out := []CommandSchema{
		bgcheckSchema, integrateBranchSupportSchema, wtdoneSchema,
		handoffCreateSchema, sessionModeSchema, pgGoMutateSchema, pgCcauditSchema,
		pgWiFlowSchema, pgHooksSchema, prekSchema, batsSchema, ghSchema,
		cetaSchema, rgSchema, fdSchema, duSchema, lsofSchema, launchctlSchema, manSchema,
	}
	return append(out, pluginScriptSchemas()...)
}
