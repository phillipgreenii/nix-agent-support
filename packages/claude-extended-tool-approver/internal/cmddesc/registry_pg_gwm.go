package cmddesc

// pg-gwm (packages/pg-gwm): the git worktree manager that leases bounded,
// reusable worktrees of a repository. Source: packages/pg-gwm/cmd/pg-gwm
// (main.go usage text, flags.go commonFlags, and each verb's own flag set) and
// its tldr page.
//
// "Approved" here means: no prompt in any session cwd, and --repo <path> is
// path-checked exactly like `git -C <path>` (CommandDir operand).
//
// Class per verb:
//
//	read-only     list (incl. --include-unmanaged), info, preserved list,
//	              config check, reap --dry-run, completion, version. They only
//	              read the lease database and git state.
//	lease-class   lease, renew, release, expire. They create, extend or end a
//	              lease on a worktree under pg-gwm's own configured worktree
//	              root; the tool bounds the count and refuses dirty or
//	              branch-in-use worktrees itself. No implicit effect is stated
//	              (git worktree add's precedent: registration bookkeeping is
//	              the tool's own), so the only path check is --repo.
//	operator      reap without --dry-run, preserved prune, serve. NOT approved.
//	              `preserved prune` and `serve` are absent from the schema, so
//	              any use abstains. `reap` is modeled only WITH --dry-run: its
//	              leading positional is an Unmodeled role that --dry-run
//	              removes (LeadingSkippedByFlags), so a reap without --dry-run
//	              is insufficient and abstains, and --dry-run=false (an
//	              attached value on a flag that takes none) is an unknown
//	              spelling and abstains too.
//
// Every verb's flags outside the lists below (including Go flag's single-dash
// spelling of a long flag) stay insufficient. Piped forms (`--format=json |
// jq`) and the documented command substitution `p=$(pg-gwm lease ...
// --print=path) && cd "$p"` classify through the shell layers, not here.

var pgGwmCommonFlags = map[string]FlagSpec{
	"--repo":   {Arity: ArityOne, Operand: CommandDir},
	"--format": literal1,
	"--json":   inert,
	"-h":       inert, "--help": inert,
}

func pgGwmFlags(withPrint bool, extra map[string]FlagSpec) map[string]FlagSpec {
	m := mergeFlags(pgGwmCommonFlags, extra)
	if withPrint {
		m["--print"] = literal1
	}
	return m
}

func pgGwmVerb(name string, flags map[string]FlagSpec, pos PositionalSpec) CommandSchema {
	return CommandSchema{
		Name:         name,
		Provenance:   "packages/pg-gwm/cmd/pg-gwm (main.go usage, flags.go, per-verb flag sets) and share/tldr/pages.common/pg-gwm.md, this repo 2026-11-20",
		Flags:        flags,
		Positionals:  pos,
		Stdin:        StdinNever,
		Stdout:       StdoutContent,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
}

var pgGwmSchema = func() CommandSchema {
	lease := map[string]FlagSpec{
		"--name": literal1, "--or-renew": inert, "--owner": literal1, "--metadata": literal1,
		"--ttl": literal1, "-b": literal1, "-B": literal1, "--detach": inert, "--wait": literal1,
	}
	renew := map[string]FlagSpec{"--name": literal1, "--ttl": literal1, "--metadata": literal1}
	byName := map[string]FlagSpec{"--name": literal1}
	info := map[string]FlagSpec{
		"--path": literal1, "--id": literal1, "--lease-id": literal1, "--name": literal1,
	}
	oneID := PositionalSpec{Rest: Literal}
	return CommandSchema{
		Name:         "pg-gwm",
		Provenance:   "packages/pg-gwm/cmd/pg-gwm main.go usage and share/tldr/pages.common/pg-gwm.md, this repo 2026-11-20",
		Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands: map[string]CommandSchema{
			// lease-class
			"lease":   pgGwmVerb("lease", pgGwmFlags(true, lease), oneID),
			"renew":   pgGwmVerb("renew", pgGwmFlags(true, renew), oneID),
			"release": pgGwmVerb("release", pgGwmFlags(false, byName), oneID),
			"expire":  pgGwmVerb("expire", pgGwmFlags(false, byName), oneID),
			// read-only
			"info": pgGwmVerb("info", pgGwmFlags(true, info), oneID),
			"list": pgGwmVerb("list", pgGwmFlags(false, map[string]FlagSpec{"--include-unmanaged": inert}), PositionalSpec{}),
			"preserved": {
				Name:         "preserved",
				Provenance:   "packages/pg-gwm/cmd/pg-gwm preserved.go (list, prune); only list is modeled",
				Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
				Subcommands: map[string]CommandSchema{
					"list": pgGwmVerb("list", pgGwmFlags(false, nil), PositionalSpec{}),
				},
			},
			"config": {
				Name:         "config",
				Provenance:   "packages/pg-gwm/cmd/pg-gwm configcmd.go (config check)",
				Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
				Subcommands: map[string]CommandSchema{
					"check": pgGwmVerb("check", pgGwmFlags(false, map[string]FlagSpec{
						"--config": {Arity: ArityOne, Operand: PathRead},
					}), PositionalSpec{}),
				},
			},
			"completion": pgGwmVerb("completion", map[string]FlagSpec{"-h": inert, "--help": inert}, PositionalSpec{Rest: Literal}),
			"version":    pgGwmVerb("version", map[string]FlagSpec{"-h": inert, "--help": inert}, PositionalSpec{}),
			// reap: approved only with --dry-run (see the file comment).
			"reap": pgGwmVerb("reap", pgGwmFlags(false, map[string]FlagSpec{
				"--all-repos": inert, "--dry-run": inert, "--deadline": literal1,
			}), PositionalSpec{
				Leading:               []OperandRole{Unmodeled},
				LeadingSkippedByFlags: []string{"--dry-run"},
			}),
		},
	}
}()
