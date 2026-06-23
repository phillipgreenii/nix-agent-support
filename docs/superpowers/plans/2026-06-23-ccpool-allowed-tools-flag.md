# ccpool `--allowed-tools` passthrough flag — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-sjrl` (P1, label `ccpool`) — blocks `pg2-3msk`.

**Goal:** Add an `--allowed-tools` flag to `ccpool new` that forwards a tool allowlist to the launched `claude` CLI, mirroring the existing `--permission-mode`/`--effort`/`--model` passthroughs.

**Architecture:** ccpool builds the `claude` argv in one place (`internal/launch`). The flag is a pure passthrough: a string carried on `launch.Spec` → emitted by `appendFlags` → threaded from the CLI through `session.EnsureOpts` into `BuildNew`/`BuildResume`. No validation of the allowlist contents (claude owns the grammar, e.g. `"Bash(git *)"`); empty = omit the flag (zero-value sentinel, same convention as the sibling flags).

**Tech Stack:** Go (stdlib `flag`, `reflect`-based table tests). Repo: `phillipgreenii-nix-agent-support/packages/ccpool`. `claude` accepts `--allowed-tools <tools...>` (comma/space-separated; verified via `claude --help` 2026-06-23).

**Branch:** `ccpool-allowed-tools` (off `main`; simple name per repo convention).

---

### Task 1: `launch.Spec.AllowedTools` + `appendFlags` emits `--allowed-tools`

**Files:**
- Modify: `packages/ccpool/internal/launch/launch.go:43-94`
- Test: `packages/ccpool/internal/launch/launch_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/launch/launch_test.go`:

```go
func TestBuildNew_emitsAllowedToolsAfterPermissionMode(t *testing.T) {
	got := BuildNew(Spec{
		ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p",
		PermissionMode: ModeDontAsk, AllowedTools: "Bash(git *),Edit", Effort: "max",
	})
	want := []string{
		"claude", "--session-id", "u1", "--plugin-dir", "/p",
		"--permission-mode", "dontAsk", "--allowed-tools", "Bash(git *),Edit", "--effort", "max",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestAppendFlags_omitsAllowedToolsWhenEmpty(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p", PermissionMode: ModeDontAsk})
	want := []string{"claude", "--resume", "u1", "--plugin-dir", "/p", "--permission-mode", "dontAsk"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/launch/ -run 'AllowedTools' -v`
Expected: FAIL — `Spec` has no field `AllowedTools` (compile error).

- [ ] **Step 3: Add the field to `Spec`**

In `internal/launch/launch.go`, inside `type Spec struct`, after the `PermissionMode` field (around line 56):

```go
	// AllowedTools emits --allowed-tools <value> when non-empty (a passthrough
	// allowlist forwarded verbatim to claude, e.g. "Bash(git *),Edit"). The zero
	// value omits the flag. Paired with PermissionMode=dontAsk it makes a
	// non-interactive worker auto-DENY any tool outside the list instead of
	// stalling on a permission prompt.
	AllowedTools string
```

- [ ] **Step 4: Emit it in `appendFlags`**

In `internal/launch/launch.go`, update `appendFlags` to insert allowed-tools right after permission-mode (keep the fixed order: permission-mode, allowed-tools, effort, model):

```go
func appendFlags(args []string, s Spec) []string {
	if s.PermissionMode != "" {
		args = append(args, "--permission-mode", string(s.PermissionMode))
	}
	if s.AllowedTools != "" {
		args = append(args, "--allowed-tools", s.AllowedTools)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	return args
}
```

Also update the `appendFlags` doc comment (line 80-82) to mention `--allowed-tools` in the order.

- [ ] **Step 5: Run tests to verify they pass (incl. the existing flag-order tests)**

Run: `cd packages/ccpool && go test ./internal/launch/ -v`
Expected: PASS — new tests pass; `TestBuildLaunchFlags`/`TestBuildNew` still pass (allowed-tools empty → omitted, no order change).

- [ ] **Step 6: Commit**

```bash
git add internal/launch/launch.go internal/launch/launch_test.go
git commit -m "feat(ccpool): emit --allowed-tools passthrough in launch.appendFlags"
```

---

### Task 2: Thread `AllowedTools` through `session.EnsureOpts`

**Files:**
- Modify: `packages/ccpool/internal/session/session.go:143-160` (EnsureOpts) and the three `launch.Spec` construction sites (`:241`, `:268`, `:288`)
- Test: `packages/ccpool/internal/session/session_test.go` (add if a focused unit test fits; otherwise the launch test + CLI test in Task 3 cover behavior)

- [ ] **Step 1: Add the field to `EnsureOpts`**

In `internal/session/session.go`, inside `type EnsureOpts struct`, after the `PermissionMode`/`Effort` fields (around line 158):

```go
	// AllowedTools is forwarded verbatim to launch.Spec.AllowedTools (claude
	// --allowed-tools). Empty omits the flag. Set by pr-pool to constrain a
	// dontAsk worker to an allowlist.
	AllowedTools string
```

- [ ] **Step 2: Pass it at the three `launch.Spec` build sites**

In `internal/session/session.go`, each of the two `launch.BuildResume(launch.Spec{...})` calls (`:241`, `:268`) and the `launch.BuildNew(launch.Spec{...})` call (`:288`) currently sets `PermissionMode: opts.PermissionMode, Effort: opts.Effort`. Add `AllowedTools: opts.AllowedTools` to each:

```go
			PermissionMode: opts.PermissionMode, Effort: opts.Effort, AllowedTools: opts.AllowedTools,
```

(Apply the identical edit at all three sites so new and resume both forward it.)

- [ ] **Step 3: Verify the package compiles + existing tests pass**

Run: `cd packages/ccpool && go test ./internal/session/ -v`
Expected: PASS (no behavior change yet — nothing sets `AllowedTools`).

- [ ] **Step 4: Commit**

```bash
git add internal/session/session.go
git commit -m "feat(ccpool): thread AllowedTools through session.EnsureOpts to launch"
```

---

### Task 3: `ccpool new --allowed-tools` CLI flag

**Files:**
- Modify: `packages/ccpool/cmd/ccpool/new.go:20-85`
- Test: `packages/ccpool/cmd/ccpool/new_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/ccpool/new_test.go` (mirror the existing `TestRunNew_*` style; this asserts the flag parses and is accepted — it does not launch claude):

```go
func TestRunNew_acceptsAllowedToolsFlag(t *testing.T) {
	// --allowed-tools is a free-form passthrough: any value parses (no validation).
	// A missing external_id is the only usage error here; with the id present and
	// the flag set, parsing must succeed past the flag stage.
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	allowed := fs.String("allowed-tools", "", "")
	pos := parseInterspersed(fs, []string{"zr-abc", "--allowed-tools", "Bash(git *),Edit"})
	if len(pos) != 1 || pos[0] != "zr-abc" {
		t.Fatalf("positional parse = %v, want [zr-abc]", pos)
	}
	if *allowed != "Bash(git *),Edit" {
		t.Errorf("allowed-tools = %q, want %q", *allowed, "Bash(git *),Edit")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run AllowedTools -v`
Expected: FAIL (the test compiles against `parseInterspersed`, but to mirror real wiring we add the flag to `runNew` next; if the test passes trivially because it declares its own flagset, also add the wiring in Step 3 and the integration assertion below).

- [ ] **Step 3: Add the flag to `runNew` and pass it through**

In `cmd/ccpool/new.go`, after the `effort` flag (line 28):

```go
	allowedTools := fs.String("allowed-tools", "", "claude --allowed-tools allowlist forwarded verbatim (comma/space-separated, e.g. \"Bash(git *),Edit\"); empty omits the flag")
```

Update the usage string (line 31) to include `[--allowed-tools list]`:

```go
		fmt.Fprintln(os.Stderr, "usage: ccpool new <external_id> [--name label] [--cwd dir] [--model m] [--env KEY=VAL ...] [--permission-mode m] [--allowed-tools list] [--effort v]")
```

In the `session.EnsureOpts{...}` literal passed to `svc.Ensure` (lines 72-77), add:

```go
		AllowedTools: *allowedTools,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ccpool/new.go cmd/ccpool/new_test.go
git commit -m "feat(ccpool): add 'ccpool new --allowed-tools' flag"
```

---

### Task 4: Full verification + bead close

- [ ] **Step 1: Full Go test + vet**

Run: `cd packages/ccpool && go test ./... && go vet ./...`
Expected: all PASS.

- [ ] **Step 2: Manual smoke against the help/spelling**

Run: `cd packages/ccpool && go run ./cmd/ccpool new 2>&1 | grep -- --allowed-tools`
Expected: the usage line lists `--allowed-tools`.
(Optional, if a claude/fake-claude stub is available: `ccpool new zr-tmp --permission-mode dontAsk --allowed-tools "Bash(git *)"` launches without error and the launched argv contains `--allowed-tools "Bash(git *)"`. The repo's `new_integration_test.go` fake-claude harness is the right place if an argv assertion is wanted.)

- [ ] **Step 3: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):
```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```
Expected: both PASS. (No `gomod2nix.toml` change — no new deps were added.)

- [ ] **Step 4: Close the bead**

```bash
bd update pg2-sjrl --claim         # if not already claimed
bd comment pg2-sjrl "Implemented: --allowed-tools passthrough on launch.Spec/appendFlags, threaded via session.EnsureOpts, exposed on 'ccpool new'. Verified flag spelling --allowed-tools against claude --help. Unblocks pg2-3msk."
bd close pg2-sjrl
```

---

## Self-review checklist (done while writing)
- **Spec coverage:** all four AC bullets covered — passthrough to claude (Tasks 1+3), `EnsureOpts.AllowedTools` + `BuildNew`/`BuildResume` emit + omit-when-empty (Tasks 1+2), unit test present+absent (Task 1), no behavior change unset (Task 1 Step 5).
- **Type consistency:** field name `AllowedTools` (Go) ↔ flag `--allowed-tools` (claude) ↔ `*allowedTools` (CLI var) used consistently; `appendFlags` order is permission-mode → allowed-tools → effort → model everywhere.
- **No placeholders:** every code step shows the actual edit.
