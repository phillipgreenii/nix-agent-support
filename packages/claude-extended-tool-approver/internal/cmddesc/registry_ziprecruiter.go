package cmddesc

// pg2-cjfpy.4: the CLIs the phillipg-nix-ziprecruiter marketplace plugins
// (gh-stack, daily-focus, local-alert-triage, zr-refactor) instruct an agent
// to run, modeled as FACTS ONLY (ADR 0075 P14: specs are facts, not policy):
// `gh auth status`, `gh extension list`, `gh stack <local verb>`, `pjira`'s
// read verbs, `pg-connector pr show`, `pg-desk show|wip`, the read-only
// daily-focus / local-alert-triage scripts, and the read-or-own-state
// zr-refactor `rc-*` scripts, plus the two coreutils/diagnostic helpers the
// lat-researcher agent names (`df`).
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim):
// "any command which is supposed to work as part of a skill should be
// autoapproved". Every schema below approves only the SHAPE the plugins use:
// UnknownFlagInsufficient everywhere, and a subcommand/mode the plugins do not
// instruct is simply absent (unmodeled -> abstain).
//
// # What is deliberately NOT here (listed in the pg2-cjfpy.4 close report)
//
// Forms that need a NEW effectpolicy judgment (tracker-write, PR/stack create,
// push, Jira write) are not landed: the parent epic's open gate-5 question
// (attach the P15 approve-reachability report, tooling tc-o14i5.3.10, not
// landed) means a change that ALTERS what gets approved waits for a human
// ruling. They are: `gh stack push|submit|sync|link|unstack`, every
// bd-mutating script (df-close-focus, df-wire, df-deferred write, df-pull,
// lat-wire, rc-claim, rc-park, rc-fp), `rc-publish` (push + draft PR),
// `pjira comment|create|transition` (Jira writes). Forms that collide with an
// explicit REJECT/ABSTAIN ruling are not overridden: `gh stack merge`
// (mirrors `gh pr merge`, Reject), `gh stack submit|link --open` (marks PRs
// ready, the `gh pr ready` Abstain ruling pg2-psiqh).
//
// # Effect model
//
//   - A read of a named remote resource (GitHub PR/stack state, Jira, beads)
//     is Remote("read"), Permitted by effectpolicy.RemoteMutation.
//   - `gh stack init|add|rebase|up|down|top|bottom|trunk|checkout` move or
//     rewrite the CURRENT checkout's branches and working tree: PathModify of
//     "." and ".git", exactly gitRebaseSchema's (registry_git_ext.go in
//     pg2-cjfpy.2) and gitCommitSchema's access class.
//   - A first-party script that writes only its OWN state (rc-sentinel's
//     $RC_STATE_DIR sentinels, df-survey's manifest under $TMPDIR, pg-desk's
//     triage-board annotation) declares no path effect for it, the same
//     precedent as session-mode and bgcheck in pg2-cjfpy.2; an explicit
//     --out/--manifest operand IS modeled as a path effect.
//
// Merge note: pg2-cjfpy.2 independently registers `gh` (for `gh pr
// view|list|status|diff|checks|create`). NewRegistry keys by Name and the
// later entry silently wins, so there MUST be exactly ONE `gh` schema: it is
// ghSchema in registry_plugin_tools.go, whose Subcommands include this file's
// zrGhSubcommands ("auth", "extension", "stack"). See
// TestGhSchemaIsSingleAndFoldsBothSiblings.

// zrRemoteRead builds a Remote("read") implicit effect on a named resource.
func zrRemoteRead(resource string) ImplicitEffect {
	return ImplicitEffect{Role: Remote("read"), Target: resource}
}

var (
	zrReadsBeads  = zrRemoteRead("beads")
	zrReadsGitHub = zrRemoteRead("github")
	zrReadsJira   = zrRemoteRead("jira")
)

// zrCheckoutMutation is the implicit local access class of a verb that moves
// or rewrites the current checkout's branches and working tree.
var zrCheckoutMutation = []ImplicitEffect{
	{Role: PathModify, Target: "."},
	{Role: PathModify, Target: ".git"},
}

// zrFlags merges flag tables (later wins), so a persistent flag can be shared
// by a parent and each of its verbs.
func zrFlags(tables ...map[string]FlagSpec) map[string]FlagSpec {
	out := map[string]FlagSpec{}
	for _, t := range tables {
		for k, v := range t {
			out[k] = v
		}
	}
	return out
}

// ziprecruiterToolSchemas lists the schemas this file adds to DefaultRegistry.
func ziprecruiterToolSchemas() []CommandSchema {
	return []CommandSchema{
		zrPjiraSchema, zrPgConnectorSchema, zrPgDeskSchema,
		zrDfResolveFocusSchema, zrDfSplitBlockersSchema, zrDfJiraRefsSchema, zrDfSurveySchema,
		zrLatSurveySchema,
		zrRcProbeSchema, zrRcSentinelSchema, zrRcPreflightSchema, zrRcBranchSchema,
		zrDfSchema, zrCutSchema,
	}
}

// ---- gh ---------------------------------------------------------------------

// zrGhStackLeaf builds one `gh stack <verb>` schema. flags are the verb's own
// flags (every `gh stack` verb also accepts -h/--help).
func zrGhStackLeaf(verb string, flags map[string]FlagSpec, pos PositionalSpec, stdout StdoutKind, implicit ...ImplicitEffect) CommandSchema {
	return CommandSchema{
		Name:            verb,
		Provenance:      "gh stack " + verb + " --help (gh 2.101.0 + github/gh-stack extension, this host 2026-10-05)",
		Flags:           zrFlags(map[string]FlagSpec{"-h": inert, "--help": inert}, flags),
		Positionals:     pos,
		ImplicitEffects: implicit,
		Stdin:           StdinNever,
		Stdout:          stdout,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// zrGhSubcommands: only the `gh` forms the ZR plugins instruct. They are
// folded into the ONE registered `gh` schema (ghSchema, registry_plugin_tools.go)
// as its "auth", "extension" and "stack" subcommands, alongside pg2-cjfpy.2's
// `pr` forms.
//
//   - `gh auth status`: reads the credential state of each known host
//     (network read of GitHub); `--show-token` (prints the auth token) is
//     deliberately unmodeled -> abstain.
//   - `gh extension list`: lists installed extensions (local read).
//     `gh extension install` (fetches and installs third-party code) is
//     deliberately absent.
//   - `gh stack view|up|down|top|bottom|trunk|init|add|rebase|checkout`: the
//     LOCAL stack verbs of the gh-stack skill (view reads PR state from
//     GitHub; the rest move/rewrite the current checkout; rebase and checkout
//     may also fetch from GitHub, a read). `view` without --json opens an
//     interactive TUI (the skill forbids it); that is a hang, not an effect,
//     so it is not modeled. `add` takes -m/-A/-u for the stage-and-commit
//     shortcut, the same access class as `git add` + `git commit`. ABSENT
//     (abstain): push, submit, sync, link, unstack, merge, modify.
var zrGhSubcommands = map[string]CommandSchema{
	"auth": {
		Name:         "auth",
		Provenance:   "gh auth --help (gh 2.101.0)",
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands: map[string]CommandSchema{
			"status": {
				Name:       "status",
				Provenance: "gh auth status --help (gh 2.101.0)",
				Flags: map[string]FlagSpec{
					"-a": inert, "--active": inert,
					"-h": literal1, "--hostname": literal1,
					"--json": literal1, "--jq": literal1, "--template": literal1,
				},
				Positionals:     PositionalSpec{},
				ImplicitEffects: []ImplicitEffect{zrReadsGitHub},
				Stdin:           StdinNever,
				Stdout:          StdoutMetadata,
				UnknownFlag:     UnknownFlagInsufficient,
				EndOfOptions:    true,
			},
		},
	},
	"extension": {
		Name:         "extension",
		Provenance:   "gh extension --help (gh 2.101.0)",
		Flags:        map[string]FlagSpec{},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands: map[string]CommandSchema{
			"list": {
				Name:         "list",
				Provenance:   "gh extension list --help (gh 2.101.0)",
				Flags:        map[string]FlagSpec{},
				Positionals:  PositionalSpec{},
				Stdin:        StdinNever,
				Stdout:       StdoutMetadata,
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
			},
		},
	},
	"stack": {
		Name:         "stack",
		Provenance:   "gh stack --help (gh 2.101.0 + github/gh-stack extension)",
		Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands: map[string]CommandSchema{
			"view": zrGhStackLeaf("view",
				map[string]FlagSpec{"--json": inert},
				PositionalSpec{}, StdoutContent, zrReadsGitHub),
			"up":     zrGhStackLeaf("up", nil, PositionalSpec{Rest: Literal}, StdoutMetadata, zrCheckoutMutation...),
			"down":   zrGhStackLeaf("down", nil, PositionalSpec{Rest: Literal}, StdoutMetadata, zrCheckoutMutation...),
			"top":    zrGhStackLeaf("top", nil, PositionalSpec{}, StdoutMetadata, zrCheckoutMutation...),
			"bottom": zrGhStackLeaf("bottom", nil, PositionalSpec{}, StdoutMetadata, zrCheckoutMutation...),
			"trunk":  zrGhStackLeaf("trunk", nil, PositionalSpec{}, StdoutMetadata, zrCheckoutMutation...),
			"init": zrGhStackLeaf("init",
				map[string]FlagSpec{"-b": literal1, "--base": literal1},
				PositionalSpec{Rest: Literal}, StdoutMetadata, zrCheckoutMutation...),
			"add": zrGhStackLeaf("add",
				map[string]FlagSpec{
					"-A": inert, "--all": inert,
					"-u": inert, "--update": inert,
					"-m": {Arity: ArityOne, Operand: Message}, "--message": {Arity: ArityOne, Operand: Message},
				},
				PositionalSpec{Rest: Literal}, StdoutMetadata, zrCheckoutMutation...),
			"rebase": zrGhStackLeaf("rebase",
				map[string]FlagSpec{
					"--abort": inert, "--continue": inert,
					"--downstack": inert, "--upstack": inert, "--no-trunk": inert,
					"--committer-date-is-author-date": inert, "--preserve-dates": inert,
					"--remote": literal1,
				},
				PositionalSpec{Rest: Literal}, StdoutMetadata,
				append([]ImplicitEffect{zrReadsGitHub}, zrCheckoutMutation...)...),
			"checkout": zrGhStackLeaf("checkout", nil,
				PositionalSpec{Rest: Literal}, StdoutMetadata,
				append([]ImplicitEffect{zrReadsGitHub}, zrCheckoutMutation...)...),
		},
	},
}

// ---- pjira ------------------------------------------------------------------

// zrPjiraLeaf builds one read verb of the Jira CLI. The persistent --config
// flag names a config TOML that holds credentials, so it is a modeled path
// read (a secret path is refused by PathAccessPolicy), not an inert flag.
func zrPjiraLeaf(verb string, flags map[string]FlagSpec, pos PositionalSpec, stdout StdoutKind) CommandSchema {
	return CommandSchema{
		Name:       verb,
		Provenance: "pjira " + verb + " --help (this host 2026-10-05)",
		Flags: zrFlags(map[string]FlagSpec{
			"-h": inert, "--help": inert,
			"--config": {Arity: ArityOne, Operand: PathRead},
		}, flags),
		Positionals:     pos,
		ImplicitEffects: []ImplicitEffect{zrReadsJira},
		Stdin:           StdinNever,
		Stdout:          stdout,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// zrPjiraSchema: only the READ verbs -- `issue <KEY>`, `search --jql ...`,
// `auth-status` (zr-refactor status.md: "look it up with the pjira CLI";
// df-survey's Jira survey). `comment`, `create` and `transition` are Jira
// WRITES and are deliberately absent (daily-focus close.md gates each posted
// comment behind an explicit per-comment operator approval).
var zrPjiraSchema = CommandSchema{
	Name:       "pjira",
	Provenance: "pjira --help (Generic Atlassian Jira access tool, this host 2026-10-05)",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert,
		"--config": {Arity: ArityOne, Operand: PathRead},
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"issue": zrPjiraLeaf("issue", nil,
			PositionalSpec{Leading: []OperandRole{Literal}}, StdoutContent),
		"search": zrPjiraLeaf("search", map[string]FlagSpec{
			"--all": inert, "--cursor": literal1, "--expand": literal1,
			"--jql": literal1, "--limit": literal1,
		}, PositionalSpec{}, StdoutContent),
		"auth-status": zrPjiraLeaf("auth-status", nil, PositionalSpec{}, StdoutMetadata),
	},
}

// ---- pg-connector -----------------------------------------------------------

// zrPgConnectorSchema: only `pg-connector pr show <id>` (daily-focus
// close.md's per-bead PR lookup): a read of the PR's current state from the
// connector's backend. Every other resource/verb is absent.
var zrPgConnectorSchema = CommandSchema{
	Name:       "pg-connector",
	Provenance: "pg-connector --help (this host 2026-10-05)",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert,
		"--output": literal1,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"pr": {
			Name:         "pr",
			Provenance:   "pg-connector pr --help (this host 2026-10-05)",
			Flags:        map[string]FlagSpec{"-h": inert, "--help": inert, "--output": literal1},
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
			Subcommands: map[string]CommandSchema{
				"show": {
					Name:       "show",
					Provenance: "pg-connector pr show --help (this host 2026-10-05)",
					Flags: map[string]FlagSpec{
						"-h": inert, "--help": inert,
						"--backend": literal1, "--output": literal1,
					},
					Positionals:     PositionalSpec{Leading: []OperandRole{Literal}},
					ImplicitEffects: []ImplicitEffect{zrReadsGitHub},
					Stdin:           StdinNever,
					Stdout:          StdoutContent,
					UnknownFlag:     UnknownFlagInsufficient,
					EndOfOptions:    true,
				},
			},
		},
	},
}

// ---- pg-desk ----------------------------------------------------------------

// zrPgDeskSchema: `pg-desk show <pr> [--json]` (prints a PR's stored
// interpretation from pg-desk's own store; `--refresh` re-runs the pipeline
// and is deliberately unmodeled) and `pg-desk wip on|off <pr>` (marks a PR
// work-in-progress on the LOCAL triage board -- an annotation row in
// pg-desk's own store, the table `show --json`'s `.wip` field reads back).
// Neither names a path or a remote resource, so no effect is declared
// (first-party own state, the session-mode precedent).
var zrPgDeskSchema = CommandSchema{
	Name:       "pg-desk",
	Provenance: "pg-desk --help (this host 2026-10-05)",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"show": {
			Name:       "show",
			Provenance: "pg-desk show --help (this host 2026-10-05)",
			Flags: map[string]FlagSpec{
				"-h": inert, "--help": inert,
				"--json": inert,
			},
			Positionals:  PositionalSpec{Leading: []OperandRole{Literal}},
			Stdin:        StdinNever,
			Stdout:       StdoutContent,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
		"wip": {
			Name:         "wip",
			Provenance:   "pg-desk wip --help (this host 2026-10-05)",
			Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
			Positionals:  PositionalSpec{Leading: []OperandRole{Literal, Literal}},
			Stdin:        StdinNever,
			Stdout:       StdoutNone,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// ---- daily-focus / local-alert-triage scripts -------------------------------

// zrScript builds the schema of a nix-packaged ZR helper script.
func zrScript(name, help string, flags map[string]FlagSpec, pos PositionalSpec, stdin StdinSpec, stdout StdoutKind, implicit ...ImplicitEffect) CommandSchema {
	return CommandSchema{
		Name:            name,
		Provenance:      name + " --help (" + help + ", this host 2026-10-05)",
		Flags:           zrFlags(map[string]FlagSpec{"-h": inert, "--help": inert}, flags),
		Positionals:     pos,
		ImplicitEffects: implicit,
		Stdin:           stdin,
		Stdout:          stdout,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

// df-resolve-focus [<date>] [--status open|all] | --any [--status ...]: a
// `bd list` over the daily-focus label; prints a bead's JSON. Read-only.
var zrDfResolveFocusSchema = zrScript("df-resolve-focus",
	"Resolve the Focus bead for a date via its df_date metadata",
	map[string]FlagSpec{"--any": inert, "--status": literal1},
	PositionalSpec{Rest: Literal}, StdinNever, StdoutContent, zrReadsBeads)

// df-split-blockers <focus-id>: partitions `bd dep list <id> --json` by
// status. Read-only.
var zrDfSplitBlockersSchema = zrScript("df-split-blockers",
	"Partition a focus bead's blockers by status",
	nil, PositionalSpec{Rest: Literal}, StdinNever, StdoutContent, zrReadsBeads)

// df-jira-refs <bead-id>...: prints each bead's jira-<KEY> external-ref via
// `bd show <id> --json`. Read-only.
var zrDfJiraRefsSchema = zrScript("df-jira-refs",
	"Print the bare Jira key for every given bead's jira-<KEY> external-ref",
	nil, PositionalSpec{Rest: Literal}, StdinNever, StdoutContent, zrReadsBeads)

// df-survey, all four modes (shallow, --allow-existing, --apply-gate, --deep):
// "Surveys PRs (gh), Jira (pjira), and epics (bd) directly" (reads of GitHub,
// Jira and beads) and writes ONE manifest JSON file -- at --out, else under
// ${DF_OUT_DIR:-${TMPDIR:-/tmp}} (its own output dir), else beside --manifest
// as <manifest>.gated.json / <manifest>.deep.json. It "never touches bd
// state". --manifest is a path read (the gate reply and --records - arrive on
// stdin, hence StdinAlways); an explicit --out is a path truncate. The
// manifest-adjacent default outputs are the tool's own, fixed-suffix files and
// declare no effect (stated, not hidden).
var zrDfSurveySchema = zrScript("df-survey",
	"daily-focus survey (shallow), gate-reply application and deep enrichment",
	map[string]FlagSpec{
		"--date": literal1, "--cap": literal1, "--jql": literal1,
		"--allow-existing": inert,
		"--apply-gate":     inert,
		"--deep":           inert, "--in-plan": inert,
		"--items": literal1, "--records": literal1,
		"--manifest": {Arity: ArityOne, Operand: PathRead},
		"--out":      {Arity: ArityOne, Operand: PathTruncate},
	},
	PositionalSpec{}, StdinAlways, StdoutContent,
	zrReadsGitHub, zrReadsJira, zrReadsBeads)

// lat-survey [--grafana-base URL] [--history-days N] [--out FILE]: "read-only
// alert survey (via pg-connector) + bd cross-reference ... Never mutates `bd`
// state." Reads the alert backend (Grafana, through pg-connector) and beads;
// writes one manifest at --out or under ${LAT_OUT_DIR:-${TMPDIR:-/tmp}}.
// --grafana-base is only used to build dashboard links (the connector fetches
// from its own configured base_url), so it is a literal, never a network
// operand.
var zrLatSurveySchema = zrScript("lat-survey",
	"read-only alert survey (via pg-connector) + bd cross-reference",
	map[string]FlagSpec{
		"--grafana-base": literal1, "--history-days": literal1,
		"--out": {Arity: ArityOne, Operand: PathTruncate},
	},
	PositionalSpec{}, StdinNever, StdoutContent,
	zrRemoteRead("grafana"), zrReadsBeads)

// ---- zr-refactor rc-* scripts -------------------------------------------------

// rc-probe <wt> <project> --spec <spec.json> [--app-bead <id>] [--first-only]:
// emits outstanding fingerprints by scanning <wt> (a path READ of the whole
// checkout) with the regex in the spec file (a path read); --app-bead
// subtracts the [rc-fp]/[rc-skip] registries via `bd show` (a beads read).
var zrRcProbeSchema = zrScript("rc-probe",
	"Emit outstanding instance fingerprints for one project",
	map[string]FlagSpec{
		"--spec":       {Arity: ArityOne, Operand: PathRead},
		"--app-bead":   literal1,
		"--first-only": inert,
	},
	PositionalSpec{Leading: []OperandRole{PathRead, Literal}}, StdinNever, StdoutContent,
	zrReadsBeads)

// rc-sentinel write|check|retire: "zr-refactor stop-sentinel operations". All
// three touch only $RC_STATE_DIR/stop.<slug> sentinel files, the plugin's own
// state (write creates one, retire deletes only sentinels OLDER than the given
// epoch, check reads one and never deletes), so no path effect is declared.
func zrRcSentinelVerb(verb string, pos PositionalSpec) CommandSchema {
	s := zrScript(verb, "zr-refactor stop-sentinel operations", nil, pos, StdinNever, StdoutMetadata)
	s.Provenance = "rc-sentinel --help, " + verb + " (zr-refactor stop-sentinel operations, this host 2026-10-05)"
	return s
}

var zrRcSentinelSchema = CommandSchema{
	Name:         "rc-sentinel",
	Provenance:   "rc-sentinel --help (zr-refactor stop-sentinel operations, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"write":  zrRcSentinelVerb("write", PositionalSpec{Leading: []OperandRole{Literal}}),
		"check":  zrRcSentinelVerb("check", PositionalSpec{Leading: []OperandRole{Literal, Literal}}),
		"retire": zrRcSentinelVerb("retire", PositionalSpec{Leading: []OperandRole{Literal, Literal}}),
	},
}

// rc-preflight: "zr-refactor worktree-pool lifecycle". Only the modes the
// zr-refactor commands run are approvable: `--list` (members + holders),
// `--verify <wt> <actor>` ("assert toplevel + own lock + clean tree (never
// cleans)"), `--release <actor>` ("release this actor's member (always exits
// 0)") and the bare `<actor>` acquire ("prints member path"; creates the
// pool's worktree). The pool is the plugin's own state under $RC_STATE_DIR.
// `--force-release <n>` ("operator reclaim of a dead session's member") is
// printed for the OPERATOR by /zr-refactor:status ("Do not run it yourself")
// and is deliberately unmodeled -> abstain.
var zrRcPreflightSchema = zrScript("rc-preflight",
	"zr-refactor worktree-pool lifecycle",
	map[string]FlagSpec{
		"--list":    inert,
		"--verify":  inert,
		"--release": inert,
	},
	PositionalSpec{Rest: Literal}, StdinNever, StdoutMetadata)

// ---- df ---------------------------------------------------------------------

// zrDfSchema: `df [-h] [path...]` -- the lat-researcher agent's "read-only
// local diagnostics ... `df -h`". Reports filesystem usage; the path operands
// are metadata reads.
var zrDfSchema = CommandSchema{
	Name:       "df",
	Provenance: "df (GNU coreutils) 9.11, df --help",
	Flags: map[string]FlagSpec{
		"-h": inert, "--human-readable": inert,
		"-H": inert, "--si": inert,
		"-k": inert, "-P": inert, "--portability": inert,
		"-T": inert, "--print-type": inert,
		"-i": inert, "--inodes": inert,
		"-l": inert, "--local": inert,
		"--total": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- cut --------------------------------------------------------------------

// zrCutSchema: `cut -f1` etc. -- the pure line filter in gh-stack's
// extension-installed gate (`gh extension list | cut -f1 | grep -qxF 'gh
// stack'`). Selects parts of lines from each FILE (path reads) or stdin;
// writes only to stdout.
var zrCutSchema = CommandSchema{
	Name:       "cut",
	Provenance: "cut (GNU coreutils) 9.11, cut --help",
	Flags: map[string]FlagSpec{
		"-b": literal1, "--bytes": literal1,
		"-c": literal1, "--characters": literal1,
		"-f": literal1, "--fields": literal1,
		"-F": literal1,
		"-d": literal1, "--delimiter": literal1,
		"-O": literal1, "--output-delimiter": literal1,
		"--complement": inert,
		"-n":           inert, "--no-partial": inert,
		"-s": inert, "--only-delimited": inert,
		"-z": inert, "--zero-terminated": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// ---- rc-branch --------------------------------------------------------------

// zrRcBranchSchema: `rc-branch <wt> <branch>` -- "Check out / reconcile a
// campaign app branch non-destructively" inside the pool worktree <wt>: it
// switches the worktree to <branch>, fetching from and rebasing on origin
// (a rebase conflict is aborted and the tree left clean). So <wt> is a
// PathModify (the working tree it rewrites) and the fetch is a read of the
// remote.
var zrRcBranchSchema = zrScript("rc-branch",
	"Check out / reconcile a campaign app branch non-destructively",
	nil,
	PositionalSpec{Leading: []OperandRole{PathModify, Literal}}, StdinNever, StdoutMetadata,
	zrReadsGitHub)
