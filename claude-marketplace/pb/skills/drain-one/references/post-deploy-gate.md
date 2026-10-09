# drain-one reference: POST-DEPLOY VERIFICATION GATE

Read in full when FINISH (step 7) routes a `done-pending-apply-verification` bead here. Moved verbatim from /drain-beads.

## POST-DEPLOY VERIFICATION GATE (use INSTEAD of `human` for deploy-only tails)

When a bead is implemented, its pre-apply gates PASS, and it has LANDED, but
the only thing left is confirming it works on the LIVE machine (subagent status
`done-pending-apply-verification`), DO NOT label it `human`. Attach a
`pn:applied` gate to a fresh verification child bead — ONE call, which runs the
whole deferred-first sequence (create the child DEFERRED → prove it is absent
from `bd ready` → attach every gate → un-defer → re-prove absence → comment the
link on the impl bead):

```bash
pb gate attach-verified-child \
  --impl <impl-id> \
  --title "verify <thing> works after apply (<impl-id>): <concrete checks>" \
  --gate <repo-key>=<landed-sha> \
  --actor "ID"
# one --gate per changed repo; the child unblocks only when ALL are applied
```

Pin `<landed-sha>` to the sha the lander reported AND you verified in LAND's
check — never HEAD, never a re-read of the shared primary branch (a peer may
have advanced either). Branch on the exit code:

- `0` → fully gated; the output names the child. CLEANUP per FINISH, then close
  the impl bead naming the child.
- `0` with a `comment failed` warning on stderr (JSON: `"comment_failed": true`)
  → gating is complete and safe, but the provenance link was not recorded:
  record it yourself —
  `bd comment <impl-id> "post-deploy verification gated as <child> (pn:applied)." --actor "ID"`
  — before closing.
- `3` → gating INCOMPLETE and the child was left DEFERRED (safe — no peer can
  claim it). Do NOT close the impl bead; route it to STUCK naming the child.
- `4` → the child could NOT be proven un-workable. Do NOT close the impl bead;
  route it to STUCK and say so in the park comment — a peer could otherwise
  claim the child and "verify" unapplied code.
- `1` with `is not in workspace` in the error → an INVOCATION mistake, not a
  transient: a mistyped `--gate` repo key (fix it and re-run — nothing was
  created), or a repo genuinely outside the workspace (take the FALLBACK below
  instead).
- any other non-zero → transient-vs-genuine per the Rules; retry once, then
  STUCK.

The gate resolves via `pn workspace apply`'s post-hook (`pb gate check`); a
gate left unapplied past its stale window auto-converts to a `human` bead. Gate
semantics, stale handling, and the squash-merge prohibition:
the `pb:pb-gate-lifecycle` skill.

**SCOPE — this gate path applies ONLY when the changed repo is a `pn workspace`
MEMBER, its resolved strategy is `ff-merge-to-main`, AND the changed FILES are
actually applied by the terminal host's own `pn workspace apply`
(nixos-rebuild).** The repo/strategy conditions are NECESSARY but NOT
SUFFICIENT. `pb gate create` cannot resolve `--repo` outside the workspace, and
a squash-merged PR rewrites the patch-id so a gate could never auto-resolve
(provenance: the `pb:pb-gate-lifecycle` skill) — but even inside a qualifying
repo/strategy, `pb gate check` resolves a `pn:applied` gate from the REPO's
applied git history alone (patch-id presence in whatever the terminal built): it
has no notion of which files within that repo a given apply actually applies.
A same-repo change whose real deployment mechanism is something else — a k8s
cluster deploy via `just deploy <cluster>` (kubectl/kustomize against a REMOTE
cluster), a `just deploy-remote <ip>` to a non-terminal machine, or any other
out-of-band mechanism — can make the gate resolve on some unrelated LATER
`pn workspace apply`, proving nothing about whether that real deployment step
ever ran. Before attaching the gate, ASK: does `pn workspace apply` on the
terminal host actually cause THIS SPECIFIC change to take effect, or does it
require a separate `just deploy` / `just deploy-remote` step? If the latter,
take the FALLBACK below even though the repo/strategy conditions are met.
(Discovered live: `tc-satmb` and `tc-vpaki`, two homelab k8s-manifest-only
drain beads whose `pn:applied` gates had to be manually caught and converted to
`human` follow-ups after this was noticed.)

**FALLBACK when the gate path does NOT apply** (repo outside a pn-workspace, or
resolved strategy `pull-request`): file the verification child as a `human`
bead instead — CORRECT under **D-1**, because a PERSON's out-of-band action
(merging the draft PR, then deploying) stands between the code and the live
machine:

```bash
bd create "verify <thing> works once <pr-url> is merged and deployed (<impl-id>): <concrete checks>" \
  --labels human --deps "discovered-from:<impl-id>" --actor "ID" --json
# capture the id as <child>. No --defer and NO gate: nothing here would resolve one.
```

Then CLEANUP per FINISH (for `pull-request`, KEEP the isolation) and close the
impl bead naming `<child>` and the PR. This outcome MUST still TERMINATE: never
attempt `pb gate attach-verified-child` here, never route to STUCK for it.
