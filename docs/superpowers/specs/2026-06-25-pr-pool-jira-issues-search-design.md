# pr-pool `jira-issues` query: working JQL source via `pg-pr-issues-jira-zr search`

**Status**: Draft
**Date**: 2026-06-25
**Deciders**: Phillip Green II
**Bead**: `pg2-gpao` (blocks verification bead `pg2-5b4l`; follows `pg2-mf4c`)

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

- **CLI branch in `main()`**: if `os.Args[1] == "search"`, parse a flagset and
  run the search path; otherwise fall through to the existing
  `scriptout.ServeIssues` path (untouched, still a stub). Additive — zero risk
  to existing behaviour.
- **Flags**: `--jql <string>` (required), `--limit <int>` (default 200).
- **`Provider.SearchIssues(ctx, jql string, limit int) ([]SearchItem, bool, error)`**
  (the `bool` reports truncation, i.e. more pages exist):
  - `POST <base>/rest/api/3/search/jql` with JSON body
    `{"jql": jql, "maxResults": limit, "fields": ["summary","status","labels","issuetype"]}`.
  - Reuses the existing `Provider` / `basicAuth()` — identical auth to
    `GetIssue`.
  - Parses the response `{ "issues": [ { "key", "fields": { "summary",
"status": {"name"}, "labels": [], "issuetype": {"name"} } } ],
"nextPageToken", "isLast" }` (no `total`).
  - **Single bounded page** — no `nextPageToken` looping. `pr-pool` only drains
    to a role cap, so one page of `--limit` suffices (mirrors `github-issues`
    `--limit` + truncation-warn). If `isLast` is false, return `truncated=true`
    and log a warning to stderr.
- **Output**: a normalized JSON array to stdout, exit 0:
  ```json
  [
    {
      "key": "FINDEV-123",
      "summary": "…",
      "status": "Open",
      "issuetype": "Bug",
      "labels": ["x"],
      "url": "https://ziprecruiter.atlassian.net/browse/FINDEV-123"
    }
  ]
  ```
  `url` is built as `<base>/browse/<key>` (same convention as `GetIssue`).

### B. `pr-pool internal/query/issues.go` (`phillipgreenii-nix-agent-support`)

- **New argv** in `JiraIssues.Run`:
  `["pg-pr-issues-jira-zr", "search", "--jql", q.jql(), "--limit", strconv.Itoa(issueListLimit)]`.
  Drop `jira issue list … --paginate 0:200 --raw`.
- **`q.jql()` is unchanged** — `pr-pool` still owns JQL construction from
  `project` / `labels` / explicit `jql` config; the tool just executes a final
  JQL string.
- **Replace `jiraSearchResult`** with a `jiraSearchItem` struct matching the
  normalized array. Map to `item.Item`: `ID=key`, `Type="jira-issue"`,
  `Title=summary`, `Metadata{project, key, issuetype, status, labels, url}`.
- **Reuse `warnIfTruncated`** on `len(items) >= issueListLimit` (symmetric with
  `github-issues`).
- Update the contract comment block, the package `README`, and the
  fake-`Commander` unit test (new argv + normalized JSON input). Keep
  `IsStub()` returning false.

### C. Deployment (`phillipg-nix-ziprecruiter`)

- Import the `zr.pgPrZr` module (`modules/pg-pr-zr/default.nix`) into the
  machine config and set:
  - `zr.pgPrZr.enable = true`
  - `jira.baseUrl = "https://ziprecruiter.atlassian.net"`
  - `jira.email = "phillipg@ziprecruiter.com"`
  - `jira.tokenFile = /Users/phillipg/.jira_api_token` (operator refreshes it)
  - `jira.realBinary` → the nix-built `pg-pr-issues-jira-zr` (follow the
    module's existing convention; wire to the flake package rather than a
    hand-installed path where practical).
- Ensure the wrapper (`~/.local/bin/pg-pr-issues-jira-zr`) is on the PATH that
  `pr-pool` runs with, then `zn-self-apply`.

## Data flow

```
pr-pool drain/run-query (jira-issues role)
  → JiraIssues.Run builds JQL (q.jql()) and runs argv via Env.Cmd Commander
    → pg-pr-issues-jira-zr search --jql <jql> --limit 200   (wrapper injects JIRA_*)
      → POST /rest/api/3/search/jql  (Atlassian Cloud, basic auth)
      ← {issues:[…], nextPageToken, isLast}
    ← normalized JSON array on stdout
  ← []item.Item  (ID=key, Type=jira-issue, …); warnIfTruncated on full page
```

## Error handling

- Missing creds: `Provider.New()` already errors clearly when `JIRA_API_TOKEN`
  / `JIRA_EMAIL` are unset; the search path surfaces the same.
- Non-2xx from Jira (401/403/400-bad-JQL): `SearchIssues` returns a wrapped
  error including status; `pr-pool` surfaces it as a query error (an auth/JQL
  error, never a stub).
- Empty result: tool emits `[]`; `pr-pool` returns no items, no error.
- Truncation: `isLast=false` → stderr warning in the tool + `pr-pool`'s
  `warnIfTruncated` on a full page. No silent caps.

## Testing

- **jira-zr** (`main_test.go`): `httptest` fake of `/rest/api/3/search/jql`
  asserting (a) the request method/path/body and `Authorization` header,
  (b) mapping to the normalized items, (c) truncation when `isLast=false`,
  (d) the `search` subcommand prints the expected JSON. Keep existing
  `GetIssue` tests.
- **pr-pool** (`issues_test.go`): fake `Commander` returns a normalized JSON
  array; assert the argv and the `item.Item` mapping. Update/remove the
  obsolete not-implemented expectation. Keep `TestIsStub_noStubTypesRemain`.
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
