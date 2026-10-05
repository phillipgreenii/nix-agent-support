package cmddesc

// pg2-cjfpy.2: the git subcommands the agent-support plugins' skills instruct
// (integrate-branch's ff-merge-to-main / pull-request landing flow,
// beads-lifecycle's premise-freshness probes, session-wrapup's status checks,
// plan-decompose's patch bookkeeping) that registry.go's original fifteen did
// not model, plus git's two global options those skills pass: `-C <dir>` and
// `-c <key>=<value>`.
//
// Operator ruling (Phillip, 2026-10-04, parent epic pg2-cjfpy, verbatim):
// "any command which is supposed to work as part of a skill should be
// autoapproved". Each schema below still APPROVES only the shape the skills
// use and abstains on every neighbouring form (an unlisted flag is
// insufficient; a subcommand's mutating overload is Unmodeled), so the
// ruling widens approval to the instructed forms, not to the verb. Flags and
// synopses were verified against this host's git 2.54.0 (`git help <sub>`).
//
// Forms the skills instruct that this file deliberately does NOT model
// (they stay abstain; listed in the bead close report as ruling collisions
// or unmodelable): `git stash push|pop|apply|drop` and `git checkout
// <branch>` (the agent rule R3 forbids stashing/re-checking-out the canonical
// clone on its own initiative — the skills instruct them only after asking
// the operator), `git apply` (the paths a patch writes are inside the patch),
// `git config <key> <value>` (config writes are judged per key, P16 — a later
// phase), `git rebase -i`/`--exec` (editor / arbitrary commands).

// gitInertConfigPairFlag is git's global `-c <key>=<value>`: inert ONLY for
// the closed pairs in allowedLiteralSets["git-inert-config-pair"]; any other
// pair (alias.*, core.pager, core.fsmonitor=<program>, ...) is insufficient.
var gitInertConfigPairFlag = FlagSpec{Arity: ArityOne, Operand: AllowedLiteral("git-inert-config-pair")}

// gitMergeBaseSchema: `git merge-base [--all] [--octopus] [--independent]
// [--fork-point] [--is-ancestor] <commit>...` — read-only ancestry queries
// over the object database (git help merge-base). Positionals are revisions.
var gitMergeBaseSchema = CommandSchema{
	Name:       "merge-base",
	Provenance: "git version 2.54.0, git help merge-base",
	Flags: map[string]FlagSpec{
		"--is-ancestor": inert, "--fork-point": inert,
		"-a": inert, "--all": inert,
		"--octopus": inert, "--independent": inert,
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

// gitCherrySchema: `git cherry [-v] [<upstream> [<head> [<limit>]]]` —
// read-only: lists commits not yet applied upstream (git help cherry).
var gitCherrySchema = CommandSchema{
	Name:       "cherry",
	Provenance: "git version 2.54.0, git help cherry",
	Flags: map[string]FlagSpec{
		"-v": inert,
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

// gitRangeDiffSchema: `git range-diff <range1> <range2>` — read-only
// comparison of two commit ranges (git help range-diff). Output is diff
// content, so Stdout is content.
var gitRangeDiffSchema = CommandSchema{
	Name:       "range-diff",
	Provenance: "git version 2.54.0, git help range-diff",
	Flags: map[string]FlagSpec{
		"--no-color":        inert,
		"--stat":            inert,
		"--creation-factor": literalOpt,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitLsTreeSchema: `git ls-tree [-d] [-r] [-t] [-l] [-z] [--name-only]
// [--name-status] [--full-name] [--full-tree] <tree-ish> [<path>...]` —
// lists the entries of a tree object; names and modes only, never blob
// content (git help ls-tree).
var gitLsTreeSchema = CommandSchema{
	Name:       "ls-tree",
	Provenance: "git version 2.54.0, git help ls-tree",
	Flags: map[string]FlagSpec{
		"-d": inert, "-r": inert, "-t": inert, "-l": inert, "--long": inert, "-z": inert,
		"--name-only": inert, "--name-status": inert,
		"--full-name": inert, "--full-tree": inert,
		"--abbrev": literalOpt,
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

// gitLsFilesSchema: `git ls-files [-c] [-o] [-m] [-d] [-s] [-z]
// [--exclude-standard] [--error-unmatch] [--full-name] [--] [<file>...]` —
// lists index / working-tree FILE NAMES (git help ls-files); the pathspecs
// only filter, so they are Literal. The ignore-file options that name a file
// to read (-x/-X/--exclude-from/--exclude-per-directory) are left unmodeled.
var gitLsFilesSchema = CommandSchema{
	Name:       "ls-files",
	Provenance: "git version 2.54.0, git help ls-files",
	Flags: map[string]FlagSpec{
		"-c": inert, "--cached": inert,
		"-o": inert, "--others": inert,
		"-m": inert, "--modified": inert,
		"-d": inert, "--deleted": inert,
		"-s": inert, "--stage": inert,
		"-z": inert, "--exclude-standard": inert,
		"--error-unmatch": inert, "--full-name": inert,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: "."},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitGrepSchema: `git grep [-c] [-n] [-l] [-L] [-i] [-w] [-F] [-E] [-h] [-I]
// [-v] [-q] [-e <pattern>] [--cached] [<tree>...] [--] [<pathspec>...]` —
// searches tracked files; the file CONTENT flows to stdout, so the implicit
// read of "." and Stdout content mirror gitShowSchema. Options that run a
// program (-O/--open-files-in-pager) or read a pattern file (-f), and
// --no-index (searches arbitrary directories), are left unmodeled.
var gitGrepSchema = CommandSchema{
	Name:       "grep",
	Provenance: "git version 2.54.0, git help grep",
	Flags: map[string]FlagSpec{
		"-c": inert, "--count": inert,
		"-n": inert, "--line-number": inert,
		"-l": inert, "--files-with-matches": inert, "--name-only": inert,
		"-L": inert, "--files-without-match": inert,
		"-i": inert, "--ignore-case": inert,
		"-w": inert, "--word-regexp": inert,
		"-F": inert, "--fixed-strings": inert,
		"-E": inert, "--extended-regexp": inert,
		"-G": inert, "--basic-regexp": inert,
		"-P": inert, "--perl-regexp": inert,
		"-h": inert, "-H": inert,
		"-I": inert, "-a": inert, "--text": inert,
		"-v": inert, "--invert-match": inert,
		"-q": inert, "--quiet": inert,
		"-z": inert, "--null": inert,
		"--cached": inert, "--untracked": inert,
		"--no-color": inert,
		"-e":         literal1,
		"-A":         literal1, "-B": literal1, "-C": literal1,
		"-m": literal1, "--max-count": literal1,
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

// gitSymbolicRefSchema: the READ form only — `git symbolic-ref [-q]
// [--short] <name>` prints where a symbolic ref points (git help
// symbolic-ref). The write form `git symbolic-ref <name> <ref>` has a SECOND
// positional, which is Unmodeled (abstain), and -d/--delete is unmodeled.
var gitSymbolicRefSchema = CommandSchema{
	Name:       "symbolic-ref",
	Provenance: "git version 2.54.0, git help symbolic-ref",
	Flags: map[string]FlagSpec{
		"-q": inert, "--quiet": inert,
		"--short": inert, "--no-recurse": inert,
	},
	Positionals: PositionalSpec{
		Leading: []OperandRole{Literal},
		Rest:    Unmodeled,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitRemoteSchema: the LISTING form only — `git remote [-v]` (git help
// remote). Every subcommand (add, remove, rename, set-url, ...) arrives as a
// positional, which is Unmodeled (abstain).
var gitRemoteSchema = CommandSchema{
	Name:       "remote",
	Provenance: "git version 2.54.0, git help remote",
	Flags: map[string]FlagSpec{
		"-v": inert, "--verbose": inert,
	},
	Positionals: PositionalSpec{Rest: Unmodeled},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathRead, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitStashSchema: the LISTING form only — `git stash list [<log-options>]`
// (git help stash). stash push/pop/apply/drop/clear are NOT modeled: the
// agent rule R3 forbids stashing the canonical clone on the agent's own
// initiative, and the skills instruct them only after asking the operator,
// so they stay abstain (reported as a ruling collision, not overridden).
var gitStashSchema = CommandSchema{
	Name:         "stash",
	Provenance:   "git version 2.54.0, git help stash",
	Flags:        map[string]FlagSpec{},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"list": {
			Name:        "list",
			Provenance:  "git version 2.54.0, git help stash",
			Flags:       map[string]FlagSpec{},
			Positionals: PositionalSpec{Rest: Literal},
			ImplicitEffects: []ImplicitEffect{
				{Role: PathRead, Target: ".git"},
			},
			Stdin:        StdinNever,
			Stdout:       StdoutMetadata,
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
		},
	},
}

// gitFetchSchema: `git fetch [-q] [-p] [-t] [-a] [--depth <n>] [<remote>
// [<refspec>...]]` (git help fetch). The leading positional names the remote
// (KindRemote, Operation "fetch"): data flows INTO the clone's own refs and
// object store, and no flag modeled here runs a program (--upload-pack /
// --recurse-submodules / --server-option are unmodeled → abstain).
// effectpolicy.RemoteMutation permits "fetch" only for a plain remote NAME;
// a URL or path, and the dynamic default remote of a bare `git fetch`, are
// Unknown. The refspec positionals are Literal.
var gitFetchSchema = CommandSchema{
	Name:       "fetch",
	Provenance: "git version 2.54.0, git help fetch",
	Flags: map[string]FlagSpec{
		"-q": inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
		"-p": inert, "--prune": inert,
		"-t": inert, "--tags": inert, "--no-tags": inert,
		"-a": inert, "--append": inert,
		"--all":       inert,
		"--depth":     literal1,
		"--unshallow": inert,
		"--atomic":    inert,
	},
	Positionals: PositionalSpec{
		Leading:         []OperandRole{Remote("fetch")},
		LeadingOptional: true,
		Rest:            Literal,
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: Remote("fetch"), Target: "<default-remote>", Dynamic: true, WhenNoPositionals: true},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutNone,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitRebaseSchema: `git rebase [--continue | --abort | --skip | --quit]
// [--autostash] [--onto <newbase>] [<upstream> [<branch>]]` (git help
// rebase) — the scripted landing step `git -c rerere.enabled=false -C <wt>
// rebase <primary>` and its recovery verbs. It rewrites the CURRENT branch
// and its working tree, which the implicit PathModify of "." and ".git"
// states (the same access class git commit/merge get). -i/--interactive
// (opens an editor), -x/--exec (runs commands), --no-verify and the
// strategy options are unmodeled → abstain. Positionals are revisions.
var gitRebaseSchema = CommandSchema{
	Name:       "rebase",
	Provenance: "git version 2.54.0, git help rebase",
	Flags: map[string]FlagSpec{
		"--continue": inert, "--abort": inert, "--skip": inert, "--quit": inert,
		"--autostash": inert, "--no-autostash": inert,
		"--onto": literal1,
		"--root": inert,
		"-q":     inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
		"--reapply-cherry-picks": inert, "--no-reapply-cherry-picks": inert,
		"--keep-base": inert,
	},
	Positionals: PositionalSpec{Rest: Literal},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: "."},
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitMergeSchema: ONLY the fast-forward form — `git merge --ff-only
// <commit>` (git help merge): the landing step `git -C <canonical clone>
// merge --ff-only <feature branch>` and the post-push catch-up. The
// positional is Unmodeled unless --ff-only appeared (the gitBranchSchema
// RestOverride trick), so a plain `git merge x` (which may create a merge
// commit and open an editor) abstains; --ff-only cannot create a commit. The
// implicit PathModify of "." and ".git" states the working-tree and ref
// update.
var gitMergeSchema = CommandSchema{
	Name:       "merge",
	Provenance: "git version 2.54.0, git help merge",
	Flags: map[string]FlagSpec{
		"--ff-only": inert,
		"-q":        inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
		"--no-stat": inert,
	},
	Positionals: PositionalSpec{
		Rest:         Unmodeled,
		RestOverride: RestOverride{Flags: []string{"--ff-only"}, Role: Literal},
	},
	ImplicitEffects: []ImplicitEffect{
		{Role: PathModify, Target: "."},
		{Role: PathModify, Target: ".git"},
	},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// gitCheckoutSchema: ONLY the path-restore form — `git checkout [--theirs |
// --ours] -- <path>...` (git help checkout): resolving a flake.lock rebase
// conflict, or discarding an experimental change to named files. The
// literal `--` is modeled as a (registered, inert) FLAG with EndOfOptions off
// so that its presence is observable: RestOverride then turns the positionals
// into PathModify ONLY when `--` appeared, and a branch switch (`git
// checkout main`, no `--`) stays Unmodeled → abstain (R3: the agent does not
// move the canonical clone off its branch). After `--`, a `-`-prefixed path
// is parsed as an unknown flag and abstains — fail closed.
var gitCheckoutSchema = CommandSchema{
	Name:       "checkout",
	Provenance: "git version 2.54.0, git help checkout",
	Flags: map[string]FlagSpec{
		"--":       inert,
		"--theirs": inert, "--ours": inert,
	},
	Positionals: PositionalSpec{
		Rest:         Unmodeled,
		RestOverride: RestOverride{Flags: []string{"--"}, Role: PathModify},
	},
	Stdin:       StdinNever,
	Stdout:      StdoutNone,
	UnknownFlag: UnknownFlagInsufficient,
	// EndOfOptions is deliberately false: see the doc comment.
}

// gitExtendedSubcommands are merged into gitSchema.Subcommands (registry.go).
func gitExtendedSubcommands() map[string]CommandSchema {
	return map[string]CommandSchema{
		"merge-base":   gitMergeBaseSchema,
		"cherry":       gitCherrySchema,
		"range-diff":   gitRangeDiffSchema,
		"ls-tree":      gitLsTreeSchema,
		"ls-files":     gitLsFilesSchema,
		"grep":         gitGrepSchema,
		"symbolic-ref": gitSymbolicRefSchema,
		"remote":       gitRemoteSchema,
		"stash":        gitStashSchema,
		"fetch":        gitFetchSchema,
		"rebase":       gitRebaseSchema,
		"merge":        gitMergeSchema,
		"checkout":     gitCheckoutSchema,
	}
}

// withGitExtensions merges gitExtendedSubcommands into the base table; a name
// present in both is a programming error and panics at init so it cannot ship.
func withGitExtensions(base map[string]CommandSchema) map[string]CommandSchema {
	for name, sub := range gitExtendedSubcommands() {
		if _, dup := base[name]; dup {
			panic("cmddesc: git subcommand " + name + " defined in both registry.go and registry_git_ext.go")
		}
		base[name] = sub
	}
	return base
}
