# pr-pool deny-by-default + constrained allowed-tools — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-3msk` (P1, bug, label `pr-pool`) — depends on `pg2-sjrl` (ccpool `--allowed-tools` passthrough flag).

> **SCOPE NOTE (stale bead refs):** The bead's file refs (`config.go:70`, `cli.go:61-63`, `--dangerously-skip-permissions`) **predate** the PermissionMode migration and are wrong. The corrected, code-verified scope (per the 2026-06-23 roadmap + the bead's 2026-06-23 comment) is implemented here. The `--dangerously-skip-permissions` flag no longer exists; pr-pool already launches workers via `--permission-mode` (default `bypassPermissions`).

> **⚠️ HUMAN REVIEW REQUIRED — security-sensitive default allowlist.** Task 2 introduces a default `AllowedTools` allowlist that becomes the security boundary for every autonomous worker. The exact contents (the comma-separated list literal) **MUST be reviewed and signed off by a human before this branch merges.** See "BLOCKING — human sign-off" at the end of this plan for the proposed list and the rationale for each entry. Do NOT merge with an un-reviewed list.

**Goal:** Harden pr-pool against a prompt-injection→RCE path by (1) flipping the default `PermissionMode` from `bypassPermissions` (permissionless) to `dontAsk` (deny-by-default, non-interactive), and (2) constraining every dispatched worker to a conservative default tool allowlist emitted via the new ccpool `--allowed-tools` flag.

**Architecture:** Worker prompts are derived from external PR-reviewer comments, so a malicious comment is an injection vector into a Claude session that can `git push`. Two layers close it together: `dontAsk` makes Claude **auto-deny** any tool not pre-approved (instead of stalling on a human-less permission prompt), and `--allowed-tools` defines the pre-approved set. `dontAsk` is what makes an allowlist both *safe* (un-listed tools are denied, not silently allowed) **and** *non-interactive* (a denial returns to the model as feedback rather than hanging on an unanswerable prompt). `PR_POOL_PERMISSION_MODE` remains the operator's opt-in escape back to `bypassPermissions` for an attended/trusted run. The new `AllowedTools` config scalar mirrors the existing `PermissionMode`/`Effort`/`Model` plumbing exactly: a `Config` field → carried on `CLIRunner` → emitted by `Ensure` as `--allowed-tools <value>` (the `pg2-sjrl` ccpool flag) when non-empty.

**Tech Stack:** Go (stdlib, `reflect`-based table tests). Package `phillipgreenii-nix-agent-support/packages/pr-pool`. Consumes the ccpool `--allowed-tools` flag delivered by `pg2-sjrl` (assumed to exist; plan `2026-06-23-ccpool-allowed-tools-flag.md`). `claude` accepts `--allowed-tools <tools...>` (comma/space-separated, e.g. `"Bash(git *)"`; verified via `claude --help`, 2026-06-23).

**Branch:** `pr-pool-deny-by-default` (off `main`).

**Why coupled (and why this order):** With `bypassPermissions`, an allowlist is moot (everything is allowed). With claude's *default* mode, an un-pre-approved tool prompts a human who never arrives → the worker stalls until `MaxWait` kills it. Only `dontAsk` gives auto-deny, so Task 1 (mode flip) and Task 2 (allowlist) ship together. The ccpool `--allowed-tools` flag (`pg2-sjrl`) MUST already be merged — this plan only emits it.

**Dependency precondition (verify first):** `pg2-sjrl` is merged: `ccpool new --allowed-tools <list>` exists and forwards `--allowed-tools` to claude. Confirm with `cd ../ccpool && go run ./cmd/ccpool new 2>&1 | grep -- --allowed-tools` (expect the usage line to list `--allowed-tools`). If absent, STOP — this plan cannot be completed.

---

## File map

| File | Responsibility | Change |
|---|---|---|
| `packages/pr-pool/internal/config/config.go` | pool-scalar config: struct, defaults, env overlay | Flip `PermissionMode` default; add `AllowedTools` field + default + env overlay |
| `packages/pr-pool/internal/config/config_test.go` | config unit tests | Assert new default mode + default allowlist + env override |
| `packages/pr-pool/internal/ccpool/cli.go` | `CLIRunner` seam to `ccpool new` | Add `AllowedTools` field; emit `--allowed-tools` in `Ensure` |
| `packages/pr-pool/internal/ccpool/cli_test.go` | `CLIRunner` argv tests | Assert `--allowed-tools` argv position; update existing default-argv expectations |

No `example.go` change: `PermissionMode`/`Effort`/`Model` are pool scalars and are NOT serialized by `emitRole` (which emits per-role fields only). `AllowedTools` follows the same pattern — confirmed by reading `internal/config/example.go` (`emitRole` lines 56-73 emit only role/query fields).

---

### Task 1: Flip the default `PermissionMode` to `dontAsk`

**Files:**
- Modify: `packages/pr-pool/internal/config/config.go:80`
- Modify: `packages/pr-pool/internal/config/config_test.go:44-46`
- Modify: `packages/pr-pool/internal/ccpool/cli_test.go:43` (the existing argv test bakes in the old default)

- [ ] **Step 1: Update the failing default-mode test**

In `internal/config/config_test.go`, replace the `TestDefault` PermissionMode assertion (lines 44-46) with the new expectation:

```go
	if d.PermissionMode != "dontAsk" {
		t.Errorf("PermissionMode = %q, want dontAsk (deny-by-default: auto-deny un-allowlisted tools, non-interactive)", d.PermissionMode)
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestDefault -v`
Expected: FAIL — `PermissionMode = "bypassPermissions", want dontAsk`.

- [ ] **Step 3: Flip the default in `Default()`**

In `internal/config/config.go`, change line 80:

```go
		PermissionMode: "dontAsk", // deny-by-default: auto-DENY any tool outside AllowedTools, non-interactive. PR_POOL_PERMISSION_MODE=bypassPermissions is the opt-in escape for an attended/trusted run.
```

- [ ] **Step 4: Run the config test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'TestDefault|TestValidate_permissionMode|TestLoad_envOverrides' -v`
Expected: PASS. `dontAsk` is already in `validPermissionModes` (config.go:165), so `Validate()` accepts it; `PR_POOL_PERMISSION_MODE` overlay (config.go:113) is unchanged, so the env-override test still passes.

- [ ] **Step 5: Update the ccpool CLIRunner argv test that baked in the old default**

`internal/ccpool/cli_test.go:43` asserts the default-built argv ends with `"--permission-mode", "bypassPermissions"`. That test (`TestEnsure_argv`) constructs `NewCLIRunner(config.Default())`, so the flipped default changes its expected argv. This task changes ONLY the mode token; the `--allowed-tools` argv is added in Task 2 (this test's expected argv is fully rewritten there). For now, change `bypassPermissions` → `dontAsk` on line 43:

```go
		"--permission-mode", "dontAsk", "--effort", "max",
```

- [ ] **Step 6: Run the ccpool test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -run TestEnsure_argv -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/ccpool/cli_test.go
git commit -m "fix(pr-pool): default PermissionMode dontAsk (deny-by-default)

Worker prompts derive from external PR comments (prompt-injection->RCE
vector). dontAsk auto-denies un-allowlisted tools non-interactively
instead of launching permissionless (bypassPermissions). The opt-in
escape stays: PR_POOL_PERMISSION_MODE=bypassPermissions. Refs pg2-3msk."
```

---

### Task 2: Add a constrained default `AllowedTools` config scalar

**Files:**
- Modify: `packages/pr-pool/internal/config/config.go` — `Config` struct (after line 37), `Default()` (after line 80), `Load()` env overlay (after line 113)
- Modify: `packages/pr-pool/internal/config/config_test.go` — add default + env-override assertions
- Test: `packages/pr-pool/internal/config/config_test.go`

> **⚠️ The literal in Step 3 is the security boundary. It MUST be human-reviewed before merge** (see "BLOCKING — human sign-off"). Treat the value below as the *proposed* default pending sign-off.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
func TestDefault_allowedTools(t *testing.T) {
	d := Default()
	if d.AllowedTools == "" {
		t.Fatal("AllowedTools default must be a non-empty allowlist (deny-by-default needs an allowlist to be useful)")
	}
	// Sanity: the conservative default must grant the worker its core verbs and
	// must NOT be a blanket "Bash" (which would re-open arbitrary RCE).
	for _, must := range []string{"Read", "Edit", "Write", "Bash(git "} {
		if !strings.Contains(d.AllowedTools, must) {
			t.Errorf("AllowedTools default %q missing required entry %q", d.AllowedTools, must)
		}
	}
	if strings.Contains(d.AllowedTools, "Bash(*)") || strings.Contains(d.AllowedTools, ",Bash,") ||
		strings.HasSuffix(d.AllowedTools, ",Bash") || d.AllowedTools == "Bash" {
		t.Errorf("AllowedTools must not grant unrestricted Bash: %q", d.AllowedTools)
	}
}

func TestLoad_allowedToolsEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_ALLOWED_TOOLS", "Read,Edit")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowedTools != "Read,Edit" {
		t.Errorf("AllowedTools = %q, want Read,Edit (PR_POOL_ALLOWED_TOOLS overlay)", c.AllowedTools)
	}
}
```

Add `"strings"` to the `import` block of `config_test.go` (it currently imports `os`, `path/filepath`, `testing`, `time`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'AllowedTools' -v`
Expected: FAIL — `Config` has no field `AllowedTools` (compile error).

- [ ] **Step 3: Add the `AllowedTools` field to the `Config` struct**

In `internal/config/config.go`, inside `type Config struct`, after `PermissionMode string` (line 37):

```go
	// AllowedTools is the claude --allowed-tools allowlist forwarded verbatim to
	// `ccpool new --allowed-tools`. Combined with PermissionMode=dontAsk it is the
	// worker's security boundary: any tool NOT matching an entry here is
	// auto-denied (no human prompt). Empty omits the flag (claude's own default
	// tool policy applies — used only when an operator deliberately clears it).
	// SECURITY-SENSITIVE: the default value in Default() requires human sign-off.
	AllowedTools string
```

- [ ] **Step 4: Add the default allowlist to `Default()`**

In `internal/config/config.go`, inside the `Default()` return literal, after `PermissionMode:` (line 80). **This literal is the human-sign-off item:**

```go
		// SECURITY-SENSITIVE default allowlist (HUMAN SIGN-OFF REQUIRED — see plan).
		// Minimum verbs an autonomous worker needs; deliberately NOT blanket Bash.
		// Per-entry rationale is in docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md.
		AllowedTools: "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*)",
```

- [ ] **Step 5: Add the env overlay in `Load()`**

In `internal/config/config.go`, in `Load()`, after the `PermissionMode` overlay (line 113):

```go
	c.AllowedTools = envStr("PR_POOL_ALLOWED_TOOLS", c.AllowedTools)
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'AllowedTools|TestDefault|TestLoad_envOverrides' -v`
Expected: PASS — default is non-empty, contains the required verbs, has no blanket Bash; the env overlay applies.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(pr-pool): add constrained default AllowedTools allowlist

Conservative deny-by-default allowlist (Read/Edit/Write/Glob/Grep, git
verbs, bd, go/nix build+test). PR_POOL_ALLOWED_TOOLS overrides. NOT a
blanket Bash. SECURITY-SENSITIVE default — pending human sign-off.
Refs pg2-3msk."
```

---

### Task 3: Emit `--allowed-tools` through the CLIRunner seam

**Files:**
- Modify: `packages/pr-pool/internal/ccpool/cli.go:39-56` (`CLIRunner` struct + `NewCLIRunner`), `:124-126` (`Ensure` flag emission), `:107-110` (doc comment)
- Modify: `packages/pr-pool/internal/ccpool/cli_test.go` — argv assertions
- Test: `packages/pr-pool/internal/ccpool/cli_test.go`

- [ ] **Step 1: Write the failing tests**

In `internal/ccpool/cli_test.go`, **rewrite** the expected argv in `TestEnsure_argv` (the existing test, lines 37-44, currently ends at `"--effort", "max"`) so the default allowlist is asserted right after the permission mode (the order `Ensure` emits: `--permission-mode`, `--allowed-tools`, `--effort`, `--model`):

```go
	want := []string{
		"new", "pr-pool-worker-zr-1-20260616T010203", "--cwd", "/repo",
		"--name", "pr-pool-worker-zr-1",
		"--env", "BEADS_ACTOR=pgii-pool__worker",
		"--env", "BEADS_DIR=/repo/.beads",
		"--env", "WORKSPACE_ROOT=/repo",
		"--permission-mode", "dontAsk",
		"--allowed-tools", config.Default().AllowedTools,
		"--effort", "max",
	}
```

Then add a focused test asserting the empty-allowlist case omits the flag and a non-default value is forwarded verbatim:

```go
func TestEnsure_allowedTools(t *testing.T) {
	// Non-default allowlist is forwarded verbatim, positioned after --permission-mode.
	var got [][]string
	cfg := config.Default()
	cfg.PermissionMode = "dontAsk"
	cfg.AllowedTools = "Read,Bash(git *)"
	cfg.Effort = ""
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--permission-mode", "dontAsk", "--allowed-tools", "Read,Bash(git *)"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v", got[0], want)
	}
}

func TestEnsure_allowedToolsEmptyOmitsFlag(t *testing.T) {
	var got [][]string
	cfg := config.Default()
	cfg.PermissionMode = ""
	cfg.AllowedTools = "" // empty => no --allowed-tools flag
	cfg.Effort = ""
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v (empty AllowedTools must omit the flag)", got[0], want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -run 'TestEnsure_argv|TestEnsure_allowedTools' -v`
Expected: FAIL — `CLIRunner` has no field `AllowedTools` (compile error), and `TestEnsure_argv`'s expected argv now contains `--allowed-tools` that `Ensure` does not yet emit.

- [ ] **Step 3: Add the `AllowedTools` field to `CLIRunner`**

In `internal/ccpool/cli.go`, inside `type CLIRunner struct`, after the `PermissionMode` field (line 42):

```go
	AllowedTools string // claude --allowed-tools allowlist; emitted on `ccpool new` when non-empty
```

- [ ] **Step 4: Populate it in `NewCLIRunner`**

In `internal/ccpool/cli.go`, update the `CLIRunner` literal in `NewCLIRunner` (line 51) to carry the config value:

```go
	c := &CLIRunner{Effort: cfg.Effort, Model: cfg.Model, PermissionMode: cfg.PermissionMode, AllowedTools: cfg.AllowedTools, bin: "ccpool"}
```

- [ ] **Step 5: Emit the flag in `Ensure`**

In `internal/ccpool/cli.go`, in `Ensure`, insert the allowed-tools emission immediately after the `--permission-mode` block (after line 126), before the `--effort` block:

```go
	if c.AllowedTools != "" {
		args = append(args, "--allowed-tools", c.AllowedTools)
	}
```

Also update the `Ensure` doc comment (lines 107-110) to include the flag in the documented order:

```go
// Ensure: ccpool new <external_id> --cwd <cwd> [--name <name>] --env K=V…
// --permission-mode <mode> [--allowed-tools <list>] --effort <effort> [--model <model>].
// The session is addressed by external_id; name is an optional display label
// (omitted when empty). env keys sorted for deterministic argv.
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -v`
Expected: PASS — `TestEnsure_argv` matches the new default argv (mode `dontAsk` + default allowlist + effort), `TestEnsure_allowedTools` forwards verbatim, `TestEnsure_allowedToolsEmptyOmitsFlag` omits the flag. `TestEnsure_argv_withModel_noPermissionMode_noName` (line 50) still passes: it sets `cfg.PermissionMode = ""` but does NOT clear `AllowedTools`, so its expected argv must include `--allowed-tools <default>`.

> **NOTE for the engineer:** `TestEnsure_argv_withModel_noPermissionMode_noName` (cli_test.go:50-69) builds from `config.Default()` and only zeroes `PermissionMode`. After Task 2 the default `AllowedTools` is non-empty, so that test's `want` (line 65) — currently `[]string{"new", "s", "--cwd", "/r", "--effort", "high", "--model", "claude-opus-4-8"}` — must be updated to insert `"--allowed-tools", config.Default().AllowedTools` before `--effort`, OR the test should set `cfg.AllowedTools = ""`. Choose the latter (clearer intent: this test isolates the model/no-mode/no-name path):

```go
	cfg.PermissionMode = ""
	cfg.AllowedTools = ""
	cfg.Effort = "high"
```

- [ ] **Step 7: Commit**

```bash
git add internal/ccpool/cli.go internal/ccpool/cli_test.go
git commit -m "feat(pr-pool): emit --allowed-tools to ccpool new (consumes pg2-sjrl)

CLIRunner carries Config.AllowedTools and emits --allowed-tools after
--permission-mode when non-empty. With the dontAsk default this
constrains every dispatched worker to the conservative allowlist.
Refs pg2-3msk."
```

---

### Task 4: Full verification + repo checks

- [ ] **Step 1: Full pr-pool test + vet**

Run: `cd packages/pr-pool && go test ./... && go vet ./...`
Expected: all PASS. (Check especially `internal/orchestrator` and any integration tests that construct a `CLIRunner` or assert dispatched argv — if one bakes in the old `bypassPermissions` default or a missing `--allowed-tools`, update its expectation to the new default; the failure message will point to the exact line.)

- [ ] **Step 2: Confirm the dependency flag exists end-to-end (pg2-sjrl)**

Run: `cd packages/ccpool && go run ./cmd/ccpool new 2>&1 | grep -- --allowed-tools`
Expected: the usage line lists `--allowed-tools`. (If empty, `pg2-sjrl` is not merged — STOP; this plan's emitted flag would be rejected by `ccpool new` at runtime.)

- [ ] **Step 3: Grep for any other consumer of the old default**

Run: `cd packages/pr-pool && grep -rn "bypassPermissions" . --include='*.go'`
Expected: only `internal/config/config.go` (the `validPermissionModes` map entry + the `Validate` error string) and `internal/config/config_test.go` (the `TestValidate_permissionMode` valid-set list). NO remaining `bypassPermissions` as a *default* or a hard-coded dispatch value. If a non-test file hard-codes it as the launch value, that is a bug — convert it to read `Config.PermissionMode`.

- [ ] **Step 4: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):
```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```
Expected: both PASS. No `gomod2nix.toml` change (no new Go deps). If `nix flake check` rebuilds pr-pool, that is expected (source digest changed); it must still pass.

- [ ] **Step 5: Final review against the bead's acceptance criteria**

Confirm against `pg2-3msk` AC: (a) skip-permissions/permissionless OFF by default — `dontAsk` is the default (Task 1); (b) enabling permissionless is explicit/opt-in — `PR_POOL_PERMISSION_MODE=bypassPermissions` (unchanged escape, verified in Task 1 Step 4); (c) workers run with a constrained allowed-tools set by default — the conservative `Default().AllowedTools` emitted on every `ccpool new` (Tasks 2+3). Do NOT close the bead in this plan (per task instructions; bead changes are out of scope).

---

## Self-review checklist (done while writing)

- **Spec coverage (corrected scope):**
  - (1) flip default `bypassPermissions`→`dontAsk` — Task 1.
  - (2) add `AllowedTools` field + conservative default allowlist — Task 2.
  - (3) plumb through the ccpool seam (`CLIRunner` + `Ensure` emits `--allowed-tools`) — Task 3.
  - `PR_POOL_PERMISSION_MODE` opt-in escape preserved (Task 1 Step 4 verifies the unchanged overlay).
  - Prompt-injection→RCE + why-`dontAsk` rationale — Goal/Architecture sections.
  - Per-entry allowlist rationale + human sign-off flag — Task 2 + the BLOCKING section below.
- **Type consistency:** field name `AllowedTools` is identical across `Config` (config.go), `CLIRunner` (cli.go), and is read via `cfg.AllowedTools`/`c.AllowedTools`. The claude/ccpool flag spelling is `--allowed-tools` everywhere. Emission order in `Ensure` is `--permission-mode` → `--allowed-tools` → `--effort` → `--model`, matching the order baked into the rewritten `TestEnsure_argv`. Env var `PR_POOL_ALLOWED_TOOLS` mirrors the `PR_POOL_PERMISSION_MODE` convention.
- **No placeholders:** every code step shows the literal edit; the default allowlist literal is fully spelled out (and flagged for human sign-off, not left "TBD").
- **Test-default coupling caught:** three existing tests bake in the old default (`config_test.go:44`, `cli_test.go:43`, `cli_test.go:65`) — each is updated in the task that changes the value it asserts.

---

## BLOCKING — human sign-off required before merge (default allowlist contents)

The `Default().AllowedTools` literal (Task 2 Step 4) is the security boundary for every autonomous worker and **must be reviewed and approved by a human before this branch merges.** Proposed default and per-entry rationale:

| Entry | Rationale | Risk if included |
|---|---|---|
| `Read`, `Glob`, `Grep` | Read-only inspection — the worker must read the bead's target files. | None (read-only). |
| `Edit`, `Write` | The worker's core job: implement the change the bead describes. | Can write anywhere in the worktree; bounded by the per-bead worktree (`pg2-yukh` work) + authorship guard. |
| `Bash(git status\|diff\|log\|add\|commit\|checkout\|switch\|branch\|worktree\|rev-parse\|fetch :*)` | The worker resolves a branch, works in a worktree, and commits. **`git push` is deliberately EXCLUDED** — pushing is gated by the authorship preamble and the worker prompt ("push ONLY if the bead says to"); a denied push is the safe default. | `git commit` is local-only; no remote mutation without `push` (excluded). |
| `Bash(bd:*)` | Beads issue tracker — claim/comment/close the bead (the worker's required workflow per `workerPromptBody`). | Mutates the local beads DB only. |
| `Bash(go build\|test\|vet\|mod :*)`, `Bash(gofmt:*)` | Build/test/format the Go change before commit. | Runs project code under test — acceptable for a worker on its own worktree. |
| `Bash(nix flake check\|nix fmt:*)`, `Bash(prek\|pre-commit:*)` | The repo's "before complete" gates (per agent-support CLAUDE.md). | Local checks only. |

**Open questions for the human reviewer (each must be answered before merge):**

1. **`git push` exclusion** — confirmed correct to EXCLUDE? Workers must not push by default (prompt + authorship guard already say so); a denied push is the safe failure. If some roles legitimately push, that should be a per-role override, not the pool default.
2. **`Bash(git *)` granularity** — is the per-subcommand allowlist (above) the right shape, or is a single coarser `Bash(git *)` acceptable? Per-subcommand is tighter (recommended) but more brittle if a worker needs an unlisted git verb; coarse `Bash(git *)` re-opens `git push`/`git config`/`git remote`.
3. **Build-tool breadth** — should `go`/`nix`/`prek` build verbs be in the *pool* default, or only granted per-role to roles that build? Including them pool-wide is convenient but widens the autonomous worker's surface.
4. **Allowlist grammar** — confirm the exact claude `--allowed-tools` matcher syntax for scoped Bash (e.g. `Bash(git commit:*)` vs `Bash(git commit *)`). The roadmap pins `claude --allowed-tools` accepts `"Bash(git *)"`; the per-subcommand `:*` form must be verified against `claude --help`/docs at implementation time — if the `:*` matcher is wrong, fall back to the coarser form the docs confirm and re-flag for sign-off.
5. **MCP / built-in tools not listed** — `TodoWrite`, `WebFetch`, `WebSearch`, `Task`, MCP tools are all OMITTED (auto-denied under `dontAsk`). Confirm no worker role needs any of them; if one does, add it explicitly.

**Recommendation:** ship with the conservative per-subcommand list above; treat any widening as a deliberate, separately-reviewed change.
