package cmddesc

// pg2-niyyq: the first-party plugin SUPPORT tools whose schema was still missing
// (hand-to-human, wtnew, pg-router), plus the recorded decision for every other
// support tool of the set.
//
// Operator ruling (Phillip, 2026-10-25, verbatim from the bead): "all custom
// plugin support tools MUST be auto-approved", with the bead's own rider that
// each tool needs a decision on which subcommands are read-only versus mutating
// before a blanket allow, and that a mutating subcommand which cannot be
// justified abstains. The decision per tool (the enumeration is `packages/`):
//
//	integrate-branch-support  APPROVE every modeled form (bare, --facts,
//	                          --prek-branch-diff, --bundle-refresh SHA). Specced
//	                          since pg2-cjfpy.2 (integrateBranchSupportSchema); the
//	                          two forms that run checkout code state a KindExec
//	                          effect, so TrustedCheckoutExec judges the directory.
//	session-mode              APPROVE start / set-status / show; the internal `hook
//	                          session-end` form is absent and abstains. Writes only
//	                          its own status record (sessionModeSchema).
//	handoff-create            APPROVE: one bd create plus a read-back, a beads
//	                          bookkeeping write (handoffCreateSchema).
//	hand-to-human             APPROVE: one bd update (labels human + attention, drop
//	                          escalated) plus a read-back, the same beads
//	                          bookkeeping write bd update already gets
//	                          (handToHumanSchema, below).
//	wtdone                    APPROVE: guarded teardown, never forced (wtdoneSchema).
//	wtnew                     APPROVE: creates ONE worktree directory under the
//	                          repo's own `.worktrees` and a new branch, then prints
//	                          integrate-branch-support's read-only facts; exactly
//	                          `git worktree add`, which this registry already
//	                          approves (wtnewSchema, below).
//	bgrun                     APPROVE only WHEN THE WRAPPED COMMAND IS: cmdparse
//	                          unwraps `bgrun NAME -- CMD` to CMD, so the wrapper
//	                          adds no authority and launders nothing. The same
//	                          holds for a `bash -c` payload, which is judged as the
//	                          child program it is. Pinned by the laundering rows in
//	                          internal/effectpolicy/plugin_support_tools_test.go.
//	bgcheck, bgwait           APPROVE: read-only (bgcheckSchema, bgwaitSchema).
//	bgkill                    ABSTAIN here, on purpose. It signals a process tree,
//	                          and ADR 0104 approves it only when bgrun's own job
//	                          record proves THIS session launched the job, which is
//	                          session state a spec cannot express. The stateful
//	                          bgkill rule of the live chain keeps that decision.
//	pb                        APPROVE the instructed verbs (registry_pb.go).
//	pg-router                 APPROVE only the READ-ONLY subcommands: status,
//	                          gate list, config --show / --print-defaults / check,
//	                          version, help (pgRouterSchema, below). ABSTAIN on every
//	                          mutating or process-starting one: pause, resume, gate
//	                          set, gate clear, push-inject, log compact, run,
//	                          run-until-idle, run-role, run-query, doctor, tui,
//	                          ingest-event, self-status.
//	example-tick-probe  NOT A PACKAGE OF THIS REPO. It ships in a consumer's
//	                          monorepo, and this public repo MUST NOT name that
//	                          consumer's tools (repo CLAUDE.md, "No ZipRecruiter
//	                          Disclosure"). Its decision belongs in the consumer
//	                          flake's config-driven spec layer; filed as a follow-up.
//
// Every schema approves only the shapes the skills use: UnknownFlagInsufficient
// everywhere, an unlisted subcommand is simply absent (abstain). Help text and
// flags were read from the tools' own `--help` and are cited per fact in
// internal/embeddedspecs/data/*.json.

// ---- hand-to-human (packages/hand-to-human) -------------------------------

// handToHumanSchema: `hand-to-human ID [--backend NAME | --bd-dir DIR]
// [--also-remove LABEL]... [--also-add LABEL]... [--note TEXT] [--actor ID]
// [--if-assignee VALUE] [--if-status VALUE]`: adds `human` + `attention` to bead ID
// and removes `escalated` in ONE `bd update` (or `pg-connector issue update
// --backend NAME`), then reads the bead back. That is the beads tracker-write the
// `bd update --add-label` form already is, so it carries the same implicit remote
// effect as handoff-create. --bd-dir is the tracker root handed to `bd -C` (a
// path read, judged by the path policy); every other value (--backend, --also-remove,
// --also-add, --note's text, --actor, the --if-* claim-guard values) is an inert
// string handed to bd as data, never a path or a program.
var handToHumanSchema = CommandSchema{
	Name:       "hand-to-human",
	Provenance: "hand-to-human --help ('Hand a bead to the operator (human + attention, escalated removed, one update)'), packages/hand-to-human show_help at this commit (the host's installed copy predates --also-add, --note and --if-*)",
	Flags: map[string]FlagSpec{
		"--backend": literal1, "--also-remove": literal1, "--also-add": literal1,
		"--note": literal1, "--actor": literal1,
		"--if-assignee": literal1, "--if-status": literal1,
		"--bd-dir": {Arity: ArityOne, Operand: PathRead},
		"-h":       inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals:     PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{writesBeads},
	Stdin:           StdinNever,
	Stdout:          StdoutMetadata,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// ---- wtnew (packages/wtnew) -------------------------------------------------

// wtnewSchema: `wtnew <bead-or-name> [--base REF] [--branch NAME]`: runs `git
// worktree add <root>/.worktrees/<name> -b <branch> <base>` against the repo's
// canonical root, reports `pg-hooks status --porcelain`, and prints
// `integrate-branch-support`'s facts block from inside the new worktree (bare
// form: read-only). The name, --base and --branch are inert names (the tool
// refuses a name containing '/'). The one write is the new directory under
// `.worktrees`, declared as an implicit PathCreate so the ordinary write ladder
// judges it exactly as it judges `git worktree add` (the leading positional there
// is a PathCreate); a path outside the project's read-write zone is not approved.
var wtnewSchema = CommandSchema{
	Name:       "wtnew",
	Provenance: "wtnew --help ('Create a fresh git worktree for manual (non-drain) work'), packages/wtnew/wtnew/wtnew.sh show_help",
	Flags: map[string]FlagSpec{
		"--base": literal1, "--branch": literal1,
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
	},
	Positionals: PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathCreate, Target: ".worktrees"},
		{Role: PathRead, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- pg-router (packages/pg-router) -----------------------------------------

// pgRouterSchema models ONLY the read-only subcommands (see this file's header
// for the abstain list). `status` and `gate list` inspect the RUNNING core over its
// local socket and "NEVER start a core"; `config --show`, `config --print-defaults`
// and `config check` read the resolved config (`check` stats the named handler
// files, it runs none); `version` and `help` print. Deliberately absent, so any
// of them abstains:
//
//   - --socket and --token: they name which core, and a token, to talk to; a
//     caller-chosen socket is a path this model does not vet;
//   - pause, resume, gate set, gate clear, push-inject, log compact: they change
//     the running core's state (the pool-wide SYSTEM_PAUSE gate, the queue, the
//     event log), which a blanket approval would hand to any session;
//   - run, run-until-idle, run-role, run-query, doctor, tui, ingest-event,
//     self-status: they start a core, execute configured sources and handlers
//     (doctor runs each handler's `validate`), or are manager-to-core callbacks.
var pgRouterSchema = CommandSchema{
	Name:       "pg-router",
	Provenance: "pg-router --help ('pg-router routes typed events from configured sources through a durable queue'), this host 2026-10-26 (help hash identical to the source build at this commit)",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert, "--version": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"status": {
			Name:         "status",
			Provenance:   "pg-router --help, status [--json]: inspect the RUNNING core",
			Flags:        map[string]FlagSpec{"--json": inert},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"gate": {
			Name:         "gate",
			Provenance:   "pg-router --help, gate (set | clear | list [--json])",
			Flags:        map[string]FlagSpec{},
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
			Subcommands: map[string]CommandSchema{
				"list": {
					Name:         "list",
					Provenance:   "pg-router --help, gate list [--json]: list the active gates",
					Flags:        map[string]FlagSpec{"--json": inert},
					Positionals:  PositionalSpec{Rest: Unmodeled},
					Stdin:        StdinNever,
					Stdout:       StdoutContent,
					UnknownFlag:  UnknownFlagInsufficient,
					EndOfOptions: true,
				},
			},
		},
		"config": {
			Name:       "config",
			Provenance: "pg-router --help, config (--print-defaults | --show [--json] | check)",
			Flags: map[string]FlagSpec{
				"--print-defaults": inert, "--show": inert, "--json": inert,
			},
			// `check` is the one positional word the verb takes; it is modeled as a
			// literal operand rather than a nested subcommand because `config`
			// ALSO takes the flag forms (--show, --print-defaults) with no
			// subcommand word, which the subcommand dispatch cannot express. Any
			// other word is a usage error from the tool itself, not an effect.
			Positionals:  PositionalSpec{Rest: Literal},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"version": {
			Name:         "version",
			Provenance:   "pg-router --help, version: print the version and exit",
			Flags:        map[string]FlagSpec{},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"help": {
			Name:         "help",
			Provenance:   "pg-router --help, help: print this help and exit",
			Flags:        map[string]FlagSpec{},
			Positionals:  PositionalSpec{Rest: Unmodeled},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// pluginSupportToolSchemas lists the schemas this file adds to DefaultRegistry().
func pluginSupportToolSchemas() []CommandSchema {
	return []CommandSchema{handToHumanSchema, wtnewSchema, pgRouterSchema}
}
