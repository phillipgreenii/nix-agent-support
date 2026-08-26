# Worktree-review label: provably-lossless-close exceptions

Two narrow exceptions to the ordinary W-7/W-8 rules, both from the same operator ruling
(2026-08-24, bead `pg2-9aewz`, implementing `pg2-q62e8`). Moved here from the main skill body
to keep it within budget (tc-ql0o Stage C, C.2); `SKILL.md`'s W-7 and W-8 entries point here.

## W-7 exception — provably-lossless close, no exchange held

When the terminal action is `/unblock-human-beads`' class 1a — a mechanically PROVEN-lossless
teardown-and-close, with no operator exchange opened at all — the agent MUST NOT open one
solely to ask the W-7 priority question. It MUST still record the gap (`NO promotion record;
priority left at P<n> — unverified. No operator exchange on this path.`) and proceed to close;
the bead is being CLOSEd, so its priority routes nothing, and W-7's ordinary ASK applies
unchanged to every other path (1b, or any exchange already open).

## W-8 exception — provably-lossless close satisfies "with the operator" without an exchange

A CLOSE whose losslessness is mechanically PROVEN in the closing session — a clean worktree,
and every commit on the branch either an ancestor of the primary or patch-identical to one
already on it, corroborated by `git range-diff` — and recorded verbatim on the bead SATISFIES
W-8's in-session-CLOSE requirement without a live exchange: the recorded proof, plus the
operator's standing ruling that a provably-landed teardown needs no approval (originating on
`pg2-kl0o4`, "if the bead is complete and provably been landed, then you do not need to ask me.
just clean up"), together stand in for the exchange W-8 otherwise requires.

This exception is narrow and MUST NOT be read as licence to skip recording the proof, and it
MUST NOT be extended to a RELEASE under any circumstances: the never-release-to-drain half of
W-8's guard stays UNCONDITIONAL regardless of how strong the proof is, because drain runs
unattended and a losslessness proof MUST NOT be inherited from an earlier session (see this
skill's Premise Freshness section) — it must be re-established by whoever acts. If ANY
substrate work remains that the proof does not cover, this exception does not apply and W-8's
ordinary rule (in-session CLOSE with the operator, or DEFER) governs.
