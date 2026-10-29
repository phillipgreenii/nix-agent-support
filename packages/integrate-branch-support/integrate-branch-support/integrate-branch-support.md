# integrate-branch-support

> Advisory detector for the `integrate-branch` skill: gathers a repo's branch-integration facts and, when it can, a recommended strategy.
> Emits one JSON object on stdout (`strategy`, `reason`, `primary_branch`, `canonical`, `remote`, `open_pr`, `mr_bead`) and exits nonzero outside a git repository; never asks or halts -- that decision belongs to the calling agent.
> `mr_bead` is the id of an open `merge-request` bead (a pg-desk PR anchor) whose metadata names THIS repo (`metadata.repo` equal to the remote URL's `owner/name` or `name`) and the CURRENT branch (`metadata.branch`), and whose `metadata.state` is not merged/closed; it is `null` otherwise, so another repo's or branch's anchor in the shared tracker never changes the strategy. A bead without those keys is ignored. `bd` output is accepted both as a bare array and as a `{"data":[...]}` envelope.
> `--facts` emits a stable `KEY=value` block (`WT`, `FB`, `CC`, `PRIMARY`, `DIRTY`, `AHEAD`, `BEHIND`, `PRECOMMIT`, `CC_CORE_WORKTREE`) instead, for a caller that wants plain orientation facts without a `jq` dependency; `PRECOMMIT` is `bundle`, `stale`, `missing` or `broken`, taken from `pg-hooks status --porcelain`.
> `--prek-branch-diff` runs `pg-hooks run pre-land` over the whole branch diff (the `ff-merge-to-main` FF-1b step): exit 10 means a hook failed, exit 13 (no bundle) and a missing `pg-hooks` print a notice line and exit 0. With `git config pgii-integrate-branch.preLandReuse true` (opt-in; any other value is off) a passing run is remembered and reused after a rebase (one `FF-1b: reused ...` line) when the branch's changed files are byte-identical and main's new files lie outside the diff's Go modules and the projects they replace, are replaced by or name near a `..` path token; any doubt re-runs. `--no-reuse` disables it for one call.
> `--bundle-refresh <old-sha>` is the FF-4 step: after a landing it starts the canonical clone's hook-bundle reinstall in the background when the landed diff touched a stamp input; a `nix` reinstall command is resolved even when the caller's `PATH` lacks nix, and a nix found nowhere is reported loudly in the command's own output.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Report the current repo's integration facts and recommended strategy:

`integrate-branch-support`

- Extract just the recommended strategy (`null` when it cannot be inferred):

`integrate-branch-support | jq '.strategy'`

- See why a strategy was chosen (declared, inferred, ambiguous, or infeasible):

`integrate-branch-support | jq '.reason'`

- Report the current worktree/branch/canonical-clone/primary-branch orientation facts as a parseable `KEY=value` block:

`integrate-branch-support --facts`

- Run the hooks over every file the current branch changed relative to the primary branch (`pg-hooks run pre-land`; exit 10 when a hook fails, one notice line and exit 0 when there is no bundle or `pg-hooks` is not installed):

`integrate-branch-support --prek-branch-diff`

- After landing, refresh the canonical clone's hook bundle in the background if the landed diff touched a stamp input (run from the canonical clone):

`integrate-branch-support --bundle-refresh {{old_primary_sha}}`

- Declare a repo's strategy explicitly instead of relying on inference:

`git config pgii-integrate-branch.strategy {{ff-merge-to-main|pull-request}}`
