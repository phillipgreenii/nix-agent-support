# hand-to-human

> Hand a bead to the operator: add the labels `human` and `attention`, remove `escalated`, all in ONE update call, then read it back.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Hand a bead the escalation path could not resolve to the operator:

`hand-to-human {{pg2-abc12}} --actor {{SESSION_ID}}`

- Also remove another marker label in the same call (for example a split-review marker):

`hand-to-human {{pg2-abc12}} --also-remove {{needs-split-review}}`

- Also add a marker label and append an explanatory note to the bead's notes in the same call (a watchdog hand-off):

`hand-to-human {{pg2-abc12}} --also-add {{stale-escalation}} --note "{{Automation timed out; check infrastructure first.}}"`

- Guard the hand-off against a concurrent claim (needs `--note`; exit 5 means the guard did not match and nothing was written):

`hand-to-human {{pg2-abc12}} --note "{{why}}" --if-assignee "" --if-status {{open}}`

- Run it against a specific tracker root instead of the current directory's:

`hand-to-human {{pg2-abc12}} --bd-dir {{/path/to/tracker/root}}`

- Reach the tracker through a pinned pg-connector backend (the escalation-triager):

`hand-to-human {{pg2-abc12}} --backend {{pg-connector-issue-beads-pg2}}`
