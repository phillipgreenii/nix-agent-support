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
// # What is deliberately NOT here
//
// pg2-cjfpy.4 held every write form for the parent epic's gate-5 question; the
// operator ruled it (2026-10-05: class D forms are APPROVE under the
// 2026-10-04 ruling; a form needing a NEW effectpolicy judgment waits for
// tc-o14i5.3.10, the P15 approve-reachability tooling) and pg2-maars landed the
// class D forms that reach APPROVE with the EXISTING effectpolicy operations
// (push-lease, pr-create-draft, tracker-write on beads): `gh stack push`,
// `gh stack submit --auto`, `gh stack sync`, `gh stack unstack --local`,
// df-close-focus, df-wire, df-deferred write|read, df-pull, lat-wire,
// rc-claim, rc-park and rc-fp. The tooling landed (tc-o14i5.3.10, commit
// 171eaa4f7: internal/effectpolicy/p15report.go) and pg2-19hdz landed the held
// forms that needed a NEW effectpolicy judgment, each with a P15 report
// pinned under internal/effectpolicy/testdata/p15/: `gh pr edit` (the new
// "pr-edit" remote operation), `rc-publish` (push-lease on origin,
// pr-create-draft and pr-edit on github), `pjira comment|create|transition`
// (tracker-write widened from the beads resource to also cover "jira"),
// `gh stack unstack` without --local (the new "stack-unstack" operation) and
// `gh stack link` without --open (a plain atomic push, draft PR creations,
// "pr-edit" base fix-ups of existing PRs and the new "stack-link" operation).
// link's draft default is what let it land: its help documents --open as
// "mark new and existing PRs as ready for review" but never says whether the
// PRs it creates are drafts without it, and the unattended hook denies even
// `gh stack link --help`, so the default was verified from the extension's
// upstream source instead (github/gh-stack cmd/link.go: new PRs are created
// with draft = !open, branch arguments are pushed non-force and atomic via
// git.Push(remote, branches, false, true), and fixBaseBranches edits the base
// of EXISTING PRs through UpdatePRBase) and from its shipped skill doc.
// Still held, listed in TestZiprecruiterUnlandedVerbsAreAbsent: `rc-validate`
// (its caller-supplied gate command needs a child-command interpreter that
// rebases the child's working directory onto <wt>, and a realistic Gradle
// gate such as `./gradlew test` still cannot reach approve even then: no
// `gradlew` schema exists, so the child abstains as "no schema for gradlew";
// adding one is its own approval decision, not a fact of rc-validate).
// Forms that collide with an explicit REJECT/ABSTAIN ruling are not
// overridden: `gh stack merge` (mirrors `gh pr merge`, Reject),
// `gh stack submit|link --open` (marks PRs ready, the `gh pr ready` Abstain
// ruling pg2-psiqh).
//
// # Effect model
//
//   - A read of a named remote resource (GitHub PR/stack state, Jira, beads)
//     is Remote("read"), Permitted by effectpolicy.RemoteMutation.
//   - `gh stack init|add|rebase|up|down|top|bottom|trunk|checkout` move or
//     rewrite the CURRENT checkout's branches and working tree: PathModify of
//     "." and ".git", exactly gitRebaseSchema's (registry_git_ext.go in
//     pg2-cjfpy.2) and gitCommitSchema's access class.
//   - A bookkeeping write to the beads database (the bd-writing daily-focus /
//     local-alert-triage / zr-refactor scripts) is Remote("tracker-write") on
//     "beads", the SAME operation `bd create|update|close|comment` carries;
//     effectpolicy.RemoteMutation Permits it by data (operator ruling
//     2026-10-04, pg2-cjfpy).
//   - `gh stack push|submit|sync` push every active branch of the stack with a
//     per-branch --force-with-lease: Remote("push-lease") on the default
//     remote (the same implicit "<default-remote>" `git push` uses) or on the
//     --remote operand, which RemoteMutation Permits for a plain remote NAME
//     (ADR 0075 R6). The stack is the tracked set of branches, never an
//     --all/--tags/--mirror bulk push. `submit` also creates PRs: only
//     `--auto` makes them drafts (the verb's own --help), so --auto is what
//     retargets the effect to "pr-create-draft"; without it the effect stays
//     "mutate" and abstains. The stack object GitHub keeps for those PRs, and
//     the base-branch fix-ups on them, are the PR flow's own bookkeeping, not
//     a separate write kind.
//   - `gh stack unstack` without --local dissolves the stack GROUPING on
//     GitHub (the PRs and branches stay): Remote("stack-unstack") on
//     "github", which RemoteMutation Permits (pg2-19hdz); --local retargets
//     it to "read".
//   - `gh stack link` (without --open) is four remote effects: a plain,
//     non-force, atomic push of the branch arguments (Remote("push") on the
//     default remote or the --remote operand), draft PR creations
//     ("pr-create-draft"), base fix-ups of existing PRs ("pr-edit") and the
//     stack grouping itself ("stack-link"), plus a read of the PRs it looks
//     up. Every operation is one RemoteMutation already judges or now Permits
//     on "github" (pg2-19hdz).
//   - A Jira write (`pjira comment|create|transition`) is Remote(
//     "tracker-write") on "jira": the same operation bd carries on "beads",
//     which RemoteMutation now Permits for both resources by DATA (pg2-19hdz).
//   - A PR metadata write (`gh pr edit`) is Remote("pr-edit") on "github"
//     (Permitted, pg2-19hdz). `rc-publish` bundles the pieces it performs: a
//     push-lease on the plain remote "origin", a draft PR creation and the
//     PR assignee edit, plus the reads that find an existing PR.
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
	zrReadsBeads = zrRemoteRead("beads")
	// zrWritesBeads is a bookkeeping write to the beads database: the same
	// Remote("tracker-write") on "beads" every `bd create|update|close|...`
	// carries (writesBeads in registry_plugin_tools.go).
	zrWritesBeads = ImplicitEffect{Role: Remote("tracker-write"), Target: "beads"}
	zrReadsGitHub = zrRemoteRead("github")
	zrReadsJira   = zrRemoteRead("jira")
	// zrWritesJira is a write to Jira (a comment, an issue, a workflow
	// transition): the same Remote("tracker-write") operation bd carries on
	// "beads", aimed at the "jira" resource (pg2-19hdz widened
	// effectpolicy.RemoteMutation's tracker-write to cover both by data).
	zrWritesJira = ImplicitEffect{Role: Remote("tracker-write"), Target: "jira"}
)

// zrCheckoutMutation is the implicit local access class of a verb that moves
// or rewrites the current checkout's branches and working tree.
var zrCheckoutMutation = []ImplicitEffect{
	{Role: PathModify, Target: "."},
	{Role: PathModify, Target: ".git"},
}

// zrPushDefaultRemote is the implicit per-branch --force-with-lease push of a
// `gh stack` verb to the default remote, the same implicit effect `git push`
// uses (its remote is a git-config fact, not argv; see
// effectpolicy.RemoteMutation's defaultRemotePush).
var zrPushDefaultRemote = ImplicitEffect{Role: Remote("push-lease"), Target: "<default-remote>", Dynamic: true}

// zrStackRemoteFlag is the `--remote <name>` flag of the remote `gh stack`
// verbs. Its operand is a push-lease remote, so a URL or path remote is not
// a plain remote name and abstains; the default-remote effect is still
// emitted alongside (an over-report, in the fail-closed direction).
var zrStackRemoteFlag = map[string]FlagSpec{
	"--remote": {Arity: ArityOne, Operand: Remote("push-lease")},
}

// zrPushPlainDefaultRemote is `gh stack link`'s implicit push of its branch
// arguments to the default remote: a PLAIN push (the extension calls
// git.Push(remote, branches, false, true): force=false, atomic=true, so no
// --force and no --force-with-lease), the same implicit "<default-remote>"
// effect `git push` uses, with the operation "push" instead of "push-lease".
var zrPushPlainDefaultRemote = ImplicitEffect{Role: Remote("push"), Target: "<default-remote>", Dynamic: true}

// zrLinkRemoteFlag is the `--remote <name>` flag of `gh stack link`. Its
// operand is a plain "push" remote, so a URL or path remote abstains; the
// default-remote effect is still emitted alongside (an over-report, in the
// fail-closed direction).
var zrLinkRemoteFlag = map[string]FlagSpec{
	"--remote": {Arity: ArityOne, Operand: Remote("push")},
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
		zrDfCloseFocusSchema, zrDfWireSchema, zrDfDeferredSchema, zrDfPullSchema, zrLatWireSchema,
		zrRcClaimSchema, zrRcParkSchema, zrRcFpSchema, zrRcPublishSchema,
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

// zrGhStackLinkSchema is `gh stack link [--base B] [--remote R]
// <stack-number | branch-or-pr> <branch-or-pr> [...]` (pg2-19hdz): "Create or
// update a stack on GitHub from branch names, PR numbers, or PR URLs ... This
// command does not rely on gh-stack local tracking state", so it declares no
// local path effect. Its help: branch arguments are pushed to the remote, new
// PRs are created for branches without one, existing PRs whose base does not
// match the expected chain are corrected, and a stack is created or extended
// (existing PRs are never removed). Upstream source (github/gh-stack
// cmd/link.go): new PRs are DRAFTS unless --open (CreatePR(..., !opts.open));
// the push is plain and atomic (git.Push(remote, branches, false, true));
// fixBaseBranches edits existing PRs' bases (UpdatePRBase). --open (marks new
// and existing PRs ready for review) is deliberately NOT modeled, so every
// --open form abstains (the gh pr ready Abstain ruling, pg2-psiqh). The two
// leading operands are the CLI's own minimum of two arguments.
var zrGhStackLinkSchema = func() CommandSchema {
	s := zrGhStackLeaf("link",
		zrFlags(zrLinkRemoteFlag, map[string]FlagSpec{"--base": literal1}),
		PositionalSpec{Leading: []OperandRole{Literal, Literal}, Rest: Literal}, StdoutMetadata,
		zrPushPlainDefaultRemote, zrReadsGitHub,
		ImplicitEffect{Role: Remote("pr-create-draft"), Target: "github"},
		ImplicitEffect{Role: Remote("pr-edit"), Target: "github"},
		ImplicitEffect{Role: Remote("stack-link"), Target: "github"})
	s.Provenance = "gh stack link --help text embedded in the gh-stack 0.1.0 extension binary, and its upstream source (github/gh-stack cmd/link.go), this host 2026-10-14"
	return s
}()

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
//     shortcut, the same access class as `git add` + `git commit`. `push`, `submit --auto`, `sync` and
//     `unstack` (pg2-maars; pg2-19hdz added `unstack` without --local) follow
//     the effect model above; `link` without --open (pg2-19hdz, draft default
//     verified from the extension's upstream source, see the header) does too.
//     ABSENT (abstain): merge, modify, `submit` without --auto, `--open` on
//     any verb, and the `delete` alias of `unstack`.
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
			// pg2-maars: `gh stack push [--remote R]` -- "Push active branches
			// in the current stack to the remote ... explicit per-branch
			// --force-with-lease checks".
			"push": zrGhStackLeaf("push", zrStackRemoteFlag,
				PositionalSpec{}, StdoutMetadata, zrPushDefaultRemote),
			// `gh stack submit --auto [--remote R]`: "Push all branches and
			// create or update a stack of PRs"; "With --auto, new PRs are
			// created as drafts unless you pass --open". --open is unmodeled
			// (marks PRs ready: the gh pr ready Abstain ruling, pg2-psiqh).
			"submit": zrGhStackLeaf("submit",
				zrFlags(zrStackRemoteFlag, map[string]FlagSpec{
					"--auto": {Transform: EffectTransform{Kind: TransformRetargetRemote, From: "mutate", To: "pr-create-draft"}},
				}),
				PositionalSpec{}, StdoutMetadata,
				zrPushDefaultRemote, ImplicitEffect{Role: Remote("mutate"), Target: "github"}),
			// `gh stack sync [--prune] [--remote R]`: "Fetch, rebase, push, and
			// sync PR state for the current stack"; "Sync never opens pull
			// requests". The fetch comes from the same remote it pushes to,
			// so the push effect subsumes it.
			"sync": zrGhStackLeaf("sync",
				zrFlags(zrStackRemoteFlag, map[string]FlagSpec{"--prune": inert}),
				PositionalSpec{}, StdoutMetadata,
				append([]ImplicitEffect{zrPushDefaultRemote, zrReadsGitHub}, zrCheckoutMutation...)...),
			"link": zrGhStackLinkSchema,
			// `gh stack unstack [--local] [<n>]`: "Remove a stack from local
			// tracking and unstack it on GitHub"; "Use --local to only remove
			// local tracking without touching remote (GitHub)"; "GitHub
			// decides which pull requests can be unstacked: PRs that are
			// queued for merge or have auto-merge enabled are left stacked".
			// Without --local the verb dissolves the stack grouping on GitHub
			// (the PRs and their branches stay): the implicit "stack-unstack"
			// operation, which RemoteMutation Permits (pg2-19hdz). --local
			// retargets it to a read. The `delete` alias is not modeled.
			"unstack": zrGhStackLeaf("unstack",
				map[string]FlagSpec{
					"--local": {Transform: EffectTransform{Kind: TransformRetargetRemote, From: "stack-unstack", To: "read"}},
				},
				PositionalSpec{Rest: Literal}, StdoutMetadata,
				ImplicitEffect{Role: PathModify, Target: ".git"},
				ImplicitEffect{Role: Remote("stack-unstack"), Target: "github"}),
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

// zrPjiraWriteLeaf builds one WRITE verb of the Jira CLI (pg2-19hdz): the
// read leaf's fail-closed flag policy, with the tracker-write effect on the
// "jira" resource instead of a read. The persistent --config flag is NOT
// modeled on a write: it names the TOML that holds the Jira base URL and the
// credential source, so a config the agent wrote could aim a write (and the
// token) at another host, and no plugin passes it on a write (the read verbs
// keep it). The verbs print a small {key,id|url|to} JSON of what they wrote:
// metadata.
func zrPjiraWriteLeaf(verb string, flags map[string]FlagSpec, pos PositionalSpec) CommandSchema {
	s := zrPjiraLeaf(verb, flags, pos, StdoutMetadata)
	delete(s.Flags, "--config")
	s.Provenance = "pjira " + verb + " --help (the verb's command definition in pjira's own source, phillipg-nix-repo-base modules/jira/cmd/pjira/main.go; this host 2026-10-14)"
	s.ImplicitEffects = []ImplicitEffect{zrWritesJira}
	return s
}

// zrPjiraSchema: the READ verbs -- `issue <KEY>`, `search --jql ...`,
// `auth-status` (zr-refactor status.md: "look it up with the pjira CLI";
// df-survey's Jira survey) -- and, since pg2-19hdz, the three WRITE verbs
// `comment <KEY> <BODY>`, `create --project P --type T --summary S
// [--description D]` and `transition <KEY> <TO>` (pjira --help: "Add a
// plain-text comment to an issue", "Create a new issue", "Transition an issue
// to a target workflow state"). The writes are Remote("tracker-write") on
// "jira", which effectpolicy.RemoteMutation Permits (P15 report
// internal/effectpolicy/testdata/p15/pjira-writes.md). daily-focus close.md
// still has the operator approve each drafted comment IN THE CONVERSATION
// before the comment is posted: that human step is the skill's own, and
// approving the posting command does not replace it.
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
		"comment": zrPjiraWriteLeaf("comment", nil,
			PositionalSpec{Leading: []OperandRole{Literal, Message}}),
		"create": zrPjiraWriteLeaf("create", map[string]FlagSpec{
			"--project": literal1, "--type": literal1,
			"--summary":     {Arity: ArityOne, Operand: Message},
			"--description": {Arity: ArityOne, Operand: Message},
		}, PositionalSpec{}),
		"transition": zrPjiraWriteLeaf("transition", nil,
			PositionalSpec{Leading: []OperandRole{Literal, Literal}}),
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
						// --fresh is a no-value bool (skip the read_ttl cache
						// read-through and ask the origin; pg2-cw6b3.2's
						// pg-desk gather passes it, bead pg2-e2h4q).
						"--fresh": inert,
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

// ---- bd-writing daily-focus / local-alert-triage scripts (pg2-maars) --------

// df-close-focus <focus-id> <date> --notes-file P --summary-file P --reason T
// [--actor ID]: per touched bead `bd update --append-notes`, then `bd close
// <focus-id> --force --reason T`. Beads bookkeeping writes; the two files are
// path reads.
var zrDfCloseFocusSchema = zrScript("df-close-focus",
	"Sequence a focus bead's mechanical close-out",
	map[string]FlagSpec{
		"--notes-file":   {Arity: ArityOne, Operand: PathRead},
		"--summary-file": {Arity: ArityOne, Operand: PathRead},
		"--reason":       {Arity: ArityOne, Operand: Message},
		"--actor":        literal1,
	},
	PositionalSpec{Leading: []OperandRole{Literal, Literal}}, StdinNever, StdoutMetadata,
	zrWritesBeads)

// df-wire <focus-id> (--records - | --manifest F) [--drop H,..] [--merge K=A,..]
// [--dry-run] [--actor ID]: "Mint + duplicate-free bulk wiring + verify" --
// only `bd create`, `bd dep add`, `bd list`, `bd config get` and the
// df-verify-wiring read-back. --records - arrives on stdin.
var zrDfWireSchema = zrScript("df-wire",
	"Mint + duplicate-free bulk wiring + verify, for one focus bead",
	map[string]FlagSpec{
		"--records":  literal1,
		"--manifest": {Arity: ArityOne, Operand: PathRead},
		"--drop":     literal1, "--merge": literal1,
		"--dry-run": inert,
		"--actor":   literal1,
	},
	PositionalSpec{Leading: []OperandRole{Literal}}, StdinAlways, StdoutContent,
	zrWritesBeads)

// df-deferred write <focus-id> (--manifest F | --records F|-) [--dry-run]
// [--actor ID] splices the "Deferred today" section into the focus bead's
// description (one `bd update`); df-deferred read <focus-id> parses it back
// (`bd show`). Only the verbs the daily-focus commands run are modeled.
var zrDfDeferredSchema = CommandSchema{
	Name:         "df-deferred",
	Provenance:   "df-deferred --help (Read/write the focus bead's \"Deferred today\" description section, this host 2026-10-05)",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"write": zrScript("write",
			"df-deferred write: replace the Deferred today section",
			map[string]FlagSpec{
				"--manifest": {Arity: ArityOne, Operand: PathRead},
				"--records":  literal1,
				"--dry-run":  inert,
				"--actor":    literal1,
			},
			PositionalSpec{Leading: []OperandRole{Literal}}, StdinAlways, StdoutMetadata,
			zrWritesBeads),
		"read": zrScript("read",
			"df-deferred read: print the Deferred today records",
			nil,
			PositionalSpec{Leading: []OperandRole{Literal}}, StdinNever, StdoutContent,
			zrReadsBeads),
	},
}

// df-pull [--dry-run] [--actor ID] [<k> | <handle>...]: tops up today's focus
// bead from its deferred section by running df-resolve-focus, df-deferred
// read, df-survey --deep (reads of GitHub, Jira and beads), df-wire and
// df-deferred write (beads bookkeeping writes). The deep survey's records
// arrive on its internal pipe, not df-pull's own stdin.
var zrDfPullSchema = zrScript("df-pull",
	"Top up today's focus bead from its Deferred today section",
	map[string]FlagSpec{"--dry-run": inert, "--actor": literal1},
	PositionalSpec{Rest: Literal}, StdinNever, StdoutContent,
	zrWritesBeads, zrReadsGitHub, zrReadsJira)

// lat-wire --decisions <file|-> [--actor ID]: "local-alert-triage's mutating
// bd-wiring step" -- bd create / update / comment / reopen / close / dep add
// (all beads bookkeeping). The decision-list JSON is read from the file, or
// from stdin for "-" (hence StdinAlways, as df-survey --records -).
var zrLatWireSchema = zrScript("lat-wire",
	"local-alert-triage's mutating bd-wiring step",
	map[string]FlagSpec{
		"--decisions": {Arity: ArityOne, Operand: PathRead},
		"--actor":     literal1,
	},
	PositionalSpec{}, StdinAlways, StdoutContent,
	zrWritesBeads)

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

// rc-claim <scope-label> <rc-fix|rc-scan> [--app L] [--exclude-app L]...
// --actor ID: "Atomically claim one zr-refactor campaign bead" (`bd ready
// --claim`) and print a projected JSON row. A beads bookkeeping write.
var zrRcClaimSchema = zrScript("rc-claim",
	"Atomically claim one zr-refactor campaign bead and print a projected JSON row",
	map[string]FlagSpec{"--app": literal1, "--exclude-app": literal1, "--actor": literal1},
	PositionalSpec{Leading: []OperandRole{Literal, Literal}}, StdinNever, StdoutContent,
	zrWritesBeads)

// rc-park <bead> <reason> --fingerprint FP [--unpushed --isolation WHERE]
// --actor ID: "Park a zr-refactor bead as human-blocked in one bd call"
// (`bd update`, after a `bd show` read). A beads bookkeeping write.
var zrRcParkSchema = zrScript("rc-park",
	"Park a zr-refactor bead as human-blocked in one bd call",
	map[string]FlagSpec{
		"--fingerprint": literal1, "--unpushed": inert, "--isolation": literal1,
		"--actor": literal1,
	},
	PositionalSpec{Leading: []OperandRole{Literal, Message}}, StdinNever, StdoutMetadata,
	zrWritesBeads)

// rc-fp <app-bead> <fp> <reason> --actor ID: "Register an instance
// fingerprint as resolved on an app bead" (`bd show` + `bd update`). A beads
// bookkeeping write.
var zrRcFpSchema = zrScript("rc-fp",
	"Register an instance fingerprint as resolved on an app bead",
	map[string]FlagSpec{"--actor": literal1},
	PositionalSpec{Leading: []OperandRole{Literal, Literal, Message}}, StdinNever, StdoutMetadata,
	zrWritesBeads)

// rc-publish <wt> <branch> <title> <abs-body-file> (pg2-19hdz): "Publish a
// campaign app branch: push, ensure draft PR, WIP-pin (verified)". The script
// runs, in order: `git -C <wt> push -u --force-with-lease origin <branch>`
// (a push-lease on the plain remote "origin"; after a rejected push a fetch
// and a rebase onto origin/<branch> rewrite the worktree, so <wt> is a
// PathModify exactly like rc-branch's); a read of the existing PR for the
// branch (`pg-connector pr list`, then `gh pr list`: reads of GitHub); the
// draft PR creation when none exists (pr-create-draft: it is "ensure draft
// PR", never a ready PR); `pg-desk wip on|show` (the local triage board, no
// effect); and `gh pr edit <n> --add-assignee @me` (pr-edit). <abs-body-file>
// is the PR body: a path read, so a secret path is refused by the path
// policy. The script takes no flags besides -h/--help.
var zrRcPublishSchema = zrScript("rc-publish",
	"Publish a campaign app branch: push, ensure draft PR, WIP-pin (verified)",
	nil,
	PositionalSpec{Leading: []OperandRole{PathModify, Literal, Message, PathRead}}, StdinNever, StdoutMetadata,
	ImplicitEffect{Role: Remote("push-lease"), Target: "origin"},
	zrReadsGitHub,
	ImplicitEffect{Role: Remote("pr-create-draft"), Target: "github"},
	ImplicitEffect{Role: Remote("pr-edit"), Target: "github"})

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
