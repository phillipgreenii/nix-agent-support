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
	Flags:        map[string]FlagSpec{"-v": inert, "--verbose": inert, "--no-telemetry": inert, "-h": inert, "--help": inert},
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

// repoBaseToolSchemas lists the schemas this file contributes to DefaultRegistry.
func repoBaseToolSchemas() []CommandSchema {
	return []CommandSchema{repoBasePnSchema, repoBasePnwfSchema}
}
