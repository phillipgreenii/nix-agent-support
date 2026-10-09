package cmddesc

import "strings"

// pg2-cjfpy.1: the `pb` CLI the claude-marketplace/pb plugin (its /drain-beads,
// /unblock-human-beads and /unstick-beads commands and its drain-one,
// drain-stuck and pb-gate-lifecycle skills) instructs an agent to run -- `pb drain isolate`,
// `pb gate create`, `pb gate check` and `pb gate attach-verified-child`.
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim): "for
// pb *, any command which is supposed to work as part of a skill should be
// autoapproved". ADR 0075 R5: approve, never ask.
//
// # Effect model (facts only; no new policy)
//
// Every pb verb below declares NO effect at all: a `pb` invocation is modeled
// as a command whose flags are inert literals (a bead id, a repo key, a sha,
// a title). That is deliberate, and it is the same model pg2-cjfpy.3 chose for
// `pn`/`pnwf` (registry_repo_base.go): pb is invoked by the drain orchestrator
// with its working directory at the pn-workspace root (or a canonical clone),
// and the directory it acts on arrives as a `--repo` ABSOLUTE PATH to a
// sibling canonical clone, so a path-write fact on it would be judged against
// a project root that never contains it and abstain on every real invocation.
//
// Effects pb performs that this model cannot see are STATED here, not hidden:
//   - `pb drain isolate` runs `git worktree add` / `git branch` against the
//     canonical clone named by `--repo`, creating `.worktrees/<bead>` on
//     `drain/<bead>` there (it reuses an existing one, and never forces or
//     clears anything: exit 3 on conflicting state). It also runs
//     `pg-hooks status --porcelain`, read-only. ADR 0075 R2 puts members of the
//     same pn workspace inside the repo-trust boundary.
//   - `pb gate create` and `pb gate attach-verified-child` WRITE to the shared
//     beads tracker (they shell out to `bd create` / `bd update` /
//     `bd dep add` / `bd comment`) and read git patch-ids. The beads write is
//     deliberately NOT declared as a Remote("mutate") effect: how the
//     remote-mutation judgment treats a beads write is exactly the policy
//     question pg2-cjfpy.2 owns for `bd` itself, and declaring it here would
//     make pb's approval depend on that unlanded decision instead of on the
//     R5 ruling above.
//   - `pb gate check` resolves (closes) or converts `pn:applied` gate beads
//     whose change has been applied; with `--dry-run` it changes nothing.
//
// Deliberately ABSENT (unmodeled -> abstain): `pb gate check --stale-handler`,
// `--stale-after`, `--last-n` and `--strict` (no skill or command instructs
// them; `--stale-handler close` closes beads), `pb completion` (writes shell
// scripts) and `pb help <topic>`. A stray positional on any verb is
// Unmodeled, so it is insufficient.
//
// Flags and synopses were verified against the host's installed binary
// (`pb --help`, `pb drain isolate --help`, `pb gate {create,check,
// attach-verified-child} --help`; pb version 0.0.0-b674b6b9, 2026-10-05) and
// are cited per fact in internal/embeddedspecs/data/pb.json.

// pbLeaf builds one pb leaf verb. extra flags are merged over -h/--help. A
// positional is never expected, so Rest is Unmodeled (any operand makes the
// interpretation insufficient).
func pbLeaf(path string, extra map[string]FlagSpec) CommandSchema {
	return CommandSchema{
		Name:         path[strings.LastIndex(path, " ")+1:],
		Provenance:   "pb " + path + " --help (pb 0.0.0-b674b6b9, this host 2026-10-05)",
		Flags:        mergeFlags(map[string]FlagSpec{"-h": inert, "--help": inert}, extra),
		Positionals:  PositionalSpec{Rest: Unmodeled},
		Stdin:        StdinNever,
		Stdout:       StdoutMetadata,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
}

// pbGroup builds a pb verb group (`drain`, `gate`): only -h/--help of its own,
// the leaves below it carry the real flags.
func pbGroup(name string, subs map[string]CommandSchema) CommandSchema {
	return CommandSchema{
		Name:         name,
		Provenance:   "pb " + name + " --help (pb 0.0.0-b674b6b9, this host 2026-10-05)",
		Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands:  subs,
	}
}

var pbSchema = CommandSchema{
	Name:         "pb",
	Provenance:   "pb --help (pb 0.0.0-b674b6b9, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert, "-v": inert, "--version": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"drain": pbGroup("drain", map[string]CommandSchema{
			"isolate": pbLeaf("drain isolate", map[string]FlagSpec{
				"--bead": literal1, "--repo": literal1, "--json": inert,
			}),
		}),
		"gate": pbGroup("gate", map[string]CommandSchema{
			"create": pbLeaf("gate create", map[string]FlagSpec{
				"--blocks": literal1, "--repo": literal1,
				"--commit": literal1, "--commits": literal1,
				"--reason": literal1, "--json": inert,
			}),
			"check": pbLeaf("gate check", map[string]FlagSpec{
				"--dry-run": inert, "--json": inert,
			}),
			"attach-verified-child": pbLeaf("gate attach-verified-child", map[string]FlagSpec{
				"--impl": literal1, "--title": literal1, "--gate": literal1,
				"--actor": literal1, "--reason": literal1, "--json": inert,
			}),
		}),
	},
}

// pbToolSchemas lists the schemas this file contributes to DefaultRegistry.
func pbToolSchemas() []CommandSchema {
	return []CommandSchema{pbSchema}
}
