# pr-pool `jira-issues` query: working JQL source via `pg-pr-issues-jira-zr search`

**Status**: Accepted (adversarial review incorporated 2026-06-25)
**Date**: 2026-06-25
**Deciders**: Phillip Green II
**Bead**: `pg2-gpao` (blocks verification bead `pg2-5b4l`; follows `pg2-mf4c`)

> **Review note (2026-06-25).** An adversarial review found three blockers, now
> fixed in this spec: (1) `os.Args[1]` would panic with no args → guarded with a
> length check; (2) `/rest/api/3/search/jql` caps fielded pages at 100 (and may
> ignore `maxResults`), so a `len>=limit` truncation check never fires →
> truncation is now driven by the API's `nextPageToken`/`isLast`; (3) `pr-pool`'s
> Commander discards a child's stderr on success → truncation now travels on
> **stdout** as a `{items, truncated}` envelope (replacing the bare array).
> Also clarified: wrapper lands on the home-manager profile PATH (not
> `~/.local/bin`), exact `realBinary = "${self.packages…}"` wiring, the
> exec-recursion invariant, an explicit PATH-reachability check, and the
> exit-code contract (error → non-zero, never an empty envelope).

## Context

`pr-pool`'s `jira-issues` query type was implemented in `pg2-mf4c` against an
assumed `ankitpokhrel/jira-cli` contract:
`jira issue list --jql <jql> --paginate 0:200 --raw`. Post-apply verification
(`pg2-5b4l`, criterion d) showed this does not work on the host:

1. **No `jira` CLI is installed.** `pr-pool run-query` on a `jira-issues` role
   errors with `exec: "jira": executable file not found in $PATH`. The host's
   only Jira tooling is `pg-pr-issues-jira-zr` (a `pg-pr` provider that fetches
   a _single_ issue via `GET /rest/api/3/issue/<id>`) — not a JQL lister — and
   it is not deployed.
2. **The assumed endpoint is gone.** Atlassian removed the old
   `/rest/api/3/search` endpoint (deprecated May 2025, traffic blocked end of
   Oct 2025). JQL search must now use `/rest/api/3/search/jql`, which dropped
   `total` and `startAt`/offset paging in favour of opaque
   `nextPageToken`/`isLast`. So even with `jira-cli` installed, the
   `--paginate 0:200` contract no longer holds (`jira-cli` PR #892 / issue
   #898).

We evaluated `ankitpokhrel/jira-cli`, Atlassian's official `acli`, and a
direct-REST approach. Direct REST through the org's existing
`pg-pr-issues-jira-zr` binary won: it reuses the Atlassian basic-auth plumbing
that binary already has, needs no interactive bootstrap, is deterministic, is
immune to third-party CLI churn (we own the endpoint), and preserves
`pr-pool`'s "shell out to a host tool via the `Env.Cmd` Commander seam" symmetry
with `github-issues` → `gh`.

### Constraints / facts established during brainstorming

- The `pg-pr-issues-jira-zr` binary already has a `Provider` (env auth +
  `basicAuth()` + an HTTP client) and a single-issue `GetIssue`. Its `main()`
  only serves the `pg-pr` scriptout protocol, which is a Phase-0 stub, so the
  binary has never run live.
- Credentials needed (`JIRA_BASE_URL`, `JIRA_EMAIL`, `JIRA_API_TOKEN`) map to
  artifacts already on the host: tenant + email in `~/.config/.jira/.config.yml`
  (`https://ziprecruiter.atlassian.net`, `phillipg@ziprecruiter.com`,
  project `FINDEV`, CLOUD). The token file `~/.jira_api_token` is **expired**
  (live `GET /rest/api/3/myself` → HTTP 401); the operator will mint a fresh
  token into that file.
- The `zr.pgPrZr` home-manager module (the wrapper that injects `JIRA_*` and
  puts the binary on PATH at `~/.local/bin/pg-pr-issues-jira-zr`) exists but is
  **not imported** into the machine config, and no `tokenFile` is wired.

## Decision

Add a non-interactive JQL **search** mode to `pg-pr-issues-jira-zr` that calls
`/rest/api/3/search/jql` and emits a **normalized, tool-owned JSON** array;
deploy it on PATH via the existing credential wrapper; repoint `pr-pool`'s
`jira-issues` query at it and parse the normalized JSON.

The tool owns the Atlassian wire-format mapping. `pr-pool` consumes a stable
schema and is decoupled from Jira's REST shape — the next Atlassian change
touches only this tool, not the cross-repo boundary.

## Components

### A. `pg-pr-issues-jira-zr search` subcommand (`phillipg-nix-ziprecruiter`)

`modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go`:

- **CLI branch in `main()`**: `if len(os.Args) > 1 && os.Args[1] == "search"`
  → run the search path; otherwise fall through to the existing
  `scriptout.ServeIssues` path (which reads its op from **stdin**, takes no
  argv, and is still a Phase-0 stub). The length guard is mandatory — the
  current binary is invoked with no args, so a bare `os.Args[1]` would panic.
  Additive: existing (stub) behaviour is unchanged when `search` is absent.
- **Testable seam**: factor the search path as
  `runSearch(ctx, args []string, stdout io.Writer) error` so a unit test can
  call it directly without `exec`/`os.Args`. `main()` just dispatches to it.
- **Flags**: `--jql <string>` (required), `--limit <int>` (default 100; see
  the page-cap note below).
- **`Provider.SearchIssues(ctx, jql string, limit int) ([]SearchItem, bool, error)`**
  — the `bool` is `truncated` (more results exist than this page returned):
  - `POST <base>/rest/api/3/search/jql` with JSON body
    `{"jql": jql, "maxResults": limit, "fields": ["summary","status","labels","issuetype"]}`.
  - Reuses the existing `Provider` / `basicAuth()` — identical auth to
    `GetIssue`.
  - Parses the response `{ "issues": [ { "key", "fields": { "summary",
"status": {"name"}, "labels": [], "issuetype": {"name"} } } ],
"nextPageToken", "isLast" }` (no `total`).
  - **Single bounded page** — no `nextPageToken` looping (`pr-pool` only drains
    to a small role cap). `truncated = (nextPageToken != "" || isLast == false)`
    — this is the **authoritative** truncation signal from the API, NOT a count
    heuristic (see the cap note). If a future consumer needs the full set, add
    `nextPageToken` looping then.
  - **Page-cap note (verified):** when `fields` is anything beyond keys/ids,
    `/rest/api/3/search/jql` caps a page at **100 issues regardless of
    `maxResults`**, and there is an active regression where `maxResults` is
    sometimes ignored entirely. So a per-page count is NOT a reliable
    truncation signal — only `nextPageToken`/`isLast` is. `--limit` defaults to
    100 to match the real fielded-page cap; requesting more is futile for a
    single page.
- **Output (envelope, NOT a bare array):** the truncation flag MUST travel on
  **stdout**, because `pr-pool`'s Commander captures only stdout and discards a
  child's stderr on success. So emit an envelope to stdout, exit 0:
  ```json
  {
    "items": [
      {
        "key": "FINDEV-123",
        "summary": "…",
        "status": "Open",
        "issuetype": "Bug",
        "labels": ["x"],
        "url": "https://ziprecruiter.atlassian.net/browse/FINDEV-123"
      }
    ],
    "truncated": false
  }
  ```
  `url` is built as `<base>/browse/<key>` (same convention as `GetIssue`).
- **Exit-code contract:** any failure (missing creds, non-2xx, bad JQL,
  transport) → write the error to stderr and exit **non-zero**; NEVER print an
  envelope on error. Empty result → `{"items":[],"truncated":false}`, exit 0.
  `pr-pool` relies on this to distinguish empty-from-error.

### B. `pr-pool internal/query/issues.go` (`phillipgreenii-nix-agent-support`)

- **New argv** in `JiraIssues.Run`:
  `["pg-pr-issues-jira-zr", "search", "--jql", q.jql(), "--limit", strconv.Itoa(issueListLimit)]`.
  Drop `jira issue list … --paginate 0:200 --raw`. Set `issueListLimit` for the
  jira path to 100 to match the API's fielded-page cap (or thread a separate
  const; `github-issues` keeps 200).
- **`q.jql()` is unchanged** — `pr-pool` still owns JQL construction from
  `project` / `labels` / explicit `jql` config; the tool just executes a final
  JQL string.
- **Parse the envelope** `{items:[…], truncated:bool}` into a
  `jiraSearchEnvelope` struct. Map each item to `item.Item`: `ID=key`,
  `Type="jira-issue"`, `Title=summary`, `Metadata{project, key, issuetype,
status, labels, url}`. `project` comes from `pr-pool`'s own config (`q.Project`)
  — it is not in the tool output; the rest come from the envelope item.
- **Truncation warning is flag-driven, not count-driven.** Do NOT reuse
  `warnIfTruncated` (its `len>=limit` heuristic is wrong here — the API caps
  fielded pages at 100). Instead, when the envelope's `truncated` is true,
  `slog.Warn` that the jira backlog was truncated. (This diverges from
  `github-issues`, whose count-based warn is correct for `gh`.)
- Update the contract comment block, the package `README`, and the
  fake-`Commander` unit test (new argv + envelope JSON input). Keep
  `IsStub()` returning false.

### C. Deployment (`phillipg-nix-ziprecruiter`)

- Import the `zr.pgPrZr` module (`modules/pg-pr-zr/default.nix`) into the
  machine config and set:
  - `zr.pgPrZr.enable = true`
  - `jira.baseUrl = "https://ziprecruiter.atlassian.net"`
  - `jira.email = "phillipg@ziprecruiter.com"`
  - `jira.tokenFile = /Users/phillipg/.jira_api_token` (operator refreshes it)
  - `jira.realBinary = "${self.packages.${pkgs.system}.pg-pr-zr}/bin/pg-pr-issues-jira-zr"`.
    `self` is already in the machine config's `specialArgs`, and the package is
    exposed as `packages.pg-pr-zr` in `flake.nix`. (`realBinary` is a plain
    `types.str`; the module is NOT passed the package, so reference it via
    `self`.)
- **Wrapper placement (correction):** when `jira.tokenFile` is set, the module
  adds the `jiraWrapper` (`writeShellApplication` named `pg-pr-issues-jira-zr`)
  to `home.packages`, so it lands on the **home-manager profile PATH**
  (`/etc/profiles/per-user/phillipg/bin` / `~/.nix-profile/bin`) — NOT
  `~/.local/bin`. (The module's `MIGRATION.md` "path-shadow" diagram is stale on
  this point; optional cleanup.)
- **Exec-recursion invariant:** the wrapper and the real binary share the name
  `pg-pr-issues-jira-zr`. This is safe ONLY because the real binary is
  referenced by store path (`realBinary`) and never added to any profile PATH.
  Do NOT add `pgPrZr` to `home.packages` — that would shadow the wrapper and
  cause `exec` recursion.
- **PATH reachability (verify, don't assume):** `pr-pool`'s nix wrapper does
  `wrapProgram … --prefix PATH` (prepend, preserving inherited PATH), so it
  finds `pg-pr-issues-jira-zr` only if the home-manager profile bin is on
  `pr-pool`'s inherited PATH at exec time. This holds for interactive
  `pr-pool run-query` / `drain` from a home-manager shell (no pr-pool launchd
  daemon exists today). Add an explicit check to the plan
  (`command -v pg-pr-issues-jira-zr` from pr-pool's runtime context, or
  `pr-pool run-query` end-to-end). **Caveat:** if `pr-pool` ever moves under
  launchd/cron with a sanitized PATH, this breaks — revisit then.
- Then `zn-self-apply` (or `zn-self-build` in a sandbox + manual
  `darwin-rebuild switch`).

## Data flow

```
pr-pool drain/run-query (jira-issues role)
  → JiraIssues.Run builds JQL (q.jql()) and runs argv via Env.Cmd Commander
    → pg-pr-issues-jira-zr search --jql <jql> --limit 100   (wrapper injects JIRA_*)
      → POST /rest/api/3/search/jql  (Atlassian Cloud, basic auth)
      ← {issues:[…], nextPageToken, isLast}     (no total; page caps at 100 w/ fields)
    ← {items:[…], truncated:bool} on stdout      (exit 0; non-zero + stderr on error)
  ← []item.Item (ID=key, Type=jira-issue, …); slog.Warn if truncated
```

## Error handling

- Missing creds: `Provider.New()` already errors clearly when `JIRA_API_TOKEN`
  / `JIRA_EMAIL` are unset; the search path surfaces the same and exits
  non-zero.
- Non-2xx from Jira (401/403/400-bad-JQL): `SearchIssues` returns a wrapped
  error including status; the tool exits non-zero with the message on stderr;
  `pr-pool`'s Commander returns that as a query error (an auth/JQL error, never
  a stub, never an empty envelope).
- Empty result: tool emits `{"items":[],"truncated":false}`, exit 0; `pr-pool`
  returns no items, no error. (The exit-code contract is what lets `pr-pool`
  tell empty from error, since its Commander sees only stdout + exit status.)
- Truncation: authoritative `nextPageToken`/`isLast` → `truncated:true` in the
  stdout envelope → `pr-pool` `slog.Warn`s. No count heuristic, no reliance on
  stderr (which `pr-pool` discards). No silent caps.

## Testing

- **jira-zr** (`main_test.go`): `httptest` fake of `/rest/api/3/search/jql`
  asserting (a) the request method/path/body and `Authorization` header,
  (b) mapping to the envelope items, (c) `truncated=true` when the response has
  a `nextPageToken` / `isLast=false`, (d) `runSearch(ctx, args, &buf)` writes
  the expected envelope JSON (call the seam directly — do not exec `main()`).
  Keep existing `GetIssue` tests.
- **pr-pool** (`issues_test.go`): fake `Commander` returns an envelope JSON;
  assert the argv (`pg-pr-issues-jira-zr search --jql … --limit …`) and the
  `item.Item` mapping; assert the `truncated:true` path logs/flows correctly.
  Update/remove the obsolete not-implemented expectation. Keep
  `TestIsStub_noStubTypesRemain`.
- **Build**: no `go.mod` / `go.sum` / `gomod2nix.toml` change is needed —
  the search path adds only stdlib imports (`flag`, `bytes`, `io`), and
  `pg-pr-zr`'s single local-replace `go.mod` + empty `gomod2nix.toml` are
  unaffected (cross-repo Pattern B). Do NOT regenerate the toml.
- Both Go modules green under `-race`; `pre-commit` / `prek` passes in both
  repos.
- **End-to-end** (after the token is refreshed + module enabled):
  `pr-pool run-query <jira-role>` against a `FINDEV` query returns items, not
  `executable file not found` and not a stub.

## Scope / non-goals

- No multi-page pagination (single bounded page to a role cap). If a future
  consumer needs full pagination, add `nextPageToken` looping then.
- No change to `pr-pool`'s JQL construction or the `github-issues` path.
- The `pg-pr` scriptout `GetIssue` path and its Phase-0 stub are untouched.
- `acli` / `jira-cli` are not adopted.

## Alternatives considered

- **`ankitpokhrel/jira-cli`**: TUI-first; interactive `jira init` bootstrap
  (issue #366) is awkward on a headless nix host; its pagination semantics just
  changed under the endpoint deprecation. Rejected.
- **Official `acli`**: headless-capable (`acli jira auth login --token`,
  `--json`) but a closed-source binary with recent nixpkgs packaging breakage
  (#419539) and a stateful `auth login` session rather than a per-call env
  token — awkward for ephemeral agent shell-outs. Reasonable fallback, not
  chosen.
- **Raw JSON passthrough** (tool emits Jira's raw `/search/jql` body, pr-pool
  parses `{issues:[…]}`): less tool code, but re-couples `pr-pool` to the exact
  wire format that just broke. Rejected in favour of the normalized contract.

## Related decisions

- See also: `phillipg-nix-ziprecruiter` `modules/pg-pr-zr` (cross-repo
  gomod2nix Pattern B build, bead `pg2-wtjz`).
- `phillipgreenii-nix-agent-support` ADR 0008 (Go gomod2nix engine).
