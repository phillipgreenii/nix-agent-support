# drain-stuck reference: STUCK steps 4 and 5 (NAME THE PRECONDITION, DETECT A REPEAT)

Read in full when an ESCALATE exit reaches STUCK step 4 or 5 (before the step 6 comment). Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

4. NAME THE PRECONDITION — only when the escalation is blocked on something that must
   become TRUE before the bead is workable (skip it for an underspecified /
   decision-needed escalation). What you write here becomes an INSTRUCTION to a later
   agent and keeps being obeyed long after the implementation it describes has been
   refactored, so it MUST be drift-detectable:
   - `PRECONDITION:` MUST state an OBSERVABLE OUTCOME — something a reader can run
     and see ("`nb` run from a subdirectory opens the Gradle ROOT project"). It MUST
     NOT state a MECHANISM ("`nb` is a function defined in `~/.zshrc`"): the next
     refactor makes a mechanism claim permanently false, and every later reader then
     concludes "not applied yet" and re-escalates forever.
   - `PRECONDITION-KEY:` MUST be a short kebab slug naming that OUTCOME, not this
     attempt — `nb-opens-gradle-root`, never `nb-check-2` — so a later escalation blocked
     on the SAME thing produces the SAME key. Step 5 counts these.
   - `DERIVED-FROM:` MUST cite the commit and the file(s) you actually read to write
     the line (`<repo>@<sha> — <path>`), so a later reader can re-derive it and see
     the drift.
   - The failure branch MUST be bounded. MUST NOT write "if not yet applied, re-escalate
     or wait" with no limit — step 5 IS the limit.

5. DETECT A REPEAT — before commenting, check whether this bead was already escalated
   on the same precondition:

   ```bash
   bd show <id> --json --include-comments | jq -r '(if type=="object" and has("data") then .data else . end)[0] | (.comments // [])[].text' | rg -o 'PRECONDITION-KEY: .*'
   ```

   (`--include-comments` is load-bearing: plain `bd show --json` omits the comments and
   reports only `comment_count`, so the probe would never find a prior key. The prelude is the
   **J-1** unwrap, valid under the `{"data":[…]}` envelope and for the bare array alike.
   `comments` is `null` on a bead with none, hence the `// []`; empty output — `rg` exit 1 —
   means no prior key, NOT a failure.)
   - The key you are about to write is ABSENT → ordinary escalation (step 6a).
   - The key is ALREADY PRESENT — this would be the SECOND escalation on the same unmet
     precondition → the precondition itself is the suspect, not the world: escalate
     it as stale (step 6b + step 7b). There is NO third escalation on one key.
   - The bead ALREADY carries the `stale-precondition` label → an earlier staleness
     escalation was released without resolving it. Write NO precondition block at
     all: comment plainly what you observed, escalate again (step 7a), and leave
     `stale-precondition` in place so it stays visible as unresolved.
