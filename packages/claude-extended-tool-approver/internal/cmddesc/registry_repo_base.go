package cmddesc

// pg2-cjfpy.3: the CLIs the phillipg-nix-repo-base plugins (pn-workspace-rules:
// its skills, /pn-workspace-sync, /pn-workspace-update and the pnwf-runner /
// pnwf-update-runner agents) instruct an agent to run -- `pn workspace <verb>`
// and `pnwf <subcommand>`.
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim): "any
// command which is supposed to work as part of a skill should be autoapproved
// ... pn workspace is approved, that is correct." ADR 0075 R6: "`pn workspace
// push|update` approvable."
//
// # Effect model (facts only; no new policy)
//
// Every verb below declares NO effect at all: a `pn`/`pnwf` invocation is
// modeled as a command whose flags and positionals are inert literals (a
// branch name, a repo key, a ref). That is deliberate, and it reproduces what
// the frozen old engine did for these tools (internal/rules/pnworkspace and
// internal/rules/pnwf approved them by basename + subcommand from any CWD).
//
// The alternative -- the KindExec fact goTestSchema declares, judged by
// TrustedCheckoutExec -- was tried and rejected: that policy is Permitted only
// when CWD is inside a git/go checkout, but the skills run these tools from a
// workforest set directory (`cd <SETDIR> && pn workspace build`) or the
// non-git pn workspace root, neither of which carries a git/go marker, so
// every real invocation would abstain. Teaching TrustedCheckoutExec the
// `pn-workspace.toml` marker is a policy change, filed as a follow-up
// (see the pg2-cjfpy.3 close report), not done here.
//
// Effects the tools perform that this model cannot see are STATED here, not
// hidden:
//   - They run the workspace repos' own flake/hook/script code (nix builds,
//     `update-locks.sh`, git hooks) and write inside the pn workspace. ADR 0075
//     R2 puts "members of the same pn-workspace workforest set" inside the
//     repo-trust boundary.
//   - `pn workspace apply` activates the host system (darwin-rebuild/
//     nixos-rebuild switch). No EffectKind models system activation and no
//     policy judges one; the operator ruling above approves it.
//   - `pn workspace push` and `pnwf sync-fetch` PUSH to a git remote. ADR 0075
//     R6 rules `pn workspace push|update` approvable, and the operator
//     re-confirmed it (2026-10-04). The push is deliberately NOT declared as a
//     Remote("push") effect: the remote is resolved at runtime by pn's
//     convention chain (--remote, single remote, branch.<b>.pushRemote,
//     remote.pushDefault, "origin"), so the effect would have to be Dynamic,
//     and RemoteMutation abstains on a Dynamic remote -- permanently, not just
//     until the R6 git-push policy (tc-o14i5.3.10) lands. Declaring it would
//     make the ruled-approvable verb unapprovable.
//   - `pn workspace update` / `pnwf update-relock` fetch flake inputs over the
//     network (`nix flake update`); ADR 0023 makes `update` local-only (no
//     remote WRITE), which is what R6 approves.
//
// Deliberately ABSENT (unmodeled -> abstain): `pn workspace init` (rewrites the
// user-only pn-workspace.toml), `allow`/`deny` (grant/revoke hook trust, ADR
// 0019), `upgrade` (update + apply, documented USER ONLY), `nix -- <args>`
// (arbitrary nix), `workforest remove`/`remove-repo` (explicitly NOT relieved by
// the 2026-09-17 operator ruling, pg2-4zyqf; pathspec.pnKind guards the set
// directory the same way), `push --no-verify`, `doctor --fix` (mutates the
// canonical clones; the skills forbid it), `--otlp-endpoint` (telemetry sink),
// and `pnwf cleanup --force-*` (operator-only per the cleanup-workforest skill).
//
// Flags and synopses were verified against the host's installed binaries
// (`pn workspace <verb> --help`, `pnwf --help`, 2026-10-05) and are cited per
// fact in internal/embeddedspecs/data/{pn,pnwf}.json.
//
// Merge note: pg2-cjfpy.2 (agent-support plugins) independently registers a
// narrower `pn` schema (push/update/apply/doctor/workforest list|prune|remove)
// for the verbs agent-support's skills instruct. NewRegistry keys by Name and
// the later entry silently wins, so whichever child lands second MUST fold the
// two `pn` schemas into one rather than leave both registered.

// pnWorkspaceGlobalFlags are the flags `pn workspace <verb>` accepts on every
// verb (cobra persistent/global flags). --otlp-endpoint is deliberately absent.
func pnWorkspaceGlobalFlags() map[string]FlagSpec {
	return map[string]FlagSpec{
		"--terminal": literal1, "--no-telemetry": inert,
		"-v": inert, "--verbose": inert, "-h": inert, "--help": inert,
	}
}

// pnWorkspaceVerbSchema builds one verb. extra flags are merged over the global ones.
func pnWorkspaceVerbSchema(name string, extra map[string]FlagSpec, implicit ...ImplicitEffect) CommandSchema {
	return CommandSchema{
		Name:            name,
		Provenance:      "pn workspace " + name + " --help (pn 0.0.0-3367c0e5, this host 2026-10-05)",
		Flags:           mergeFlags(pnWorkspaceGlobalFlags(), extra),
		Positionals:     PositionalSpec{Rest: Literal},
		ImplicitEffects: implicit,
		Stdin:           StdinNever,
		Stdout:          StdoutMetadata,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

var pnWorkspacePush = pnWorkspaceVerbSchema(
	"push", map[string]FlagSpec{
		"--no-siblings": inert, "-u": inert, "--set-upstream": inert, "--remote": literal1,
	},
)

var pnWorkspaceWorkforest = CommandSchema{
	Name:         "workforest",
	Provenance:   "pn workspace workforest --help (pn 0.0.0-3367c0e5, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"add":      pnWorkspaceVerbSchema("add", map[string]FlagSpec{"--repos": literal1}),
		"add-repo": pnWorkspaceVerbSchema("add-repo", nil),
		"list":     pnWorkspaceVerbSchema("list", nil),
		"prune":    pnWorkspaceVerbSchema("prune", nil),
	},
}

var repoBasePnSchema = CommandSchema{
	Name:         "pn",
	Provenance:   "pn --help (pn 0.0.0-3367c0e5, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-v": inert, "--verbose": inert, "--no-telemetry": inert, "-h": inert, "--help": inert, "--version": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"workspace": {
			Name:         "workspace",
			Provenance:   "pn workspace --help (pn 0.0.0-3367c0e5, this host 2026-10-05)",
			Flags:        pnWorkspaceGlobalFlags(),
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
			Subcommands: map[string]CommandSchema{
				// Pure introspection: no effect.
				"status":   pnWorkspaceVerbSchema("status", nil),
				"tree":     pnWorkspaceVerbSchema("tree", nil),
				"discover": pnWorkspaceVerbSchema("discover", nil),
				"info":     pnWorkspaceVerbSchema("info", map[string]FlagSpec{"--json": inert}),
				// Runs workspace flake/hook/script code.
				"build":            pnWorkspaceVerbSchema("build", nil),
				"flake-check":      pnWorkspaceVerbSchema("flake-check", nil),
				"format":           pnWorkspaceVerbSchema("format", nil),
				"pre-commit-check": pnWorkspaceVerbSchema("pre-commit-check", nil),
				"doctor": pnWorkspaceVerbSchema("doctor", map[string]FlagSpec{
					"--json": inert, "--offline": inert, "--strict": inert,
				}),
				"lock":  pnWorkspaceVerbSchema("lock", map[string]FlagSpec{"--allow-missing-edges": inert}),
				"clone": pnWorkspaceVerbSchema("clone", nil),
				"update": pnWorkspaceVerbSchema("update", map[string]FlagSpec{
					"--in-place": inert, "--siblings-only": inert,
				}),
				"rebase":     pnWorkspaceVerbSchema("rebase", nil),
				"apply":      pnWorkspaceVerbSchema("apply", map[string]FlagSpec{"--force": inert}),
				"push":       pnWorkspacePush,
				"workforest": pnWorkspaceWorkforest,
			},
		},
	},
}

// pnwfVerb builds one pnwf subcommand; extra flags are merged over -h/--help.
func pnwfVerb(name string, extra map[string]FlagSpec, pos PositionalSpec, implicit ...ImplicitEffect) CommandSchema {
	flags := map[string]FlagSpec{"-h": inert, "--help": inert}
	return CommandSchema{
		Name:            name,
		Provenance:      "pnwf --help (pnwf, phillipg-nix-repo-base modules/pnwf, this host 2026-10-05)",
		Flags:           mergeFlags(flags, extra),
		Positionals:     pos,
		ImplicitEffects: implicit,
		Stdin:           StdinNever,
		Stdout:          StdoutContent,
		UnknownFlag:     UnknownFlagInsufficient,
		EndOfOptions:    true,
	}
}

var (
	pnwfSetFlag   = map[string]FlagSpec{"--set": inert}
	pnwfNoPos     = PositionalSpec{}
	pnwfBranchPos = PositionalSpec{Leading: []OperandRole{Literal}}
)

var repoBasePnwfSchema = CommandSchema{
	Name:         "pnwf",
	Provenance:   "pnwf --help (pnwf, phillipg-nix-repo-base modules/pnwf, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert, "-v": inert, "--version": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		// Read-only probes ("Subcommands (read-only, implemented)").
		"resolve":        pnwfVerb("resolve", pnwfSetFlag, pnwfNoPos),
		"repos":          pnwfVerb("repos", pnwfSetFlag, pnwfNoPos),
		"stage":          pnwfVerb("stage", pnwfSetFlag, pnwfNoPos),
		"residue":        pnwfVerb("residue", pnwfSetFlag, pnwfNoPos),
		"fork-preflight": pnwfVerb("fork-preflight", map[string]FlagSpec{"--repos": literal1}, pnwfBranchPos),
		"land-plan":      pnwfVerb("land-plan", nil, pnwfBranchPos),
		"status":         pnwfVerb("status", nil, pnwfBranchPos),
		// Mutating WORK-recipe helpers. sync-fetch also publishes a member's
		// canonical primary to origin when it is ahead (never --force); see the
		// file comment for why that push is not a declared effect.
		"sync-fetch":    pnwfVerb("sync-fetch", pnwfSetFlag, pnwfNoPos),
		"update-relock": pnwfVerb("update-relock", pnwfSetFlag, pnwfNoPos),
		// cleanup WITHOUT the two --force-* flags only removes members already
		// confirmed as ancestors of their primary (via wtdone). The force flags
		// are absent from Flags, so their presence is insufficient.
		"cleanup": pnwfVerb("cleanup", nil, pnwfBranchPos),
	},
}

// ---- nix eval / nix fmt / darwin-rebuild build / pa-monitor (pg2-33slg) ----
//
// pg2-33slg re-graded the forms pn-workspace-rules and capability-model still
// instruct after pg2-cjfpy.2/.3 and models the ones that are safe. Same
// operator ruling as above (Phillip, 2026-10-04: "any command which is
// supposed to work as part of a skill should be autoapproved"), same
// restraint: every schema approves only the shape the skills use and every
// other flag stays insufficient.

// nixEvalSchema: `nix eval [--raw|--json] [.|.#attr]` -- evaluates an attribute
// of THIS directory's flake and prints it (capability-model:
// `nix eval ...darwinConfigurations.<host>...` to surface option/type errors
// before a build). The operand set is CLOSED to the local-flake spellings
// exactly like nixBuildSchema (a registry or URL flakeref can fetch remote
// code); evaluating runs the checkout's own nix code, which the KindExec
// implicit effect states so TrustedCheckoutExec judges the working directory.
// Deliberately absent (insufficient): `--expr`/`--file`/`--apply` (evaluate
// arbitrary text), `--impure`, `--write-to` (writes a directory tree),
// `--option`, `--override-input`.
var nixEvalSchema = CommandSchema{
	Name:       "eval",
	Provenance: "nix eval --help (Nix 2.34), this host 2026-10-05",
	Flags: map[string]FlagSpec{
		"--raw": inert, "--json": inert,
		"-L": inert, "--print-build-logs": inert, "--help": inert,
	},
	Positionals:     PositionalSpec{Rest: AllowedLiteral("local-flake-installable")},
	ImplicitEffects: []ImplicitEffect{{Role: Exec, Target: "nix eval"}},
	Stdin:           StdinNever,
	Stdout:          StdoutContent,
	UnknownFlag:     UnknownFlagInsufficient,
	EndOfOptions:    true,
}

// nixFmtSchema: `nix fmt [-- paths...]` -- runs the formatter THIS directory's
// flake declares (`nix fmt` is an alias for `nix formatter run`). The formatter
// is checkout code (KindExec, judged by TrustedCheckoutExec) and it rewrites
// files in place, so the working tree is declared PathModify. Every positional
// is forwarded to the formatter, whose own flags this schema cannot see, so a
// positional is accepted only from the closed "relative-project-path" set (a
// plain relative file name: no leading `-`, no absolute/`~`/`$`/`..` spelling).
// A PathModify role would be wrong here: after `--` it would read a forwarded
// flag such as `--tree-root=/` as a file NAMED like the flag and approve it.
// No flag is modeled: nix's own options (`--json`, `--impure`, `--option`, ...)
// stay insufficient.
var nixFmtSchema = CommandSchema{
	Name:        "fmt",
	Provenance:  "nix fmt --help (Nix 2.34), this host 2026-10-05",
	Flags:       map[string]FlagSpec{},
	Positionals: PositionalSpec{Rest: AllowedLiteral("relative-project-path")},
	ImplicitEffects: []ImplicitEffect{
		{Role: Exec, Target: "nix fmt"},
		{Role: PathModify, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// darwinRebuildSchema: ONLY `darwin-rebuild build --flake <local flake>#<host>`
// -- the BUILD-only recipe pn-workspace-rules gives for a system-build-
// equivalent check ("or `darwin-rebuild build`"). switch/activate/check/edit/
// changelog are absent: switch and activate change the running system (a
// user-only step) and `check` needs root. --flake is REQUIRED (a bare build
// would evaluate whatever /etc/nix-darwin or NIX_PATH points at) and closed to
// "." / ".#attr" like nix build, because a flake elsewhere is code from outside
// the project. The first positional is Leading/Unmodeled and skipped only when
// --flake appeared, so no --flake (or a stray positional) is insufficient.
// `--override-input`, `--impure`, `--option`, `-I`, `--refresh` and `--offline`
// change what is evaluated or fetched and are unmodeled; the pn-workspace
// "replicate --override-input by hand" workaround therefore still abstains.
// The build writes the ./result symlink and runs the checkout's nix code.
var darwinRebuildSchema = CommandSchema{
	Name:         "darwin-rebuild",
	Provenance:   "darwin-rebuild --help (nix-darwin), this host 2026-10-05",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"build": {
			Name:       "build",
			Provenance: "darwin-rebuild --help (nix-darwin), this host 2026-10-05",
			Flags: map[string]FlagSpec{
				"--flake":   {Arity: ArityOne, Operand: AllowedLiteral("local-flake-installable")},
				"--dry-run": inert, "--show-trace": inert,
				"-L": inert, "--print-build-logs": inert,
			},
			// Rest: Unmodeled makes ANY extra word insufficient: the script keeps
			// the LAST action word it sees, so `build ... switch` would activate.
			Positionals: PositionalSpec{
				Leading:               []OperandRole{Unmodeled},
				LeadingSkippedByFlags: []string{"--flake"},
				Rest:                  Unmodeled,
			},
			ImplicitEffects: []ImplicitEffect{
				{Role: Exec, Target: "darwin-rebuild build"},
				{Role: PathCreate, Target: "result"},
			},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// readsPaMonitor is the implicit effect of a pa-monitor query: a read over the
// local Unix-socket RPC to the user's own pa-monitor daemon, the same shape as
// readsBeads for bd.
var readsPaMonitor = ImplicitEffect{Role: Remote("read"), Target: "pa-monitor"}

// paMonitorSchema: ONLY the read-only queries `pa-monitor status [--json]` and
// `pa-monitor info <selector> [--json]` (pn-workspace-rules:audit-worktrees'
// informational liveness check). Source: packages/pa-monitor/cmd/pa-monitor/
// cli.go runStatus (GetState + per-session GetSessionInfo, no mutation) and
// control.go runInfo (GetPathInfo / GetSessionInfo). The selector
// (`session:<id>`, `path:<p>`, `cmux:<ws>`) is a daemon lookup key, not a
// filesystem operand: the daemon only matches it against known sessions. The
// other subcommands are absent: daemon, caffeinate, nudge, auto-resume,
// cmux-bridge and wait-until-agents-finished change daemon or session state,
// and bare `pa-monitor` launches the TUI.
var paMonitorSchema = CommandSchema{
	Name:         "pa-monitor",
	Provenance:   "pa-monitor --help, packages/pa-monitor/cmd/pa-monitor/main.go usageText, this repo 2026-10-05",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"status": {
			Name:            "status",
			Provenance:      "packages/pa-monitor/cmd/pa-monitor/cli.go runStatus, this repo 2026-10-05",
			Flags:           map[string]FlagSpec{"--json": inert},
			Positionals:     PositionalSpec{Rest: Unmodeled},
			ImplicitEffects: []ImplicitEffect{readsPaMonitor},
			Stdin:           StdinNever,
			Stdout:          StdoutContent,
			UnknownFlag:     UnknownFlagInsufficient,
			EndOfOptions:    true,
		},
		"info": {
			Name:            "info",
			Provenance:      "packages/pa-monitor/cmd/pa-monitor/control.go runInfo, this repo 2026-10-05",
			Flags:           map[string]FlagSpec{"--json": inert},
			Positionals:     PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled},
			ImplicitEffects: []ImplicitEffect{readsPaMonitor},
			Stdin:           StdinNever,
			Stdout:          StdoutContent,
			UnknownFlag:     UnknownFlagInsufficient,
			EndOfOptions:    true,
		},
	},
}

// repoBaseToolSchemas lists the schemas this file contributes to DefaultRegistry.
func repoBaseToolSchemas() []CommandSchema {
	return []CommandSchema{repoBasePnSchema, repoBasePnwfSchema, darwinRebuildSchema, paMonitorSchema}
}
