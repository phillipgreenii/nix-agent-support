Propose a change only if the problem recurred in at least 3 sessions on at least 2 different days. Otherwise report it as a one-off note, not a proposal.

LENS: agent-workflow. How the assistant's tools and workflow behave in practice.

Kinds (the closed list; use exactly one per finding):

- misclassified-error: a tool result reported as an error that was really expected behaviour, or the reverse.
- repeat-refusal: the same action refused by a hook or deny rule again and again.
- tool-ux: a tool whose interface makes the assistant stumble in a repeatable way.
- wasted-retries: a chain of retries that burned time or tokens before reaching a result.
- cost-hotspot: one call shape or signature that dominates token or time cost.

Calibration examples (synthetic; they show the bar, not real data):

- Qualifies (repeat-refusal): the signature "tool-alpha: remove generated files" was refused by a deny rule in 6 sessions over 4 days, 22 refusals in total, each followed by about 3 retried variants. Proposal: route the safe variant to an allow rule and delete the stale deny pattern, confirm_by: the refusal count for that signature in the next window is 0.
- Does not qualify: a one-off error in a single session on one day. Report it as a one-off note.
- Does not qualify: "tool-beta is slow" with no count or minutes. No number means a note.
