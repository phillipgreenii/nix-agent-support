# pgd

A session display mod: a band, an alert row and modals drawn from cached sources. Component
options live in the Home Manager module `phillipgreenii.programs.pgd`; the mod internals are in
`hooks/README.md`.

## The pgd noun (contributions)

Another bundle pushes data to the display through the `pgd` noun. Every method takes one object and
returns a Promise, and only data crosses the boundary: there is no callback argument and no action
field (contributed buttons are out of scope). A contributor pushes when its state changes; the
display never calls back into it.

| Method       | Argument                                                | Effect                                           |
| ------------ | ------------------------------------------------------- | ------------------------------------------------ |
| `pgd.put`    | `{ id, surface, priority, text, color?, ttlMs?, url? }` | Adds or replaces the contribution with that `id` |
| `pgd.remove` | `{ id }`                                                | Removes it; an unknown `id` is not an error      |

- `surface` is `alert` (plain text that joins the alert row, ranked by `priority`, larger first) or
  `focus` (an item held for the Focus pane; only a focus item may carry `url`).
- `ttlMs`: the contribution disappears that long after its last `put`, and nothing else changes. A
  contributor that stops refreshing therefore disappears. Without `ttlMs` it never expires.
- `color` is stored; the alert row is plain text and does not use it.
- A bad argument rejects with an `Error` that names the field.
- With `components.focus` off, a `focus` put resolves and stores nothing. The noun itself is
  registered whenever `enable` is true.

The types contract is [`types/pgd.d.ts`](types/pgd.d.ts) and the one documented example is
[`types/example.ts`](types/example.ts); the flake check `test-pgd-types` compiles the example
against the contract with `tsc --noEmit`.

## Rate-limit capture (always on with the mod)

Every `session.measure` that changes the five-hour or seven-day reading appends one record to
`<claude dir>/projects/<slug>/<session-id>.status.jsonl` (`hooks/limits-capture.ts`; the claude dir
is `CLAUDE_CONFIG_DIR`, else `~/.claude`). The retired shell status line was this file's only
writer. Its readers are unchanged: pa-monitor's account-global limit reading (and its corpus
monitor), the recorder's `claude-status` source, `pg-ccaudit` ingest and transcript discovery, which
skips these files as transcripts. The line carries `ts`, `session_id` and whichever of
`five_hour_pct`, `five_hour_resets_at`, `seven_day_pct`, `seven_day_resets_at` are valid (a
percentage outside 0 to 100 and an unparseable reset are left out, never written as 0). Records are
written on change only, and the session's text is kept in memory and rewritten whole because
`$.fs.write` replaces a file. A write the host refuses is dropped silently; a session whose file
never appears shows as a stale limit reading in pa-monitor.

## Modals and toggles

The session id label and the context label in the band are buttons. Each opens its own modal pane
(the Info modal and the Context and usage modal), drawn from the same cached sources as the band,
so a label and its modal never disagree. A modal never uses `holdToasts`. The band, Info and
Context are governed by the top-level `enable` option only; they have no component flag.

- Info rows, one per line: name, session id, actor, version, repo, worktree, branch, PR. A row
  whose value is missing is omitted (no branch and worktree rows when the `.git` reads fail, no PR
  row without a PR value).
- Context and usage rows: context percent and tokens, last compaction, the five-hour and seven-day
  limits each with its reset time, and cost. Each row appears only when its value exists, and a
  reset time only when the limit carries one.

### Choices the design left to the builder

Each of these is covered by a test in `hooks/modals.test.ts`, `hooks/modal.test.ts` or
`hooks/toggles.test.ts`.

| Choice                          | Decision                                                                                                               |
| ------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| What `pressButton` does         | It toggles: the first press opens the modal, a second press on the open modal closes it.                               |
| Close action                    | Always added with id `close`; the framework closes the modal itself and `onAction` never receives it.                  |
| Info name row without a name    | Shows the session id (the session id also has its own row).                                                            |
| Band collapse button            | No plugin button: the engine draws the only `[-]`; `/pgd-band` hides or shows the band.                                |
| Toggle ids                      | `band`, `info`, `context`. The band is open and each modal closed by default.                                          |
| Slash commands                  | `/pgd-band`, `/pgd-info`, `/pgd-context`, no arguments; each toggles and answers `<id> shown.` or `<id> hidden.`.      |
| Host-closed pane (Esc, dismiss) | A `ui.close` with origin `person` for a `pgd-` pane resyncs the toggle state to closed.                                |
| Colours                         | Literal `yellow`, `red`, `green`; context yellow from `contextWarn`, red from `contextAlert`; limits red from `limit`. |
| Actor                           | The session id cut to 8 characters and an ellipsis.                                                                    |
| Value formats                   | Tokens with thousands separators, cost `$1.50`, time `YYYY-MM-DD HH:MM UTC`, reset time as the engine gives it.        |
| `lastCompaction` source         | Event-driven, defined by the modal component, set from the single `session.compact` hook in `gitHead.ts`; it redraws.  |
| Open modal on invalidate        | Rebuilt on each draw from the live builder, so values stay current.                                                    |
| Toggle persistence              | An engine atom (`pgd.toggles`), hydrated at `session.start`; it survives a hot reload.                                 |
| Pane ids                        | `pgd-<modal id>`.                                                                                                      |

## Session name (`components.sessionName`)

The first band button shows the session name in place of the 8-character id prefix when a name
exists, and the id prefix otherwise. The name is read from the session's own entry in Claude Code's
session registry (`~/.claude/sessions/<pid>.json`) through the facade's `fs.list` and `fs.read`: the
first fetch lists the directory and reads each `.json` entry until one has a `sessionId` equal to the
session id, caches that path, and later fetches re-read only that path. The source is polled at most
every 10 seconds while the band or Info modal is shown and is re-read after `turn.complete` (throttled
to the same 10 seconds). The name is the entry's `name`, whatever its `nameSource` (`user`, `auto`,
`derived` or `collision`); the id prefix is shown only until a name can be read, and the Info modal
keeps the full session id as its own row. With the component off nothing is registered: no registry
file is read and the id is shown. The transcript is never read and no process runs.

### Choices the design left to the builder

| Choice                           | Decision                                                                                                                                   |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Malformed or partly written file | The value is undefined (the id is shown) and the cached path is kept, so the next read recovers when the write finishes.                   |
| Failed read of the cached path   | The path is dropped and the directory is listed again in the same fetch; the same happens when the path now holds another session's entry. |
| Missing directory or entry       | The value is undefined (the id is shown); it is not counted as a failure, so each poll lists the directory again until the entry appears.  |
| Sample registry fixtures         | `hooks/fixtures/registry.ts`: user-named, derived, other-session, renamed and malformed entries, as exported strings.                      |

## PR status (`components.prStatus`)

The Info modal's PR row comes from the `pr` source: the PR number and review state of the current
branch, from `gh pr view <branch> -R <owner>/<repo> --json number,reviewDecision`, run through the
facade's `process.run`. With the repo and the branch both passed, gh runs no git and cannot contend
for a git lock (a bare `gh pr view` needs git); the display itself never runs a git command. `gh` is
expected on `PATH`, installed by the existing module that provides it. With `components.prStatus`
off there is no gh source, no timer and no PR row.

- Interval and sharing: at most one fetch every five minutes (and only while the Info modal is
  shown), shared across sessions through the store under `shared:pr:<owner/name>#<branch>`, so a
  session that finds the entry fresh does not fetch and two branches of one repo never read each
  other's entry.

### Choices the design left to the builder

Each of these is covered by a test in `hooks/pr.test.ts` or `hooks/pr.realgh.test.ts`.

| Choice                       | Decision                                                                                                                                                                                                                                    |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Branch change                | The cached value is cleared at once and one forced refresh bypasses the throttle and the backoff, once per new key.                                                                                                                         |
| Change while the modal shut  | The request is remembered and the fetch runs as soon as the modal is next shown.                                                                                                                                                            |
| Branch or repo absent        | No key, so no fetch, no shared entry and no PR row (detached head, no `.git`, or `session.repo()` giving no `owner/name`).                                                                                                                  |
| Repo                         | `session.repo()` is read once at start and memoized; its `name` when it is `owner/repo`, else parsed from the remote URL.                                                                                                                   |
| Empty `reviewDecision`       | The value is kept with an empty state; the row shows the number alone, uncoloured.                                                                                                                                                          |
| Colours                      | `APPROVED` green, `CHANGES_REQUESTED` red, `REVIEW_REQUIRED` yellow; any other state uncoloured.                                                                                                                                            |
| A failed gh call             | Non-zero exit, rejection, timeout, gh absent, unparseable output or no PR for the branch: value undefined, row omitted, backoff.                                                                                                            |
| Backoff curve                | The source layer's: the interval doubled per failure, capped at ten minutes.                                                                                                                                                                |
| Second session in tests      | A second boot on the same stub store, reading the entry the first session wrote.                                                                                                                                                            |
| What "nothing else" compares | The cached `gitHead` and `model` values before and after the failure.                                                                                                                                                                       |
| `claude plugin test` and gh  | `pr.realgh.test.ts` imports node modules, which the engine's loader refuses (as for the multi-process suite); `test-pgd-mod` runs it under node with `pkgs.gh` on its inputs, and a gh missing from `PATH` is itself a tested failure path. |

## Session mode (`components.sessionMode`)

The mode button, the Session mode modal and the "wrapping up" alert come from the `sessionMode`
source: `session-mode show`, run through the facade's `process.run` with `CLAUDE_CODE_SESSION_ID`
(from `session.id()`) in the run's env. `session-mode` is expected on `PATH` from the existing
session-mode module; the tool is used as it is. With `components.sessionMode` off there is no
button, modal, stop action, source, timer or alert.

- The button (`mode`, band order 40) is hidden only while no record exists for this session (operator,
  2026-11-11: "if never set, it can remain hidden, if ever set, it stays until the session clears").
  A record in any state, finished included, keeps it; the session clears when the session id changes
  (after `/clear`) or the record is removed (`show` exits 2 and the cached value is cleared). The label
  is `<mode>: running|stopping|finished` plus ` [detail]`; `wrap-up-session` reads `wrapping up` while
  active and `wrapped up` once finished. Colours (the old status line): mode magenta, wrapping up and
  stopping yellow, wrapped up green, other finished dim. A press opens the modal; a second press closes it.
- The modal lists `mode`, `state` and `detail` (omitted when empty), one per line, with the actions
  Stop (runs `session-mode set-status stopping`, then reads the source again; not offered once the
  mode is finished) and Close.
- The alert `wrapping up` (priority 100, the highest) shows while a wrap-up mode is active.

### Choices the design left to the builder

Each of these is covered by a test in `hooks/session-mode.test.ts` or `hooks/session-mode.realtool.test.ts`.

| Choice                | Decision                                                                                                                                                                                                                   |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Refresh               | Event-driven, no timer: read at start, on `turn.complete` (through the existing `turn.complete` hook in `gitHead.ts`) and after a stop action.                                                                             |
| Interval              | At most one read per 10 seconds on `turn.complete`; a read that begins less than 10 seconds after the previous one is skipped.                                                                                             |
| Refresh after a stop  | The stop action bypasses the throttle once, so the label and alert follow the stop at once.                                                                                                                                |
| Parsing               | `session-mode show` prints one JSON record; `kind` becomes `mode`, `state` and `detail` are kept. A record without `kind` or `state` is a parse failure.                                                                   |
| No record             | Exit 2 (also as a rejection carrying exit code 2) is the value undefined, not a failure: no button, no backoff.                                                                                                            |
| Failed read           | Any other non-zero exit, rejection, timeout, absent tool or unparseable output leaves the value undefined, so the button is not drawn.                                                                                     |
| Active                | A record whose state is not `finished`; only an active mode offers Stop or raises the alert.                                                                                                                               |
| Wrapping up           | The mode `wrap-up-session` while active.                                                                                                                                                                                   |
| Labels and action ids | Button label as above; band row id and modal id `mode`; actions `stop` and `close`.                                                                                                                                        |
| Throwaway record      | The real-tool test sets `SESSION_MODE_STATE_DIR` to a temporary directory and creates the record with the real `session-mode start`.                                                                                       |
| Nix check             | `test-pgd-mod` gains `pkgs.session-mode` on its inputs; `session-mode.realtool.test.ts` imports node modules, so `claude plugin test` reports it as "did not load" (as for `pr.realgh.test.ts`) and the nix check runs it. |

## Model and effort (`components.modelEffort`)

The `Model · effort` label on the band is a button (`model`) that opens the Model and effort modal.
With `components.modelEffort` off there is no button (the label is not drawn at all, as before), no
modal, no model change and no prompt fill.

- The modal lists the model choices and the effort choices, one per line, the current one of each
  marked. Apply switches the model directly through `config.set({ key: "model", value })`. An effort
  choice pre-fills `/effort <level>` in the prompt box for the operator to send; it never submits.

### Choices the design left to the builder

Each of these is covered by a test in `hooks/model-modal.test.ts`.

| Choice                | Decision                                                                                                                                                 |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Model choices         | `sonnet`, `opus`, `haiku`, in that order.                                                                                                                |
| Effort choices        | `low`, `medium`, `high`, `xhigh`, `max`, in that order.                                                                                                  |
| Items                 | One per choice, labelled `model` or `effort`, value the choice; the current one carries `current: true`.                                                 |
| Unknown current model | A current model that is not a choice is appended as one more `model` item marked current.                                                                |
| Blank effort          | Before the first turn the effort group is one item labelled `effort` with an empty value and no current marker; the choices appear after the first turn. |
| Action ids            | `apply-model:<id>` (label `Apply <id>`) and `effort:<level>` (label `Effort <level>`), then the framework's Close.                                       |
| Apply value           | The model id string, passed to `host.config.set({ key: "model", value })`.                                                                               |
| Model refresh         | After `config.set` the model source is read again with a forced `refresh('model')` (the `config.set` hook also updates it).                              |
| Effort choice         | `host.prompt.fill({ text: "/effort <level>", mode: "replace" })`; nothing is submitted.                                                                  |
| Closing               | Both Apply and an effort choice close the modal.                                                                                                         |
| Facade                | `host.config.set` takes the engine's `{ key, value }` object; `host.prompt.fill` is one added closure over `$.prompt.fill`.                              |

## Assume (`components.assume`)

The Assume button (band order 50) shows the AWS profile the session's own processes run with: the
`AWS_PROFILE` value in the Claude process environment, read once at session start through the
facade's `env.awsProfile()`. Pressing it opens the Assume modal.

- Label: `Assume` when the variable is unset or empty, otherwise the profile name.
- Colour, matching the name ignoring case: unset or empty is dim; a name containing `prod` is red;
  otherwise a name containing `dev` is green; any other value is yellow. `prod` is checked first,
  so a name containing both is red.
- Source: `assume`, value `{ role?, expiresAt? }` (epoch milliseconds), polled once a minute
  (`intervalMs` 60000, feeds the band and the alert); Apply and Clear update it directly.
- Modal `assume`: the current role and its expiry when known, one `role:<name>` row per profile (alphabetical, ignoring case), and
  the actions `apply`, `clear` and `close`. Choosing a role only marks it; Apply uses the marked
  role (else the current role when the config lists it).
- Alert `assume expired` (priority 50): true when a stored expiry has passed. A missing expiry never
  raises it.

### Choices the design left to the builder

- Role list: the profile names in the AWS config (the file named by `AWS_CONFIG_FILE`, else
  `~/.aws/config`), read once through `host.fs.read`. `[default]` and `[profile NAME]` sections
  count; other sections (for example `sso-session`) do not. A missing file leaves the list empty.
- Apply: sets `AWS_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION` and `GRANTED_COMMAND` with
  `$.env.set` through four facade closures (each a fixed string literal), and does not run
  `assume`. The profile name is the value of `AWS_PROFILE` and `GRANTED_COMMAND`; the region
  variables are set only when the profile has a `region` key. A denied set leaves the stored role as
  it was. The operator's verification found the AWS CLI authenticates with the profile and the two
  region variables alone, and that a model-run Bash call sees values set this way.
- Clear: sets the four variables to empty strings (the host has no unset) and clears the stored role
  and expiry.
- Expiry source: the `Expiration` field of `aws configure export-credentials --profile NAME --format
process`, run once after each Apply. Only that field is read; the credentials are never stored or
  logged. The call runs with a five-minute timeout, because a lapsed SSO login can open a keychain
  prompt and a browser window, which does not fit the 30-second default. A failed or timed out
  lookup leaves no expiry (button and modal work, no alert). A profile that was already set when the
  session started has no expiry either; applying it again records one.
- Check: the one-minute poll returns the stored value and asks for one redraw when the expiry has
  just passed, so the alert row appears without another event.

Known limit: a profile exported inside a `!` command or a Bash call changes only that child shell,
so the button does not show it.

## Wrap up (always drawn)

The Wrap up button is the last of the component buttons (order 70, after Focus; only the band toggle follows). It has no component flag: it is
drawn whenever `enable` is true. Pressing it opens no modal; it pre-fills the prompt box with the
wrap-up skill's slash command and never submits (a submit from a command does not start a turn
until the next user input).

### Choices the design left to the builder

- Glyph: no glyph; the label is the plain text `Wrap up`.
- Command text: `/session-wrapup:wrap-up-session`, the plugin-qualified invocation of the
  `wrap-up-session` skill in the `session-wrapup` plugin.

## Transcript links (`components.links`)

A render hook on assistant-message text (`ui.render`, component `AssistantMessage`) rewrites
matches of the configured link rules into markdown links. A rewrite changes only what is drawn
(the hook hands the engine a copy of the draw event; nothing stored changes). Rules come from the
`links` option list and apply in list order. The shipped list is empty: no rule is on until it is
configured.

### Rule format

A rule is `{ kind, name, options }`. An entry with no `kind` is a `regex` rule, so an entry
written before kinds existed (`name`, `pattern`, `url`) keeps working unchanged. There are four
kinds.

| Kind        | Finds                                                    | Options                                        |
| ----------- | -------------------------------------------------------- | ---------------------------------------------- |
| `regex`     | any match of `pattern`                                   | none; takes `pattern` and `url` on the rule    |
| `github-pr` | pull request references                                  | `sessionRepoFallback` (boolean, default false) |
| `jira`      | issue keys of the listed projects                        | `baseUrl` (required), `keys` (required)        |
| `file-ref`  | `path:line`, `path:line:col` and `path:line-line2` paths | `localTemplate` (default below)                |

Top-level `github` options (not per rule) feed `github-pr` and `file-ref`:

| Option         | Default      | Meaning                                                           |
| -------------- | ------------ | ----------------------------------------------------------------- |
| `github.host`  | `github.com` | Host the github-pr and remote file-ref links point at.            |
| `github.repos` | `[]`         | Known repositories, each `owner/repo`; any other form is skipped. |

In the Home Manager module a rule has `name`, `kind` (null means regex), `pattern` and `url` (regex
rules) and `settings`, a free attribute set rendered as the rule's `options` (left out when empty).

#### regex

JavaScript `RegExp` with flag `g` only (no `u`, `i` or `m`); a match of zero length is skipped. The
`url` is a template where `{0}` is the whole match, percent-encoded (`encodeURIComponent`). A
pattern that is not a valid regular expression, or a url with no `{0}` or that is not a valid URL
once `{0}` is filled, skips that rule only.

#### github-pr

Scans `owner/repo#N`, `PR N` and `PR #N`, `pull request N` and `pull request #N`, and `pull #N`.
`PR` MUST be uppercase: a lowercase `pr 5 times` is prose and is not linked. The keyword forms
`pull request` and `pull` are case-insensitive. A bare `#N` is never linked (see "Calls"). The link
text is the whole token (`PR 12`, not only `12`). The URL is `https://<host>/<owner>/<repo>/pull/N`.

The repository is settled by the first hit of these:

1. The explicit `owner/repo` of the token. Its owner MUST be the owner of a known repository (the
   session repository or an entry of `github.repos`), else the token declines `unknown-repo`.
2. The nearest earlier `owner/repo` written in full in the same paragraph (a blank line ends a
   paragraph) that is a known repository. A bare repository name never counts, and a mention an
   earlier rule already rewrote is not visible.
3. With `sessionRepoFallback` true, the session repository.
4. Otherwise the token declines `no-repo`.

Owner and repository comparisons ignore case; the link keeps the case the text used.

#### jira

The scanner is built from `keys`: `\b(?:KEY1|KEY2)-[0-9]+\b`, case sensitive, keys escaped, so
`UTF-8` or `SHA-256` never match unless listed. The link is `<baseUrl>/<KEY-N>` (trailing slashes
of `baseUrl` are dropped). `baseUrl` MUST be an `http` or `https` URL and each key MUST match
`^[A-Z][A-Z0-9_]*$`. An empty `keys` list makes the rule inactive: it is skipped with a message
and links nothing.

#### file-ref

Scans `path:line`, `path:line:col` and `path:line-line2` (a range links its first line). A path
with a `/` is a path; a path without one needs a short alphabetic extension that is not a
top-level domain such as `com` or `io`, so `example.com:443` is not linked.

- Local form (the default): a relative path is made absolute against the session working
  directory, `~/` against the home directory; an absolute path is used as is. There is no
  existence check, and `.` and `..` segments are resolved textually.
- Remote form: when the text before the first colon is a known `owner/repo`
  (`acme/widgets:src/a.go:12`), the link is
  `https://<host>/<owner>/<repo>/blob/HEAD/<path>#L<line>`. An `owner/repo` that is not known
  declines `unknown-repo`.
- `localTemplate` renders the local form. `{path}` (absolute, leading slash included, every
  segment percent-encoded), `{line}` and `{col}` (1 when the token has none) are filled. The
  default is `file://{path}:{line}`. The template MUST contain `{path}` and its scheme MUST be one
  of `file`, `vscode`, `vscode-insiders`, `cursor` or `zed`; a rule whose template breaks either
  is skipped.

### Declines

A scanned token that cannot be linked safely stays plain text and is counted with a reason.

| Reason         | When                                                                                                                                                                 |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `no-context`   | The snapshot lacks what the token needs: no working directory for a relative path, no home for `~/`, no snapshot at all for github-pr.                               |
| `no-repo`      | A github-pr token with no repository by the order above.                                                                                                             |
| `unknown-repo` | An explicit `owner/repo` that is not known.                                                                                                                          |
| `adjacent`     | The token sits in a path: preceded by `/` or `\`, followed by `.` and a word character, or followed by `:` and a digit.                                              |
| `unsafe`       | A github-pr owner, repository or host that is not plain; a `file:` link to a launchable type (`app`, `command`, `pkg`, `dmg`, `sh`, `workflow`, `terminal`, `tool`). |

For `jira`, a `/` that directly follows a complete listed key-number token does not make the next
key adjacent, so `ABC-1/ABC-2` links both keys while `a/ABC-12` stays plain.

### Link context

The kinds read a snapshot, never a host call, while drawing: the working directory, the session
repository, the home directory, the github host and the known repositories and their owners. It is
seeded at session start from `github` and refreshed, past the usual throttle, at session start and
at the end of every turn (`turn.complete`). A refresh that fails keeps the last value of that field;
a repository read that answers "no repository" clears it. The drawing is invalidated only when a
refreshed value differs.

### Protected spans, memo and streaming

- Protected spans: fenced code blocks (CommonMark: a fence of three or more backticks or tildes,
  indented at most three spaces, closed by a fence of the same character and at least the same
  length, or by the end of the text), inline code spans (a backtick run closed by a run of equal
  length), existing markdown links, `<https://...>` autolinks, bare `http(s)` URLs, and any span an
  earlier rule rewrote. Each is replaced by one private-use character (U+E000 to U+F8FF) during the
  pass, so a later rule cannot match inside it; text that already holds such a character is left
  unrewritten.
- Escaping: the link text escapes `\`, `[`, `]`, backtick, `_`, `*` and `<`; the URL takes the
  match percent-encoded.
- Memo: the last 64 distinct messages are remembered per rule set, so a redraw of an unchanged
  message costs a lookup. A message holding a `no-context` decline is not remembered, so it links
  once the snapshot fills in. A link that was resolved is final: the memo ignores later snapshot
  changes.
- Streaming: a message that grows while it streams is a new text on every chunk, so it is a miss
  and is rewritten again each time.
- Invalid rule: a rule that fails validation is skipped alone, with a message naming it; the other
  rules still apply. Unknown option fields are ignored.

### Counters and debug log

`pgd_links_resolved_total{kind,rule}` and `pgd_links_declined_total{kind,rule,reason}` (`rule` is
the rule name, so the label set is bounded by configuration). They count resolutions on memo
misses, an approximation and not a count of unique links: a streaming message is a miss on every
chunk, an evicted message is counted again when it is redrawn, and a `no-context` decline later
followed by a link counts once in each. The first decline per (kind, reason) in a session writes
one `debug` line carrying the kind and the reason and no message text; there is no per-link line.

### Recommended order

A token can fit two kinds (`src/ABC-12.go:5` holds a key and a path); the rule earlier in the list
claims it, and the adjacency declines protect against a wrong order. Put `file-ref` first, then
`github-pr`, then `jira`. List order is the only ordering rule.

### Worked example

```nix
phillipgreenii.programs.pgd = {
  github.host = "github.com";
  github.repos = [
    "acme/widgets"
    "acme/gadgets"
  ];
  links = [
    {
      name = "files";
      kind = "file-ref";
    }
    {
      name = "prs";
      kind = "github-pr";
      settings.sessionRepoFallback = true;
    }
    {
      name = "tickets";
      kind = "jira";
      settings = {
        baseUrl = "https://jira.example.com/browse";
        keys = [
          "ABC"
          "OPS"
        ];
      };
    }
  ];
};
```

With the session in `/work/widgets` (repository `acme/widgets`):

| Text                                   | Drawn as a link to                                           |
| -------------------------------------- | ------------------------------------------------------------ |
| `src/main.go:42`                       | `file:///work/widgets/src/main.go:42`                        |
| `acme/gadgets:lib/x.go:7`              | `https://github.com/acme/gadgets/blob/HEAD/lib/x.go#L7`      |
| `acme/gadgets#9`                       | `https://github.com/acme/gadgets/pull/9`                     |
| `PR 12` (no repository in the message) | `https://github.com/acme/widgets/pull/12` (session fallback) |
| `ABC-34`                               | `https://jira.example.com/browse/ABC-34`                     |
| `#12`, `pr 12`, `UTF-8`                | not linked                                                   |

### Calls

These were decisions of the design, open to correction.

- Bare `#N` is never linked: it appears in headings, step numbers and issue lists too often.
- `sessionRepoFallback` defaults to off: a wrong repository is the worst outcome. With it on and no
  session repository the reference stays plain (`no-repo`).
- Remote file links use `HEAD` (GitHub resolves it to the default branch), not the local commit,
  because the local commit may be unpushed.
- A local `path:line` is linked with no existence check, and a token without enough context to
  say it is remote is treated as local; a relative or `~/` path with no context declines rather
  than guess a base.
- `file://{path}:{line}` is the first local form, an experiment: the template is data, so the
  default can change without code.
- The `file` scheme declines launchable file types, and the template scheme is allowlisted.
- The keyword forms link the whole token, not just the number.
- Existing links, autolinks and bare URLs are protected from rewriting.
- One top-level `github` object, not per-rule repository lists.
- The shipped list is empty.
- Registration: the engine takes hooks at module load, before the options are known, so the render
  hook is registered at load and gated on every draw: with `components.links` (or `enable`) false it
  hands the event on untouched.

## Focus pane (`components.focus`)

The Focus button (band order 60, action `focus`) toggles a pane listing the contributions pushed to
the focus surface, one row per item: `kind  id  title  [ open ]`. Rows are read from the contribution
store (`listContributions('focus')`), ordered by priority (larger first) and then by time added
(earlier first). The pane never appears in the band. With `components.focus` off there is no
button, pane, `/pgd-focus` command or contribution storage (a focus `put` stores nothing).

### Choices the design left to the builder

Each is covered by a test in `hooks/focus-pane.test.ts`.

| Choice               | Decision                                                                                                                                                                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Band button label    | The plain text `Focus`, no glyph; always drawn while the component is on.                                                                                                 |
| Pane id and label    | Toggle id `focus`, host pane `pgd-focus`, title `Focus`; a separate pane from every modal, so it can be open beside one. Slash command `/pgd-focus` toggles it.           |
| Host pane API        | The modal pane framework (`openModal`, `registerModalBuilder`): one item per row (label is the kind), rebuilt on every draw; an item may carry a per-row `action` button. |
| Reading the rows     | `focusRows()` (rows) and `focusSpec()` (the pane spec) in `hooks/focus-pane.ts`.                                                                                          |
| Kind and id of a row | The contribution id `kind:rest` gives kind `kind` and id `rest` (split at the first colon, both parts non-empty); any other id gives kind `item` and the whole id.        |
| Title of a row       | The first non-empty line of the contribution text, trimmed; a text with none falls back to the row id.                                                                    |
| Row layout           | Kind padded to the widest kind, two spaces, `id  title`, then a `[ open ]` button only when the item has a url.                                                           |
| Open action id       | `open:<contribution id>`. It runs from the button press, never from a draw, and re-reads the item so a removed item opens nothing.                                        |
| What open does       | Runs `open <url>` (macOS), then `xdg-open <url>` if that fails, through `host.process.run`; only `http` and `https` urls are opened.                                      |
| Empty state          | One row with an empty kind and the text `Nothing tracked for this session.`                                                                                               |
