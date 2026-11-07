# wtaudit

> Deterministic classifier and remover for worktrees and branches (the mechanical half of the audit-worktrees sweep). `scan` is read-only and prints one verdict per candidate; `remove` re-verifies one candidate and deletes it, the branch by compare-and-swap, never `git branch -D`.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Classify every candidate in every workspace repo as JSON (`schema_version` 1; `scan` needs `pn` and `bd` on PATH):

`wtaudit scan > {{scan.json}}`

- JSON is the default output; `--json` states it explicitly, and `wtaudit --help` lists the verdict enum and every exit code:

`wtaudit scan --json`

- Classify specific repos only, as a human table (every row is shown, nothing is hidden):

`wtaudit scan --repo {{/path/to/repo}} --table`

- Remove a candidate whose row says `remove` or `reclaim` (pass the row's `repo_path`, `branch` and `tip`); stdout is one JSON object and the exit code says what happened:

`wtaudit remove --repo {{/path/to/repo}} --branch {{drain/pg2-abcde}} --expect-tip {{40-hex-sha}}`

- Reclaim the worktree of an `agent` row a bead has ruled abandoned, keeping its unlanded branch (same guards and exit codes; exit 0 means the worktree is gone and the branch intact):

`wtaudit remove --repo {{/path/to/repo}} --branch {{drain/pg2-abcde}} --expect-tip {{40-hex-sha}} --keep-branch`

- Exit codes of `remove`: 0 gone; 10 refused before acting (verdict changed or tip moved); 11 worktree gone but the branch remains; 12 set member (use `pnwf cleanup`); 13 `wtdone` refused and the worktree is still there; 2 usage.

- Checkout-pool slots are never candidates: a worktree locked with a reason starting "pool slot" (what `pg-checkout-pool` sets), or sitting at `<root>/pg-checkout-pool/slots/<id>/tree`, is neither removed nor reported, and the lease branch it has checked out is not a dangling branch. Any other `git worktree lock`ed worktree is still reported as `keep worktree-unreadable`.

- Agent instructions, in order:
  1. Run `wtaudit scan > <fresh scratch file>` (JSON is the default). Read it; never re-derive its facts by hand.
  2. `action: keep`: report the row verbatim (`reason`, `evidence`). MUST NOT remove, relabel or act on it, and MUST NOT second-guess it with `git` or `wtdone`.
  3. `action: remove` or `reclaim`: dispatch one action subagent per repo (serialized within a repo) running `wtaudit remove --repo <repo_path> --branch <branch> --expect-tip <tip>` and returning its JSON. Exit 0: removed. Exit 11: the worktree is gone and the branch remains: report it. Exit 10, 12 or 13: report the `reason` and move on, do NOT retry. After a `reclaim` with `removed_worktree` true, the orchestrator writes the clean-up note on the bead (`bd comment --file`).
  4. `action: agent`: do the research and classification (abandoned, vouched, no usable bead, ambiguous; `bd search` is allowed here) from the row's bead and commit facts; bead writes and landing stay with the agent. For a standalone row (not a set member) whose bead rules it abandoned, run `wtaudit remove ... --keep-branch`: it removes the worktree and keeps the branch. A `bead-match-weak` row means the bead id was only a suffix guess: corroborate it first.
  5. Sets: use `sets[]`. `remove-via-pnwf` means run `pnwf cleanup <set>`. A set whose blocking member reason is `idle-clean-unworked` or `isolated-not-worked` is only reported as "isolated, not yet worked", never researched, landed or reclaimed. Any other set is reported or researched like a standalone row.
  6. A tool error (scan exit 2 or 3, remove exit 2) or output failing `jq -e '.schema_version == 1'` means stop and report; never fall back to manual removal.
