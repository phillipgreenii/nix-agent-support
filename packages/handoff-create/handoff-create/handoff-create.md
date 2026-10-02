# handoff-create

> Create a handoff bead correctly: type `handoff`, P0, `Handoff:` title, a `Handoff from session ID` first line, `handed_off_from_session` metadata, the `human` label iff attended, one task fallback, and a read-back.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Create a handoff while a human is in the session (labelled `human`, so no drain agent takes it):

`handoff-create --attended --session-id {{SESSION_ID}} --title "{{finish the retry work}}" --body-file {{/tmp/body.md}}`

- Create a handoff with no human present (no `human` label, so a drain agent may pick it up), with a provenance label:

`handoff-create --unattended --session-id {{SESSION_ID}} --title "{{finish the retry work}}" --body-file {{/tmp/body.md}} --label {{auto-session-wrapped}}`

- Create it in a specific tracker instead of the current directory's:

`handoff-create --unattended --session-id {{SESSION_ID}} --title "{{subject}}" --body-file {{/tmp/body.md}} --bd-dir {{/path/to/tracker/root}}`

- Capture the new bead id (the verification report goes to stderr):

`id=$(handoff-create --attended --session-id {{SESSION_ID}} --title "{{subject}}" --body-file {{/tmp/body.md}})`
