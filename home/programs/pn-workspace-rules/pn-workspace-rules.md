# pn-workspace Conventions for Agents

Rules for AI agents working inside a `pn-workspace.toml` workspace. These apply to ANY repo whose flake is declared as a project in the workspace.

## Cardinal Rule

**Never modify `flake.nix` to point input URLs at local paths.** `pn-workspace-*` and `pn-ws-nix` inject `--override-input <name> <local-path>` at build/eval time — the lock file and flake.nix stay clean. Local-path URLs in `flake.nix` break every other consumer (CI, teammates, future you on another machine).

## Completion Gate

After completing any task in a project that participates in a `pn-workspace.toml`, you MUST run `pn-workspace-build` from the workspace root (or anywhere with `PN_WORKSPACE_ROOT` set) before declaring the task complete. Cross-project changes (a new flake output consumed by another workspace repo, for instance) only show up here.

```text
pn-workspace-build
```

Per-project `pn-ws-nix flake check` is necessary but not sufficient. Workspace-level build catches consumer-side breakage.

If `pn-workspace-build` fails, the task is not complete. Fix the failure (in this or the consuming project) and re-run.

## Builds and Validation

| Goal                                       | Use                                    | Don't use                                                                |
| ------------------------------------------ | -------------------------------------- | ------------------------------------------------------------------------ |
| Build the system (current host)            | `pn-workspace-build`                   | `darwin-rebuild build`, `nix build .#darwinConfigurations.<host>.system` |
| Activate the system                        | the **user** runs `pn-workspace-apply` | NEVER invoke from agent context                                          |
| Run `nix flake check` on a project         | `pn-ws-nix flake check`                | `nix flake check`                                                        |
| Run `nix flake check` across every project | `pn-workspace-flake-check`             | per-repo `nix flake check`                                               |
| Build a single package                     | `pn-ws-nix build .#<pkg>`              | `nix build .#<pkg>`                                                      |
| Evaluate an attribute                      | `pn-ws-nix eval .#<attr>`              | `nix eval .#<attr>`                                                      |
| Pre-commit checks across all repos         | `pn-workspace-pre-commit-check`        | per-repo `pre-commit run --all-files`                                    |
| Update flake locks across all repos        | `pn-workspace-update`                  | per-repo `nix flake update`                                              |

## When to Push

You don't need to push branches for builds to work. `pn-workspace-*` and `pn-ws-nix` operate on the local working tree. Push only when:

- The user explicitly asks.
- The work is ready for review/merge.

A failing remote build is **not** a reason to push agent-only branches.

## When `pn-ws-nix` Doesn't Apply

Two `nix` subcommands operate on lock state and override flags don't do anything useful:

- `nix flake update`
- `nix flake lock`

`pn-ws-nix` detects these and (by default) warns + exec's without overrides. Use `pn-workspace-update` for cross-repo lock refresh; use bare `nix flake lock` only when you specifically need single-repo lock manipulation.

## When `pn-ws-nix` Is Insufficient

Non-flake `nix` subcommands (`store *`, `profile list`, `log`, `key *`, `nar *`, `daemon`, `doctor`, `config show`) don't take `--override-input`. Use bare `nix` for those.

Interactive operations like `nix develop` / `nix shell` are user concerns; agents rarely need them.

## Command Surface Cheat-Sheet

Workspace-level (operate on every repo in the workspace):

```text
pn-workspace-build              Build the current host's system config
pn-workspace-apply              Activate (USER ONLY)
pn-workspace-pre-commit-check   Run pre-commit checks across all repos
pn-workspace-flake-check        Run `nix flake check` across all repos
pn-workspace-update             Refresh flake locks across all repos
pn-workspace-upgrade            Update + apply (USER ONLY for the apply step)
pn-workspace-rebase             Rebase each repo on its remote
pn-workspace-push               Push each repo (USER-INITIATED ONLY)
pn-workspace-status             Per-repo working-tree summary
```

Project-level workspace-aware (operate on one flake with overrides):

```text
pn-ws-nix <subcommand>          Generic wrapper around `nix`; injects overrides
```
