# REMOVE_CORP_REFERENCE.md

## What this file is

This repository contains references to a current/former employer (ZipRecruiter, abbreviated
`ZR`) — its private GitHub org, its internal tooling, its Jira tenant, its filesystem layout,
and in one case a verbatim capture of an internal pull-request review. This file tracks every
such reference so they can be removed as a unit.

**This file is itself temporary.** It is committed so the work is visible and resumable across
sessions, and the LAST item on the checklist below is to delete it. It MUST NOT outlive the
cleanup.

**Scope:** this repository only. Sibling trackers exist in `nix-personal`, `nix-repo-base`,
`nix-overlay`, and `bb`. `homelab` and `ha-addon-esphome-mcp` are clean and have no tracker.

**Severity:** this is the worst-affected repository of the five. It is the only one containing
verbatim internal employer data (§A) rather than merely names and paths.

## Provenance

Findings assembled 2026-08-12/13 from three independent read-only sweeps: current content at
every ref tip (incl. untracked and gitignored files), full reachable commit history
(`git log --all` pickaxe + diff-regex), and every object in the store regardless of
reachability (`git cat-file --batch-all-objects`), plus reflogs, notes, and `rr-cache`.

Counts below are occurrences in the working tree, verified 2026-08-13. Every count is
re-derivable — the command is given with each section. Re-run them before claiming an item
done; do not trust the number as recorded.

```bash
# whole-repo totals
rg -i -o 'ziprecruiter' . | wc -l     # expect 469 -> must reach 0
rg -i -l 'ziprecruiter' . | wc -l     # expect 80 files -> must reach 0
rg -i -o 'starterview'  . | wc -l     # expect 15 -> must reach 0
```

---

## Findings

### A. Verbatim internal PR review — HIGHEST SEVERITY

`packages/pg-pr/pkg/provider/vcs/github/testdata/enriched-prs-single.json`

A raw GitHub GraphQL response captured and committed as a test fixture (commit `bb1c1495`,
2026-05-28 — the only commit that ever touched the file). Live on `main` and `origin/main`.
71 `ziprecruiter` + 4 `starterview` occurrences. It contains:

| Item                        | Value                                                                                     |
| --------------------------- | ----------------------------------------------------------------------------------------- |
| Internal schema namespace   | `sqitch/starterview/...` — the ONLY `starterview` source in the corpus                    |
| Internal DB object          | `usage_pricing_token_dry_run`, columns `run_id` / `org_id` / `billing_date`, plus indexes |
| Internal enum values        | `EXPIRE_TOKEN`, `CLOSE_QUIZ_TOKEN`, `ISSUE_PPP`, `SKIP_ORG`                               |
| Internal repo layout        | `db/skeema/ec2-reporting/`, `sqitch/`, `etc/loaded-schema/`                               |
| Private org/repo/PR         | `ZR-Private/ziprecruiter` PR `#91071` (also `#93270` elsewhere)                           |
| Jira key                    | `FINDEV-9345`                                                                             |
| **A colleague's real name** | branch `constantin.segarceanu.FINDEV-9345.add-dry-run-table`                              |
| Commit SHAs                 | internal monorepo revisions                                                               |
| Undecoded blob              | a large base64 CodeRabbit "internal state" payload — contents unreviewed                  |

This is third-party personal data plus non-public schema. It MUST be removed from history, not
merely from `main`, and the base64 blob SHOULD be decoded and reviewed before disposal so the
removal scope is known rather than assumed.

- [ ] **A1** — Replace this fixture with synthetic data that exercises the same parser paths.
- [ ] **A2** — Decode and review the embedded base64 CodeRabbit state blob; record what it held.
- [ ] **A3** — Purge the file from all history (see §H — history rewrite).

### B. Internal toolchain / infrastructure inventory — HIGH SEVERITY

`packages/claude-extended-tool-approver/internal/rules/configrules/testdata/zr-rules.json`
(the filename is itself a reference). Live on `main`, added `808c54b6` 2026-07-25.

Discloses the employer's internal command surface and cluster naming:

- Build/deploy commands: `grazr`, `gozr`, `pyzr`, `shzr`, `stevedore`, `epoxy`,
  `validate_format`, `check-airflow-dags.sh`
- Log verbs: `wslogs`, `zrlog`, `wsfirstpod`
- Kubernetes dev-cluster prefixes: `d1-`, `dd1-`
- Production account names: `prod`, `dprod`, `euprod`, `build`, `fastlane`, `pdx`, `test`
- Scripts: `zr-proto-regenerate.sh`, `pre-merge-protobuf-check`, `fix-ai-tools-ownership`

- [ ] **B1** — Replace with a generic fixture; move the real ruleset to the private ZR repo.
- [ ] **B2** — Rename the file off `zr-rules.json`.
- [ ] **B3** — Purge from history (§H).

### C. Private GitHub org, repo, and work account

```bash
rg -i -n -e 'ZR-Private' -e 'phillipgziprecruiter' .   # 74 + 8 occurrences
```

| Identifier                | Count | Representative locations                                                                                                                                                                                              |
| ------------------------- | ----: | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ZR-Private/ziprecruiter` |    74 | `packages/pg-pr/pkg/provider/vcs/github/enrich_test.go:16,:35`; the §A fixture                                                                                                                                        |
| `phillipgziprecruiter`    |     8 | `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md:187` (`export SELF_LOGIN=`); `.../2026-06-09-pr-pool-work-triaging.md:572`; `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md:46,:162` |

- [ ] **C1** — Remove the private org/repo name from source and tests (parameterise it).
- [ ] **C2** — Remove the work GitHub login from docs and example commands.

### D. Corporate email, Atlassian tenant, and credential retrieval recipe

```bash
rg -i -n -e 'phillipg@ziprecruiter\.com' -e 'ziprecruiter\.atlassian\.net' .   # 4 + 5
```

- `docs/superpowers/specs/2026-06-25-pr-pool-jira-issues-search-design.md:56,:167`
- `docs/superpowers/plans/2026-06-25-pr-pool-jira-issues-search.md:614,:656` — includes a
  runnable `curl` against `https://ziprecruiter.atlassian.net/rest/api/3/myself` and a
  `printf`/`curl` recipe reading `~/.jira_api_token`

No secret VALUE is committed. The complete retrieval procedure is.

- [ ] **D1** — Remove the corporate email and tenant URL; use placeholders.
- [ ] **D2** — Remove the credential-retrieval recipes.

### E. Corporate filesystem layout

```bash
rg -i -n '/Volumes/ziprecruiter' .   # 49 occurrences
```

`/Volumes/ziprecruiter/monorepo` and `/Volumes/ziprecruiter/pristine/` (the latter documented
as an ORG-SHARED read-only reference directory) appear in:

- `packages/claude-extended-tool-approver/internal/rules/envvars/envvars_test.go:180,:186,:240`
- `packages/claude-extended-tool-approver/internal/rules/pathsafety/pathsafety.go:83`
- `docs/superpowers/specs/2026-05-19-pg-pr-design.md:14,:36,:649,:731`
- `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md:93`
- `docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md:491`
- `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md:693,:708`
- `docs/superpowers/plans/2026-06-09-pr-pool-session-lifecycle-and-dedup.md:768,:780,:794`

- [ ] **E1** — Replace the volume paths with a configurable root in source.
- [ ] **E2** — Replace them with placeholders in docs.

### F. Hard-coded employer name in NON-TEST source

```bash
rg -n 'scope: *"ziprecruiter"|"ziprecruiter"' --type go . | rg -v '_test\.go'   # 8
```

The employer name is a shipped runtime label value, not only prose:

- `packages/claude-extended-tool-approver/internal/secretpath/secretpath.go`
- `packages/claude-extended-tool-approver/internal/rules/pathsafety/pathsafety.go:83`
- `packages/pa-monitor/internal/labels/decorator.go`, `internal/tui/details.go`
- `packages/pg-pr/internal/config/config.go`, `packages/pr-pool/cmd/pr-pool/drain.go`

Plus heavy test-fixture use: `packages/pa-monitor-decorator-scope/main_test.go` (25),
`packages/pa-monitor/internal/otel/sessions_zero_test.go` (10), and the `pa-monitor`
`daemon`/`poller`/`config`/`proto` test files.

- [ ] **F1** — Make the scope value configuration-driven; remove the literal from source.
- [ ] **F2** — Replace the literal in test fixtures with a neutral scope name.

### G. Private sibling repo name, Jira keys, and `zr` identifier families

```bash
rg -i -o 'phillipg-nix-ziprecruiter' . | wc -l                    # 183
rg -o -E '\b(FINDEV|JVM)-[0-9]+' . | wc -l                        # 19
rg -o -e '\bzr\b' -e '\bzr[-_./][a-z0-9-]+' -e 'ZR_[A-Z_]+' . | wc -l   # ~2076 surviving
```

| Class                                       |    Count | Notes                                                                                                                                                               |
| ------------------------------------------- | -------: | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `phillipg-nix-ziprecruiter`                 |      183 | private sibling flake; in `flake.nix:23,:1010,:2245,:2333,:2396`, ADRs, `tests/behavior-docs-real-corpus.sh:36`                                                     |
| `FINDEV-*` / `JVM-*`                        |       19 | real Jira project keys — incl. `claude-marketplace/pg-pr/skills/pg-pr-break-down-work/evals/evals.json` and `packages/pg-pr/internal/ticketlink/ticketlink_test.go` |
| `pg-pr-zr`                                  |      212 | Nix module name for the ZR edge                                                                                                                                     |
| `pr-pool-worker-zr` / `pr-pool-feedback-zr` | 128 / 43 | ZR bead-pool namespace baked into Go fixtures                                                                                                                       |
| `pg-pr-issues-jira-zr`                      |       98 | ZR Jira CLI                                                                                                                                                         |
| `ZR_*` env vars                             |      ~20 | `ZR_MACHINE_SUPPORT_WORKSPACE_ROOT`, `ZR_REPO`, `ZR_REV`, `ZR_SET`, `zr_finance`                                                                                    |
| `INTF-ZR`                                   |       17 | behavior-doc element ids                                                                                                                                            |
| Prose `ZR`                                  |      169 | shorthand for the employer throughout docs                                                                                                                          |

Also: `docs/superpowers/2026-06-10-pr-pool-triaging-handoff.md:75` states the live bead
database uses prefix `zr` and is run from `/Volumes/ziprecruiter/monorepo` with
`self_login = phillipgziprecruiter`.

- [ ] **G1** — Rename the `zr` module/namespace families to neutral names.
- [ ] **G2** — Remove the private sibling repo name from `flake.nix` and docs.
- [ ] **G3** — Replace real Jira keys with fictional ones (`PROJ-123`).
- [ ] **G4** — Rewrite prose `ZR` references generically.

### H. Filenames that are themselves references

- [ ] **H1** — `docs/superpowers/plans/2026-06-23-pg-pr-zr-nix-cross-repo-build.md`
- [ ] **H2** — `packages/claude-extended-tool-approver/internal/rules/configrules/testdata/zr-rules.json` (see §B)

### I. Perl toolchain attribution

Benign as a term — no Perl code has ever existed here — but these disclose that the
employer's monorepo is Perl-based:

- `packages/claude-extended-tool-approver/internal/rules/buildtools/buildtools.go:14` —
  `// ... Consumer tools (e.g. ZR's Perl runners prove/yath, project`
- `packages/claude-extended-tool-approver/internal/rules/configrules/configrules.go:162`
- `docs/adr/0033-ceta-config-driven-kubectl-buildtools.md:20`

- [ ] **I1** — Strip the employer attribution; keep the generic tool names if needed.

### J. Findings NOT fixable by editing files

These survive any content edit and MUST be handled explicitly.

| Item                                           | Detail                                                                                                                                                                                                                                   |
| ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Commit authorship**                          | **1,859 of 1,918 commits (97%)** are authored AND committed as `phillipg@ziprecruiter.com`. Also the author name `mayor <phillipg@ziprecruiter.com>` (1 commit). Baked into commit objects — only a history rewrite changes it.          |
| **Unreachable objects**                        | 2 blobs carrying `ziprecruiter` are unreachable from any ref: `5b41060b` (`flake.nix`, `# flox input in ziprecruiter)`) and `23768021` (**referenced by no tree at all** — `git add`-ed, never committed). Invisible to `git log --all`. |
| **Reflog-only objects**                        | ~12, incl. `3d5f0842` (`engine_integration_test.go`, `/Volumes/ziprecruiter/pristine/bin`), `d6ea78a7` (`// ... exercise real ZR behavior`), `b5679aef`/`dfc3f536`/`a9f408cd` (`flake.nix`), and commits `aad62c2f`, `0b368a94`.         |
| **History-only, already scrubbed from `main`** | `//captains-log.zr.org/api` ×34 (commits `a7014b87`, `5d1f58cd`); the `zr-foreman` agent directory (`packages/pgii-pack-foremen/pack-src/agents/zr-foreman/`). Absent from `main`, still in history.                                     |

- [ ] **J1** — Rewrite history (`git filter-repo`) to purge §A, §B, and the history-only items.
- [ ] **J2** — Rewrite commit authorship to the personal identity in the same pass.
- [ ] **J3** — After rewrite, run `git reflog expire --expire=now --all` followed by
      `git gc --prune=now --aggressive`, then re-run the §Provenance sweeps to confirm 0
      across ALL objects.
- [ ] **J4** — Force-push, and confirm the remote no longer serves the old objects.

### K. Prior cleanup failed — do not assume this one holds

Commits `5d1f58cd` / `827623ad` (2026-05-07) claim "scrub ZR-specific content for public
release" and "complete public scrub of company/personal references". Both failed:

1. The scrub was partial — 469 occurrences remain today.
2. **Regression followed**: the §B `zr-rules.json` fixture landed 2026-07-25, eleven weeks after.
3. The scrub commit MESSAGES name `.zr.org`, `.ziprecruiter.com`, `.zipaws.com`, `gozr`, `pyzr`
   and `zn-self-*` — so the scrub commits are themselves references.

- [ ] **K1** — Add a CI guard (a test that fails on the forbidden strings) so regression is
      caught mechanically. `nix-repo-base/modules/jira/pkg/pjira/guardrails_test.go` is a working
      precedent, but it covers ONE module only — this guard MUST cover the whole repo.
- [ ] **K2** — Purge the scrub commit messages themselves in the §J rewrite.

---

## Final item

- [ ] **Z1** — **DELETE THIS FILE.** It names the employer, the private org, the internal
      schema, and a colleague — it is itself a corporate reference and MUST NOT survive the
      cleanup. Removing it is the last step, and it MUST also be purged from history in the
      §J rewrite (or added after the rewrite and removed in a final ordinary commit).

## ⚠️ THIS REPOSITORY IS ALREADY PUBLIC

Verified 2026-08-13 against the GitHub API: `github.com/phillipgreenii/nix-agent-support` has
`visibility: public`, last pushed 2026-08-12. **Everything in §A–§I is world-readable right
now.**

This was confirmed by anonymous fetch — no credential, no token:

```bash
curl -s https://raw.githubusercontent.com/phillipgreenii/nix-agent-support/main/\
packages/pg-pr/pkg/provider/vcs/github/testdata/enriched-prs-single.json   # HTTP 200
```

That returns the §A fixture in full, including the internal PR title
`feat(findev-9345): add usage_pricing_token_dry_run table`, the URL
`https://github.com/ZR-Private/ziprecruiter/pull/91071`, the author login `csziprecruiter`,
and the branch carrying a colleague's full name.

So this is NOT a "clean it up before going public" task. It is remediation of a live exposure,
and it SHOULD be treated as time-sensitive. Note also that the `flake.nix` comments claiming
this repo is private ("Private repo → git+ssh ... monorepod has the SSH key") are **stale** —
they no longer describe reality and are themselves worth correcting.

### A history rewrite does NOT undo publication

Force-pushing a rewritten history removes the objects from `main`, but MUST NOT be treated as
un-publishing. All of the following can retain the old content:

- GitHub continues to serve unreachable objects by SHA on the original repository until
  GitHub Support is asked to run garbage collection.
- Any fork, clone, or mirror made while the content was public.
- Search-engine, CDN, and code-search caches; archival services.
- Anything that scraped the repo for training or indexing.

- [ ] **P1** — Decide, with the employer's guidance if appropriate, whether this rises to a
      disclosure obligation. It contains a third party's name and login plus non-public schema.
- [ ] **P2** — Consider making the repository private IMMEDIATELY as a containment step,
      before the cleanup rather than after. This is reversible; the exposure is not.
- [ ] **P3** — After the §J rewrite and force-push, ask GitHub Support to garbage-collect
      unreachable objects, and check for forks.

## Once this list is complete

Once every item above is done and the verification sweeps return zero across all objects — not
just `main` — **this repository is safe to be public**, which it already is.

If **P2** is taken and the repo is made private as a containment step, then completing this
list is what allows it to go public again. Note the fleet consequence either way:
`homelab/nix/flake.nix` consumes this repo over `git+ssh://git@github.com/...` rather than
`github:`, which is why fleet hosts cannot fetch it without a GitHub credential — see bead
`tc-1q5w`, the fleet has been unable to auto-update since 2026-06-27 for exactly this reason.
Since the repo is in fact public, that input SHOULD be switched to `github:` regardless, which
fixes the fleet blockage independently of this cleanup.
