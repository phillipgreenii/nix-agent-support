# integrate-branch-support

> Advisory detector for the `integrate-branch` skill: gathers a repo's branch-integration facts and, when it can, a recommended strategy.
> Emits one JSON object on stdout (`strategy`, `reason`, `primary_branch`, `canonical`, `remote`, `open_pr`, `mr_bead`) and exits nonzero outside a git repository; never asks or halts -- that decision belongs to the calling agent.
> `--facts` emits a stable `KEY=value` block (`WT`, `FB`, `CC`, `PRIMARY`, `DIRTY`, `AHEAD`, `BEHIND`, `PRECOMMIT`, `CC_CORE_WORKTREE`) instead, for a caller that wants plain orientation facts without a `jq` dependency.
> `--prek-branch-diff` runs `prek` over the whole branch diff (the `ff-merge-to-main` FF-1b step); with no usable `.pre-commit-config.yaml` it prints one notice line and runs nothing.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Report the current repo's integration facts and recommended strategy:

`integrate-branch-support`

- Extract just the recommended strategy (`null` when it cannot be inferred):

`integrate-branch-support | jq '.strategy'`

- See why a strategy was chosen (declared, inferred, ambiguous, or infeasible):

`integrate-branch-support | jq '.reason'`

- Report the current worktree/branch/canonical-clone/primary-branch orientation facts as a parseable `KEY=value` block:

`integrate-branch-support --facts`

- Run `prek` over every file the current branch changed relative to the primary branch (one notice line and exit 0 when the worktree has no usable prek config):

`integrate-branch-support --prek-branch-diff`

- Declare a repo's strategy explicitly instead of relying on inference:

`git config pgii-integrate-branch.strategy {{ff-merge-to-main|pull-request}}`
