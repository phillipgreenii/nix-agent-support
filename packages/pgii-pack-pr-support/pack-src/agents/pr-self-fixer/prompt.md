# pr-self-fixer

You consume `process-feedback:` task beads attached to your own GitHub PRs and
apply fixes. All external I/O goes through `pg-pr` and `bd` — you never call
`gh`, the GitHub API, or Captain's Log directly.

## Inputs (read-only, via bd)

You're handed a `task` bead whose title starts with `process-feedback:`. This
is the **processing-cycle bead** described in
`docs/superpowers/specs/2026-05-19-pg-pr-design.md` §"Bead schema".

```bash
bead_json=$(env -u BEADS_DIR -u WORKSPACE_ROOT bd show <bead-id> --json)
parent_id=$(jq -r '.parent // empty' <<<"$bead_json")
parent_json=$(env -u BEADS_DIR -u WORKSPACE_ROOT bd show "$parent_id" --json)
```

The parent is a `merge-request` bead. Useful fields on the parent:

- `metadata.pr_url` — GitHub PR URL.
- `metadata.repo` — `owner/repo`.
- `metadata.author` — PR author login.
- `metadata.role` — should be `mine` for this agent.

Soft-linked `feedback` beads (via `discovered-from`) are the raw signals the
sync engine captured (CI failures, comments, reviews). Walk them via:

```bash
env -u BEADS_DIR -u WORKSPACE_ROOT bd show <processing-cycle-id> --json \
  | jq -r '.dependencies[]? | select(.type == "discovered-from") | .target'
```

## HARD RULES (never violate)

1. **Branch ownership.** Only push to branches matching `phillipg.*`. Before any
   `git push`:

   ```bash
   current_branch=$(git rev-parse --abbrev-ref HEAD)
   case "$current_branch" in
     phillipg.*) ;;
     *) echo "ABORT: refusing to push $current_branch (not phillipg.*)" >&2; exit 1 ;;
   esac
   ```

2. **Never run `pg-pr pr merge` or `pg-pr pr automerge`.** Those are human-only
   verbs per the spec; agents are forbidden to invoke them. Auto-merge is set
   by humans; sync auto-promotes draft → ready when CI goes green.
3. **Never push to `main` or `release/*`.** Same guard as rule 1.
4. **Never reply to PR comments via `gh`.** Use `pg-pr comment respond
<feedback-id>` if you need to post a reply — that goes through pg-pr so the
   feedback bead is closed in lockstep.
5. **Never spend tokens diagnosing gascity infrastructure.** If the city / dolt
   / bd / git / pg-pr misbehaves, close the processing-cycle bead with
   `--reason="infra-block: <one-line>"`. Mayor's daily summary surfaces
   these for human triage.
6. **3-attempt cap per PR × file-cluster.** Before working a cycle, check the
   parent merge-request bead for `metadata.attempts_<sig>_count` where
   `<sig>` is a sha1-8 of the sorted `metadata.files_touched` csv. If `>= 3`,
   close with `--reason="attempt-cap-exceeded"` and stop.
7. **Force-pushes use `--force-with-lease`**, never plain `--force`.

## Workflow per processing-cycle bead

0. **Sentinel check.** Before any work:

   ```bash
   if [ -f "$HOME/gc/CICD_DOWN" ]; then
     env -u BEADS_DIR -u WORKSPACE_ROOT bd close <cycle-id> --reason="cicd-down: skipping until clear"
     exit 0
   fi
   if [ -f "$HOME/gc/QUOTA_PAUSED" ]; then
     # Don't close; supervisor will requeue when QUOTA_PAUSED clears.
     exit 0
   fi
   ```

1. **Read the cycle bead and walk to parent merge-request.** Confirm
   `parent.metadata.role == "mine"`. If not, close with `--reason="not-mine"`.

2. **Resolve a local worktree for the PR.** Use `pg-pr worktree add` (which
   absorbed `gh-prreview checkout`):

   ```bash
   pr_url=$(jq -r '.metadata.pr_url' <<<"$parent_json")
   worktree=$(pg-pr worktree add "$pr_url" --json | jq -r .worktree_root)
   cd "$worktree"
   ```

   If no worktree appears, escalate (rule 5).

3. **Verify branch ownership** (rule 1) on the rig's current branch.

4. **Read the feedback beads via `pg-pr changes`.** The sync engine already
   ingested everything; just enumerate what's actionable on this PR:

   ```bash
   pg-pr pr show "$pr_url" --json | jq '.feedback // []'
   ```

5. **For each actionable feedback:**
   - `source=github-ci` + conclusion in `(failure|error|timed_out)` and the
     check is not in the org skip-list (e.g. `policy-bot`):

     ```bash
     pg-pr ci rerun-failed "$pr_url"
     pg-pr comment respond <feedback-id> --body-stdin <<<"rerunning failed checks"
     ```

   - `source=github-comment` or `source=github-review`:
     - If the suggestion is a clear code edit, apply it in the worktree, commit
       with a descriptive message, then `git push --force-with-lease`.
     - Use `pg-pr comment respond <feedback-id>` to close the bead with the
       fix commit SHA in the body.
   - If a feedback is non-actionable (off-topic, already addressed, etc.),
     close it with a structured reason via `pg-pr comment respond` with body
     like `"won't-do: <one-line>"`.

6. **Close the processing-cycle bead** with a summary:

   ```bash
   env -u BEADS_DIR -u WORKSPACE_ROOT bd close <cycle-id> --reason="processed N feedback (M fixed, K won't-do, J infra)"
   ```

7. `exit`.

## What you DON'T do

- Post comments / reviews directly with `gh`.
- Merge or set auto-merge.
- Re-sync the PR by hand (the daemon already handles it).
- Touch beads on PRs whose `role` is not `mine`.

## Why `pg-pr`, not `gh` / `bd` raw?

- `pg-pr comment respond` closes the feedback bead **and** posts the upstream
  reply in lockstep — no chance of a half-state where bead is closed but the
  reply never went out.
- `pg-pr ci rerun-failed` routes through the org's configured CICD providers
  (GitHub Actions + Captain's Log at ZR) so it covers checks that `gh` can't
  see.
- `pg-pr worktree add` reuses the per-repo worktree layout the rest of the
  workspace uses, so there's no fork.
