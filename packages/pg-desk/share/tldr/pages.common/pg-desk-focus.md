# pg-desk focus

> Plan and track the daily focus: the entities you chose to work on today, kept in the pg-desk store.
> Reads and writes only the store; nothing here calls the tracker.
> More information: `pg-desk focus --help`, and `pg-desk focus <verb> --help` for a verb's reply grammar, exit codes and flags.

- Morning, step 1: show today's draft plan (the ranked candidates with live facts and the coverage header):

`pg-desk focus show`

- Step 2: preview a reply; `ok` accepts, `-{{key}}` strikes, `+{{key}}` adds, `cap={{N}}` re-caps. Nothing is written:

`echo "{{ok -1234 +ZZ-9 cap=4}}" | pg-desk focus select --dry-run`

- Step 3: lock the plan with the same reply (exit 2 means an annotation write failed: run `pg-desk focus select --repair`):

`echo "{{ok}}" | pg-desk focus select --apply`

- During the day: open a draft again over the locked plan, with new or re-ranked candidates below it:

`pg-desk focus replan`

- Add the two best-ranked candidates not yet selected, without replacing the plan:

`pg-desk focus pull --top 2`

- Add named candidates (a PR number, `OWNER/REPO#N` or a tracker key):

`pg-desk focus pull {{1234}} {{ZZ-9}}`

- End of day: close the period with a summary (a closed day cannot be amended; exit 6):

`echo '{"summary": "{{what got done}}"}' | pg-desk focus close`

- Ask why an entity is, or is not, a candidate and where it sits:

`pg-desk focus explain {{key}}`

- Mark a bead as plannable (the only bead marker; needs `bead_id_pattern` in the config):

`bd update {{id}} --add-label pg-focus-planable`

- Address another day, or print JSON (`pg-desk.focus/v1`), with any verb:

`pg-desk focus show --date {{2026-10-09}} --json`
