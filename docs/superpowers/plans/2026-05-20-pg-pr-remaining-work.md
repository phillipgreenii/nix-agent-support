# pg-pr Remaining Work Plan

> **For agentic workers:** This is a roadmap, not a single executable plan. Items are grouped by readiness: implementation-ready tasks have concrete steps; design-needed tasks list the brainstorm prerequisite. Workspace rule: bd is the authoritative tracker — each item below maps to a bd issue (existing or to-be-created). Markdown is design reference; `bd ready` is the work queue.

**Goal:** Drive pg-pr from "Phase 0-4 scaffold complete" to "production-grade and fully functional" without losing the architectural invariants set in `docs/superpowers/specs/2026-05-19-pg-pr-design.md` and ADRs `0007`-`0010`.

**Status as of 2026-05-20:**

- Phases 0-4 closed. CLI builds, sync engine runs, 4 epic bd trees closed (`beads_pg2-k6t`, `-p81`, `-ywy`, `-2ww`).
- Two open bd issues for major post-Phase-4 features: `beads_pg2-srk` (description gen), `beads_pg2-01a` (OTEL).
- Several smaller follow-ups not yet ticketed (this plan creates them).

---

## Section A — Implementation-ready (no new design needed)

These map cleanly to the existing spec. Each should be one focused PR / subagent task.

### A1. scriptout protocol implementation

**Why:** The two ZR extension binaries (`pg-pr-cicd-captains-log`, `pg-pr-issues-jira-zr`) exist but their `main()` calls `scriptout.ServeCICD` / `scriptout.ServeIssues`, both currently stubbed to return `ErrNotImplemented`. Until this lands, no provider extension actually runs.

**Files:**

- Modify: `packages/pg-pr/pkg/plugin/scriptout/scriptout.go`
- Create: `packages/pg-pr/pkg/plugin/scriptout/scriptout_test.go`
- Modify: `packages/pg-pr/internal/sync/sync.go` — instantiate exec-style CICD providers from config

**Protocol** (single stdin-in / stdout-out JSON):

Request (sent to extension binary on stdin):

```json
{
  "op": "list_runs",
  "args": { "repo": "owner/name", "pr_number": 42 }
}
```

Response (extension writes to stdout, then exits 0):

```json
{ "result": [{ "id": "...", "name": "...", "status": "..." }] }
```

Or on error:

```json
{ "error": "captains-log: 401 unauthorized" }
```

**Operations per provider type:**

- VCS: each method on `vcs.Provider` becomes a single op. Op name = method name in `snake_case`.
- CICD: `list_runs`, `get_logs`, `rerun_failed`.
- Issues: `get_issue`.

**ServeCICD body sketch:**

```go
func ServeCICD(p cicd.Provider) error {
    var req struct {
        Op   string          `json:"op"`
        Args json.RawMessage `json:"args"`
    }
    if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
        return writeErr(fmt.Errorf("decode request: %w", err))
    }
    ctx := context.Background()
    switch req.Op {
    case "list_runs":
        var a struct {
            Repo     string `json:"repo"`
            PRNumber int    `json:"pr_number"`
        }
        if err := json.Unmarshal(req.Args, &a); err != nil {
            return writeErr(err)
        }
        runs, err := p.ListRuns(ctx, a.Repo, a.PRNumber)
        if err != nil {
            return writeErr(err)
        }
        return writeResult(runs)
    case "get_logs":
        // …
    case "rerun_failed":
        // …
    default:
        return writeErr(fmt.Errorf("unknown op %q", req.Op))
    }
}

func writeResult(v interface{}) error {
    return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"result": v})
}

func writeErr(err error) error {
    json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
    os.Exit(1)
    return nil
}
```

**Caller side** (in `internal/sync` or a new `internal/provider/exec/`): a `cicd.Provider` impl that takes a binary name + args, spawns it, writes the request, reads the response, decodes. Return errors verbatim.

**Tests:**

- A fake provider that returns canned data; serve it; spawn the test binary via `exec.Command(os.Args[0], "-test.run=TestHelperProcess", …)` pattern (standard Go test-helper-process trick).
- Verify protocol shape, error propagation, non-zero exit on error.

**Acceptance:**

- `pg-pr ci runs --repo foo/bar 42` against a config with `cicd: exec:pg-pr-cicd-captains-log` actually invokes the binary.
- The binary's main_test.go end-to-end test passes against a real fake collector (httptest.Server).

**Bd issue:** to be created — see Section D.

---

### A2. Daemon mode (`pg-pr sync --daemon`)

**Why:** Currently `--daemon` flag exists but prints "lands in Phase 3" and exits. Daemon is required for hands-off sync.

**Files:**

- Modify: `packages/pg-pr/cmd/pg-pr/sync.go`
- Create: `packages/pg-pr/internal/sync/daemon.go`
- Create: `packages/pg-pr/internal/sync/daemon_test.go`

**Behavior** (per spec §"Daemon mode"):

- Loop: `Sync(ctx)` → sleep `interval` → repeat.
- Single-instance enforced via `flock` on `$XDG_RUNTIME_DIR/pg-pr/daemon.lock`.
- `SIGHUP` triggers config reload (re-read `$XDG_CONFIG_HOME/pg-pr/config.yaml`).
- `SIGTERM` finishes current iteration, releases lock, exits cleanly.
- Logs to stderr; `--log-json` toggles structured.
- Default interval: 10m.

**Code outline:**

```go
package sync

func (e *Engine) Daemon(ctx context.Context, interval time.Duration, sighup <-chan os.Signal) error {
    lockPath := filepath.Join(runtimeDir(), "pg-pr", "daemon.lock")
    if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
        return err
    }
    f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
    if err != nil {
        return err
    }
    defer f.Close()
    if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
        return fmt.Errorf("another pg-pr daemon is running (lock held)")
    }
    defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

    for {
        if _, err := e.Sync(ctx); err != nil {
            // log but don't crash; daemon survives partial failures
        }
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-sighup:
            // reload config
        case <-time.After(interval):
        }
    }
}
```

**Tests:** ctx cancellation triggers clean exit; lock-already-held returns clear error; SIGHUP reloads (use a fake signal channel).

**Nix delivery:** add systemd user unit + launchd agent to `home/programs/pg-pr/default.nix`. Disabled by default; opt in via `phillipgreenii.programs.pg-pr.daemon.enable = true;`.

**Bd issue:** to be created.

---

### A3. `pg-pr changes --since <ts>` subcommand

**Why:** Spec specifies this verb for gascity (and any external caller) to learn what changed since a timestamp. Source: bd query for beads where `updated_at > ts`; errors enriched from `$XDG_STATE_HOME/pg-pr/repo-state.json`.

**Files:**

- Create: `packages/pg-pr/cmd/pg-pr/changes.go`
- Create: `packages/pg-pr/internal/changes/changes.go`
- Create: `packages/pg-pr/internal/changes/changes_test.go`

**Output JSON sections** (per spec):

```json
{
  "created": [{ "id": "...", "type": "merge-request", "updated_at": "..." }],
  "updated": [...],
  "closed": [...],
  "errors": [{ "repo": "...", "message": "..." }]
}
```

**Implementation:**

- Call `bd list --json --updated-after <ts>` (verify exact flag with `bd list --help`). Filter to pg-pr-managed bead types: `merge-request`, `feedback`, `task` with `parent-child` dep to a merge-request, and `task`/`bug` actions.
- Read `repo-state.json` and emit the error block.
- Tests use a real temp bd workspace populated with synthetic beads at known timestamps.

**Acceptance:** `pg-pr changes --since 2026-05-20T00:00:00Z --json` runs cleanly against an empty workspace (returns `{}` shaped output); against a populated workspace, returns the expected categorization.

**Bd issue:** to be created.

---

### A4. `pg-pr auth status` subcommand

**Why:** Spec specifies per-provider auth diagnostics. Currently no admin commands exist.

**Files:**

- Create: `packages/pg-pr/cmd/pg-pr/auth.go`
- Modify: `packages/pg-pr/internal/auth/auth.go` (replace stub)

**Per-provider checks:**

| Provider       | Check                                                                                            |
| -------------- | ------------------------------------------------------------------------------------------------ |
| github         | `gh auth status` exit code + scopes.                                                             |
| jira (builtin) | env vars `JIRA_API_TOKEN`, `JIRA_EMAIL`, `JIRA_BASE_URL` present; one `HEAD /rest/api/3/myself`. |
| github-actions | same as github.                                                                                  |
| Exec providers | invoke binary with op `auth_status` (extension protocol op).                                     |

**Output:** human table by default, `--json` for machine. Non-zero exit per failing provider.

**Bd issue:** to be created.

---

### A5. `pg-pr config show / validate` subcommands

**Why:** Spec specifies these admin verbs. Useful for shell completions, debugging, gascity introspection.

**Files:**

- Create: `packages/pg-pr/cmd/pg-pr/config.go`

**`config show`:** print resolved config (after env / flag overrides). Human default, `--json` for machine.

**`config validate`:** load + validate. Non-zero exit on invalid. Useful for nix activation scripts to fail fast.

**Bd issue:** to be created.

---

### A6. `pg-pr issue show <ticket>` subcommand

**Why:** Spec specifies issue-tracker provider reads. Currently no CLI surface for it.

**Files:**

- Create: `packages/pg-pr/cmd/pg-pr/issue.go`

**Behavior:** resolve the configured issues provider (jira or github-issues), call `GetIssue(ticket)`, print human or JSON.

**Bd issue:** to be created.

---

### A7. `pg-pr comment respond <feedback-id>` real impl

**Why:** Currently returns a "Phase 3" error. Now Phase 3's feedback bead lifecycle exists; this can be wired.

**Steps:**

1. Look up feedback bead by id via `bd show --json <id>` (or `beads.ListFeedback`).
2. Extract `external_id` and `kind` metadata.
3. For `kind=comment-thread`: call `vcs.ReplyToThread(repo, external_id, body)`.
4. For `kind=review-thread`: same.
5. For other kinds: return clear error ("cannot respond to <kind> feedback").

**Files:**

- Modify: `packages/pg-pr/cmd/pg-pr/comment.go` (or wherever `comment respond` lives)
- Possibly add helper: `internal/feedbacklookup/`

**Bd issue:** to be created.

---

### A8. `pg-pr pr close` should also close the merge-request bead

**Why:** Current impl calls `vcs.Close` but leaves the bead open. Spec says sync's cascade-on-close handles this on next sync; that's correct but lossy in the meantime. Closing the bead in-line is cleaner.

**Steps:**

1. After `vcs.Close` succeeds, query `beads.ListMergeRequests` for `(repo, pr_number)` match.
2. If found and open: `beads.CloseMergeRequest(id, "closed via pg-pr pr close")`.
3. If not found: silently skip (next sync will reconcile).

**Files:**

- Modify: `packages/pg-pr/cmd/pg-pr/pr_write.go`

**Bd issue:** to be created.

---

### A9. `pg-pr pr create` should push `--reviewers` and `--labels` to GitHub

**Why:** Currently the flags are stored as bead metadata only, not applied to the PR. Half-feature.

**Steps:**

- After successful `gh pr create`, run `gh pr edit <n> --add-reviewer <list> --add-label <list>` (or pass them to the create command directly with the correct gh flags).
- Verify reviewer assignment works for both individual users (`--reviewer u1,u2`) and teams (`--reviewer org/team`).

**Files:**

- Modify: `packages/pg-pr/pkg/provider/vcs/github/github.go` (extend `CreatePR` signature)
- Modify: `packages/pg-pr/cmd/pg-pr/pr_write.go` (pass through the new fields)

**Bd issue:** to be created.

---

### A15. Global `--json` flag + `PGPR_OUTPUT=json` env var

**Why:** Agents calling pg-pr from heterogeneous contexts (skill, gascity, manual) need a way to force JSON output without a flag at every site. Spec §"CLI surface" intro now says: `--json` flag wins, env var `PGPR_OUTPUT=json` is the global fallback.

**Files:**

- Modify: every `cmd/pg-pr/<group>.go` that has a `--json` flag — read the env var as the flag default before parsing.
- Cleanest: a small helper `internal/output/format.go` exposing `Resolve(flag bool) bool` that returns `flag || os.Getenv("PGPR_OUTPUT") == "json"`.
- Tests: table-driven — flag-set / env-set / both / neither — verify each command honors precedence.

**Acceptance:**

- `PGPR_OUTPUT=json pg-pr pr show 1 --repo owner/name` emits JSON.
- `PGPR_OUTPUT=json pg-pr pr show 1 --repo owner/name --json=false` emits human (explicit flag false beats env). (If cobra makes `--json=false` awkward, then the precedence is: any `--json` presence wins over env.)
- All existing tests still pass.

**Bd issue:** to be created.

---

### A10. Pre-commit hook entry for `modules/pg-pr-zr/` in nix-ziprecruiter

**Why:** Phase 4 deferred this. Without it, the ZR extension Go code is not auto-linted.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/flake.nix` — extend the pre-commit `extraHooks` to include `gofmt` + `golangci-lint` scoped to `modules/pg-pr-zr/`.

**Steps:**

1. Add hooks to the relevant attribute set.
2. `nix run .#install-pre-commit-hooks` to regenerate `.pre-commit-config.yaml`.
3. `prek run --all-files` to confirm clean.

**Bd issue:** to be created.

---

### A11. Convert `modules/pg-pr-zr/default.nix` to `buildGoModule` once pg-pr is a flake output

**Why:** Currently uses relative-path `replace` directive, which `buildGoModule` can't see (hermetic). Pinned to a follow-up.

**Steps:**

1. In `phillipgreenii-nix-agent-support/flake.nix`: expose `packages.pg-pr-lib` (or similar) for downstream consumption. Effectively a `pkgs.runCommand` that vendors `packages/pg-pr/` into a fetchable location.
2. In `modules/pg-pr-zr/go.mod`: drop the `replace` directive; depend on the published path.
3. `modules/pg-pr-zr/default.nix`: switch from `mkDerivation` to `buildGoModule`.

**Caveats:** Until pg-pr ships a real Go release on GitHub, this is awkward. Alternative: use nix's `fetchFromGitHub` + a fixed-output hash referencing the same monorepo. Cleaner once the repo has tagged releases.

**Bd issue:** to be created. Low priority; current relative-path replace works fine for local dev.

---

### A12. Parallel-run validation week + delete `gc/assets/imports/zr/`

**Why:** Phase 4's epic was closed but the actual cutover is pending. Issue `beads_pg2-56a` (already closed with a doc deliverable) should be reopened or split: the doc is done; the action is pending.

**Steps:**

1. Enable `modules/pg-pr-zr` in `phillipg-nix-mbp` machine config.
2. Run both packs simultaneously for 7 days.
3. Daily check (per `modules/pg-pr-zr/MIGRATION.md`): bead counts match between old and new, no duplicate actions, agents claim correctly.
4. At end of week, if green: `rm -rf /Users/phillipg/gc/assets/imports/zr/`. Commit.
5. Add the deletion to `gc` repo with a clear message.

**Bd issue:** create a new one (the original was prematurely closed as a doc deliverable).

---

### A13. Captain's Log real API endpoints

**Why:** `cmd/pg-pr-cicd-captains-log/README.md` documents placeholder endpoints. Need to confirm actual ZR Captain's Log API contract.

**Steps:**

1. Manually call Captain's Log API to verify endpoint shapes (likely needs internal ZR docs or asking the team).
2. Update `main.go` and `README.md` with real endpoints.
3. Smoke test against staging instance.

**Bd issue:** to be created. Blocked-by: API contract verification.

---

### A14. ZR jira config wiring

**Why:** `pg-pr-issues-jira-zr` reads `JIRA_BASE_URL` / `JIRA_API_TOKEN` / `JIRA_EMAIL` from env. Need to wire these from the home-manager module so they're available to the daemon / gascity processes.

**Steps:**

1. In `phillipg-nix-ziprecruiter/modules/pg-pr-zr/default.nix`: add nix options `phillipgreenii.modules.pg-pr-zr.jira = { baseUrl, tokenFile, email };`.
2. Emit a wrapper script around `pg-pr-issues-jira-zr` that sets env vars from secret file at runtime.
3. Document in `MIGRATION.md`.

**Bd issue:** to be created.

---

## Section B — Needs design (brainstorm prerequisite)

These items have meaningful design questions that should go through a brainstorm round before implementation. Each one likely produces its own spec + ADR.

### B1. Unified PR description generation (`beads_pg2-srk`)

**Decided design** (per user feedback 2026-05-20; see spec §"Post-v1 design directions" → "Unified PR description generation"):

- **SKILL, not command.** Agents may decide to create PRs autonomously; SKILLs auto-trigger on intent, commands require deliberate invocation. SKILL path: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-write-pr-description/SKILL.md`.
- **Context gathering lives in the CLI.** Skill calls `pg-pr pr show --json`, `pg-pr pr files --json`, `pg-pr pr commits --json` to assemble context. CLI already exposes these — skill is thin.
- **Three callers, one prompt:**
  - Direct claude session: skill auto-triggers when user asks Claude to create/update a PR.
  - Gascity: agents in a gascity session also auto-trigger the same skill.
  - Direct CLI (`pg-pr pr create --generate-description`): CLI shells out via `zr-agent <skill>` (existing generic agent-CLI wrapper in nix-ziprecruiter) which spawns a fresh agent process that reads the same SKILL.md and calls back into `pg-pr` in a new process. Two-process trip is acceptable; consistency over process count.
- **Body emit path:** skill writes to `pg-pr pr update <n> --body-stdin` or `pg-pr pr create --body-stdin`.

**Implementation plan (post-brainstorm-confirmation):**

1. Add `SKILL.md` content with explicit `description:` frontmatter that triggers on PR create/update intent.
2. Add `--generate-description` flag to `pg-pr pr create` / `pg-pr pr update` that shells out via `zr-agent`.
3. `zr-agent` binary already exists in `phillipg-nix-ziprecruiter/modules/zr-agent/`; verify it can take a skill path as the prompt source.
4. Tests with a fake `zr-agent` impl to verify the call shape.

**Bd:** `beads_pg2-srk` (existing).

### B2. OTEL + Prometheus instrumentation (`beads_pg2-01a`)

**Decided design** (per user feedback 2026-05-20; see spec §"Post-v1 design directions" → "OTEL + Prometheus instrumentation"):

- **Span boundary** = one sync run per repo. Provider calls and bd writes nest as child spans.
- **Allowed trace attributes:** `repo_name`, `pr_number`, `run_id`, `author_email`. Comment body deferred (probably acceptable but waits for privacy review).
- **Startup behavior:** missing/unreachable OTEL OTLP endpoint emits a one-line stderr warning and continues. NOT a startup error.
- **OTEL endpoint config:** env vars matching `~/gc`'s convention. Read the existing setup before implementing to match exactly.
- **Daemon Prometheus endpoint:** scrape endpoint on `127.0.0.1:<port>`. Metrics: per-PR sync duration histogram, sync errors counter (label=repo), feedback beads created counter, ci-only-attempts gauge, last-successful-sync-time gauge.
- **One-shot sync** emits traces but no metrics endpoint.

**Implementation plan (post-brainstorm-confirmation):**

1. Read `~/gc` workspace's OTEL setup to learn the exact env var names and SDK initialization pattern.
2. Add OTEL SDK dependency. Initialize tracer + meter providers at CLI startup. No-op gracefully when endpoint missing.
3. Instrument `internal/sync/sync.go` — one span per `syncRepo(ctx, repoCfg)`. Provider call spans inside.
4. Add daemon Prometheus endpoint: gorilla/mux or stdlib `http.ServeMux` on a configurable bind address. Use `prometheus/client_golang`.
5. Tests for span emission (use `tracetest` SDK helpers), metrics endpoint shape, graceful no-op on missing endpoint.

**Bd:** `beads_pg2-01a` (existing).

### B3. Sync auto-reply to feedback (v2)

**Decided design** (per user feedback 2026-05-20; see spec §"Open questions / v2 deferrals"):

- **Reply text storage:** bd custom metadata `reply_draft` on the feedback bead. If bd supports proper custom fields per type, use them; otherwise generic metadata.
- **Trigger:** when sync sees a feedback bead with non-empty `reply_draft` and no `response_id` yet, post via `vcs.ReplyToThread` and store the returned `response_id` back on the bead.
- **Idempotency:** presence of `response_id` skips the post. Re-runs are safe.
- **Thread auto-resolve:** NO. Agents must not auto-resolve threads. Resolution stays a human decision.

**Bd:** to be created.

### B4. Forgejo VCS provider (v2)

**Deferred until GitHub implementation has full sign-off.** Per user direction 2026-05-20: plan is fine but do not start implementation until GitHub is fully validated in production. The VCS interface affordance already exists; adding a Forgejo impl is non-breaking when ready.

Open design questions to revisit when GitHub signoff lands:

- Forgejo API differences from GitHub (REST shape, auth flow).
- Multi-VCS config: a repo with both github and forgejo remotes? Or strictly one VCS per repo?
- Test environment for forgejo (run a local forgejo container?).

**Bd:** to be created with explicit `deferred` status until signoff.

---

## Section C — v2 explicit deferrals (per spec)

Documented as out-of-scope for current work; tracked for completeness.

- Webhook-driven sync (push, not poll) — daemon poll is sufficient.
- Per-repo provider override at command-line — `--cicd` / `--issues` flags.
- Watch-mode CICD (`gh run watch` style) — `pg-pr ci watch <pr>` as future verb.
- Captain's Log adapter as a kubectl-style command plugin alternative to the script-out provider.

No bd issues for these yet; create as priorities clarify.

---

## Section C — Bd formulas

bd has 21 reusable workflow formulas (`bd formula list`). When creating future bd issue trees for these tasks, consider:

- `mol-do-work` — simple work formula (read bead, do what it says, close). Good for A6, A8, A15 (small, well-specified).
- `wf-bugfix` — fast-path bug fix; lightweight planning gate then implement. Good for any bug that comes up.
- `wf-echo` — test workflow with human-approval step. Not applicable here, but a useful template for `pg-pr ci watch` style work that needs human gates.
- `mol-scoped-work` — graph-first worktree lifecycle. Possibly useful for cross-repo work like A11/A12.

Use `bd mol pour <formula>` to instantiate. For one-off implementation tasks, plain `bd create --type=task` is usually fine; reach for a formula when a task has multiple steps with structured handoffs.

## Section D — bd issue creation

After this plan is approved, create bd issues for every item in Sections A and B (and B3, B4 in Section B) that doesn't already have one. Link them as children of a new epic `pg-pr Phase 5: production-readiness`.

Suggested epic:

```bash
PHASE5=$(bd create --type=epic --priority=2 \
  --title="pg-pr Phase 5: production-readiness" \
  --description="Wrap up scaffold gaps: scriptout protocol, daemon, changes/auth/config/issue admin commands, comment respond, pr close+create polish, ZR module pre-commit + buildGoModule conversion, parallel-run cutover. See docs/superpowers/plans/2026-05-20-pg-pr-remaining-work.md." \
  --json | jq -r '.id // .ID')
```

Then create the A-items as children with `--type=parent-child` dep to `$PHASE5`.

For B1 and B2 (description gen, OTEL), the existing issues `beads_pg2-srk` and `beads_pg2-01a` can be linked as children of a separate epic `pg-pr Phase 6: design-driven features`. They each need their own brainstorm round before implementation tasks are written.

---

## Execution sequencing

Recommended order (rough cost / dependency model):

1. **A1 scriptout** — unblocks the two ZR extension binaries. Once shipped, A13/A14 become testable.
2. **A2 daemon** — small, isolated, high-value.
3. **A3 changes / A4 auth status / A5 config show/validate / A6 issue show** — can be done in parallel (independent verbs). One subagent batch.
4. **A7 comment respond** — depends on feedback bead lifecycle (Phase 3 done) → straightforward now.
5. **A8 / A9** — polish on existing pr verbs.
6. **A10 / A11** — nix housekeeping.
7. **A12 parallel-run cutover** — calendar-time gated (7 days).
8. **A13 / A14** — depend on ZR API discovery; can be picked up as info arrives.
9. **B1 / B2** — schedule brainstorm rounds. Once spec amendments land, write phase plans for each.
10. **B3 / B4** — defer until B1/B2 settle.

---

## Out of scope for this plan

- Modifying `/Volumes/ziprecruiter/pristine/.claude/`. Read-only per spec.
- Reorganizing the existing pg-pr-plugin SKILL.md / agent prompts beyond what's already shipped. Phase 6 brainstorms may revisit.
- Re-opening Phases 0-4 epics. Anything missed lands as a Phase 5 ticket, not as a re-do.
