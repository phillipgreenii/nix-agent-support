# Agent-Posted PR Comments Carry a Visible Bot Attribution

**Status**: Accepted
**Date**: 2026-07-08
**Deciders**: Phillip Green II

## Context

pg-pr posts PR comments and reviews under the **user's own GitHub account** (the
resolved `GH_TOKEN`), not a dedicated bot account — so GitHub renders no "bot"
badge and a reader cannot tell an agent-authored comment from one the account
owner typed by hand.

Comments were originally stamped with a visible `🤖` glyph (`marker.Markerify`).
The feedback-datastore work (2026-06-23) introduced an **invisible** HTML marker
(`<!-- pg-pr -->`) so ingestion could distinguish agent- from human-authored
content without relying on the login, and then deleted `Markerify` as dead code
(`bb633da9`). Net effect: agent comments carried only the invisible marker, so
they were visually indistinguishable from the user's own comments — the concern
raised in bead `pg2-r1gm`.

The invisible marker is the right primitive for **machine** detection (a human
is unlikely to type `<!-- pg-pr -->`, and it does not clutter the rendered
comment), but it does nothing for **human** readers.

## Decision

Every comment or review body pg-pr/an agent posts MUST carry **both**:

1. The invisible `HTMLMarker` (`<!-- pg-pr -->`), for machine detection
   (`IsOurs`, dedup, ingestion classification).
2. A human-visible attribution banner (`VisibleAttribution`) making clear the
   comment was posted by automation and not written directly by the account
   owner.

Both are applied by the single chokepoint `marker.Stamp`, through which all
posting paths (`cmd/pg-pr` review post/submit, `reviewsink`, `replyposter`)
already funnel. `Stamp` MUST remain idempotent (keyed on the invisible marker)
so re-posts do not stack banners.

Content-based dedup and fingerprint keys MUST compare the **marker-stripped**
body (`marker.Strip`), not the raw body, so that neither the constant banner nor
the old→new marker-format transition affects identity. In particular
`reviewstage.dedupKey` — which keys on a fixed 100-char body window — MUST strip
first, or the banner would dominate the window and collide distinct findings.

## Consequences

### Positive

- Readers can tell at a glance that a comment is agent-authored, even though it
  is posted under the user's login.
- Detection, dedup, and ingestion continue to key off the robust invisible
  marker, unaffected by banner wording changes.
- The convention is now recorded, so a future cleanup will not silently drop the
  visible attribution again (the original regression's root cause).

### Negative

- The banner adds a few lines to every agent-posted body.
- Two marker representations (visible + invisible) must be kept in sync in
  `Stamp`/`Strip`.

### Neutral

- The legacy `Glyph` is retained: it still leads the visible banner and is still
  recognised by `IsOurs` for the invisible-marker transition window.

## Alternatives Considered

### Post from a dedicated bot GitHub account

Would give a native "bot" badge and need no in-body banner, but pg-pr is designed
to act as the user (own token, own PRs); provisioning and threading a separate
bot identity is out of scope and would change the ownership model.

### Visible glyph only (revert to `Markerify`)

Rejected: a bare glyph is easy for a human to type by accident (weak machine
signal) and is less explicit than a worded banner. Keeping the invisible marker
for detection and adding a worded visible banner serves both audiences.

## Related Decisions

- Supersedes the marker portion of the feedback-datastore plan
  (`docs/superpowers/plans/2026-06-23-pg-pr-feedback-datastore.md`), which
  established the invisible-only marker.
- See also: 0007-pg-pr-go-cli-consolidation.md
