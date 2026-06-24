# Autonomous-mode AskUserQuestion stall prevention — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-2f9d` (P3, label `pr-pool`) — D2 RESOLVED. Spans **ccpool** (launch flag + rendered hook behavior) and **pr-pool** (sets the flag + prompt-forbid). Tasks below are labeled by component.

**Goal:** Give autonomous (human-less) pr-pool workers a structural guarantee they never stall on an `AskUserQuestion` picker — via a ccpool **`--autonomous` launch flag** that makes the existing `ccpool hook ask` PreToolUse hook emit a blocking `permissionDecision:"deny"` for that session, PLUS a **mandatory prompt-forbid** lever in the worker prompt so the model treats the denial as "proceed autonomously" rather than "user channel closed."

**Architecture (D2 design — both levers required):**

- ccpool's `AskUserQuestion` detection is wired in ONE static plugin file (`ccpool-plugin/hooks/hooks.json`) shared by every session, invoking `ccpool hook ask`. There is **no per-session hooks.json rendering** — the plugin dir is a single configured path. Therefore the autonomous-vs-attended choice is carried by a **per-session env var** (`CCPOOL_AUTONOMOUS=1`) injected at launch, which the _same_ `ccpool hook ask` command reads.
  - **Attended (default, env unset):** `hook ask` behaves exactly as today — records the `needs_input` edge, fires the notifier, exits 0 NON-blocking, picker renders for a human (`pg2-7a5b` preserved untouched).
  - **Autonomous (`CCPOOL_AUTONOMOUS=1`):** `hook ask` ADDITIONALLY prints the blocking deny JSON (`hookSpecificOutput.permissionDecision:"deny"` + a reason that tells the model it is autonomous) to stdout and exits 0. The tool never executes, no picker appears, the session does not idle, and the model gets the reason as feedback and continues. For autonomous workers the blocking-deny **supersedes** the non-blocking detection for that session; the `needs_input` record is harmless (the picker never blocks).
- The `--autonomous` flag threads through the established passthrough seam: `cmd/ccpool/new.go` → `session.EnsureOpts` → injected into the session env at `launchAndWait` (alongside `CCPOOL_EXTERNAL_ID`/`CCPOOL_POOL`). It is **not** a `claude` argv flag and **not** part of `launch.Spec` — it is a ccpool session-env marker, exactly like `CCPOOL_POOL`.
- pr-pool sets `--autonomous` for workers via the `ccpool.CLIRunner.Ensure` seam (config-driven, `Autonomous bool`) AND adds the prompt-forbid sentence to the worker prompt body (`internal/roles/builtin.go`).

**Why a session-env marker, not a claude flag or a forked plugin dir:** rendering a second plugin dir per session would duplicate the whole `ccpool-plugin` tree and fork the hooks.json contract that `plugin_test.go` pins; a `claude` argv flag can't change a static hook's behavior. The env-var seam reuses the exact mechanism (`launchAndWait` env map) that already carries `CCPOOL_POOL`, and the hook already reads env (`CCPOOL_EXTERNAL_ID`).

**PreToolUse deny contract (verified via claude-code-guide, docs `code.claude.com/docs/en/hooks.md`, claude 2.1.x):**

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "<text>"
  }
}
```

Exit 0 with this JSON on stdout = structured deny: tool does NOT run, no picker, model gets the reason as feedback and continues. (Exit 2 + stderr is the alternative binary-block; we use the JSON/exit-0 form so the reason text is carried explicitly.)

**Tech Stack:** Go (stdlib `flag`, `encoding/json`, table tests). Repos: `phillipgreenii-nix-agent-support/packages/{ccpool,pr-pool}`.

**Branch:** `ccpool-autonomous-deny-ask` (off `main`).

---

## File Structure

| File                                               | Component | Responsibility / change                                                                                                                                     |
| -------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `packages/ccpool/cmd/ccpool/hook.go`               | ccpool    | `handleAskHook` gains an `autonomous bool`; when true ALSO emits deny JSON to a writer. New helper `writeAskDenyJSON`. `runHook` reads `CCPOOL_AUTONOMOUS`. |
| `packages/ccpool/cmd/ccpool/hook_test.go`          | ccpool    | Tests: autonomous emits deny JSON to the writer; attended emits nothing; deny JSON is well-formed with the exact field names.                               |
| `packages/ccpool/internal/session/session.go`      | ccpool    | `EnsureOpts` gains `Autonomous bool`; `launchAndWait` injects `CCPOOL_AUTONOMOUS=1` when set.                                                               |
| `packages/ccpool/internal/session/session_test.go` | ccpool    | Test: the launch env carries `CCPOOL_AUTONOMOUS=1` iff `EnsureOpts.Autonomous`.                                                                             |
| `packages/ccpool/cmd/ccpool/new.go`                | ccpool    | `--autonomous` bool flag → `EnsureOpts.Autonomous`; usage string updated.                                                                                   |
| `packages/ccpool/cmd/ccpool/new_test.go`           | ccpool    | Test: `--autonomous` parses interspersed with the positional id.                                                                                            |
| `packages/pr-pool/internal/config/config.go`       | pr-pool   | `Config.Autonomous bool` (default true for workers) + `PR_POOL_AUTONOMOUS` env overlay.                                                                     |
| `packages/pr-pool/internal/config/config_test.go`  | pr-pool   | Test: default + env overlay of `Autonomous`.                                                                                                                |
| `packages/pr-pool/internal/ccpool/cli.go`          | pr-pool   | `CLIRunner.Autonomous bool`; `Ensure` appends `--autonomous` when set.                                                                                      |
| `packages/pr-pool/internal/ccpool/cli_test.go`     | pr-pool   | Test: `Ensure` argv includes `--autonomous`.                                                                                                                |
| `packages/pr-pool/internal/roles/builtin.go`       | pr-pool   | Append the prompt-forbid sentence to `workerPromptBody`.                                                                                                    |
| `packages/pr-pool/internal/roles/roles_test.go`    | pr-pool   | Test: worker prompt body forbids AskUserQuestion.                                                                                                           |

---

### Task 1 (ccpool): `handleAskHook` emits blocking deny JSON in autonomous mode

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/hook.go` (`runHook` ~line 92, `handleHookN` ~line 108, `handleAskHook` ~line 186)
- Test: `packages/ccpool/cmd/ccpool/hook_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `packages/ccpool/cmd/ccpool/hook_test.go` (the `askPayload` const already exists in this file, claude-2.1.177 shape; reuse it). `handleAskHook` currently takes `(stdin, st, envExternalID, n, on)` — these tests call the NEW signature with a trailing `autonomous bool` and an `io.Writer` for the deny JSON:

```go
func TestHandleAskHook_autonomous_emitsDenyJSON(t *testing.T) {
	st := newTestStore(t) // existing helper used by the other hook_test.go tests
	mustSeedRow(t, st, "csid-x", "zr-1") // existing helper: row resolvable by claude_session_id
	var out bytes.Buffer
	if err := handleAskHook(strings.NewReader(askPayload), st, "", notify.None{}, nil, true, &out); err != nil {
		t.Fatalf("handleAskHook: %v", err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("deny JSON malformed: %v\nraw: %s", err, out.String())
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", got.HookSpecificOutput.HookEventName)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("permissionDecision = %q, want deny", got.HookSpecificOutput.PermissionDecision)
	}
	if got.HookSpecificOutput.PermissionDecisionReason == "" {
		t.Error("permissionDecisionReason must be non-empty (the autonomous-mode guidance)")
	}
}

func TestHandleAskHook_attended_emitsNoDenyJSON(t *testing.T) {
	st := newTestStore(t)
	mustSeedRow(t, st, "csid-x", "zr-1")
	var out bytes.Buffer
	if err := handleAskHook(strings.NewReader(askPayload), st, "", notify.None{}, nil, false, &out); err != nil {
		t.Fatalf("handleAskHook: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("attended mode must emit NO stdout (non-blocking detection only); got %q", out.String())
	}
}
```

> **Note for the implementer:** confirm the exact names of the existing store-seed helpers in `hook_test.go` (the file already constructs a store and seeds a resolvable row for the `ask` tests — `grep -n "func newTestStore\|Upsert\|Insert" packages/ccpool/cmd/ccpool/hook_test.go`). Use whatever the existing `ask` tests use (e.g. `TestHandleAskHook_*` setup) verbatim; do not invent new helpers. If the existing tests seed inline, copy that inline setup into these two tests.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'HandleAskHook_(autonomous|attended)' -v`
Expected: FAIL — `handleAskHook` has 5 params, the calls pass 7 (compile error: too many arguments).

- [ ] **Step 3: Add the deny-JSON helper**

In `packages/ccpool/cmd/ccpool/hook.go`, add (near `askQuestionText`, after line 241):

```go
// askDenyReason is the autonomous-mode denial fed back to the model. It MUST tell
// the model the channel is intentionally closed (autonomous mode) and what to do
// instead — a BARE denial can read as "user went away" and make the model give up
// (D2 caveat). Pairs with pr-pool's prompt-forbid lever.
const askDenyReason = "Autonomous mode: AskUserQuestion is disabled — there is no human to answer. Do NOT ask; proceed with your best judgment. If a decision genuinely needs a human, record the question with `bd comment` on your bead and continue or hand the bead back."

// writeAskDenyJSON emits the PreToolUse structured-deny payload (claude hooks
// spec: code.claude.com/docs/en/hooks.md). Printed to stdout with exit 0, it
// makes claude skip the tool, show no picker, and feed the reason back to the
// model so it continues the turn. Best-effort: an encode error must not fail the
// hook (never-fail policy, spec §9/§15).
func writeAskDenyJSON(w io.Writer) {
	type hookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	}
	payload := struct {
		HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
	}{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "deny",
		PermissionDecisionReason: askDenyReason,
	}}
	_ = json.NewEncoder(w).Encode(payload)
}
```

- [ ] **Step 4: Thread `autonomous` + writer into `handleAskHook`**

In `packages/ccpool/cmd/ccpool/hook.go`, change the `handleAskHook` signature (line 186) and emit the deny JSON when autonomous, AFTER the existing detection work so the `needs_input` record is unchanged:

```go
func handleAskHook(stdin io.Reader, st *store.Store, envExternalID string, n notify.Notifier, on []string, autonomous bool, denyOut io.Writer) error {
	var p hookPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.ToolName != "" && p.ToolName != "AskUserQuestion" {
		return nil
	}
	ctx := context.Background()
	externalID, ok, err := resolveExternalID(ctx, st, p.SessionID, envExternalID)
	if err != nil {
		return err
	}
	if !ok {
		// Even with no resolvable row, an autonomous session must still be UNBLOCKED:
		// emit the deny so the picker never stalls a human-less worker.
		if autonomous {
			writeAskDenyJSON(denyOut)
		}
		return nil
	}
	prior, err := st.Transition(ctx, externalID, store.NeedsInput, p.SessionID, p.TranscriptPath)
	if err != nil {
		return fmt.Errorf("transition %q: %w", externalID, err)
	}
	if err := st.SetPendingQuestion(ctx, externalID, askQuestionText(p.ToolInput)); err != nil {
		return fmt.Errorf("set pending question %q: %w", externalID, err)
	}
	if notify.ShouldNotify(on, string(prior), string(store.NeedsInput)) {
		_ = n.Notify(notify.Event{Name: externalID, UUID: p.SessionID, State: string(store.NeedsInput), CWD: p.CWD})
	}
	// Autonomous mode: the blocking deny SUPERSEDES the non-blocking detection for
	// this session (the record above is harmless — the picker never blocks because
	// the tool never executes). Attended sessions skip this and keep today's
	// non-blocking behavior (pg2-7a5b).
	if autonomous {
		writeAskDenyJSON(denyOut)
	}
	return nil
}
```

Update the doc comment above `handleAskHook` (lines 180-185) to note the autonomous branch.

- [ ] **Step 5: Pass `autonomous` + stdout through `handleHookN` and `runHook`**

In `packages/ccpool/cmd/ccpool/hook.go`:

Change `handleHookN` (line 108) to accept and forward the two new args:

```go
func handleHookN(event string, stdin io.Reader, st *store.Store, envExternalID string, n notify.Notifier, on []string, ra *retryActuator, autonomous bool, denyOut io.Writer) error {
	if event == "ask" {
		return handleAskHook(stdin, st, envExternalID, n, on, autonomous, denyOut)
	}
	// ... unchanged ...
```

Change `handleHook` (line 101, the test convenience wrapper) to keep compiling — pass attended defaults:

```go
func handleHook(event string, stdin io.Reader, st *store.Store, envExternalID string) error {
	return handleHookN(event, stdin, st, envExternalID, notify.None{}, nil, nil, false, io.Discard)
}
```

In `runHook` (line 92), read the env marker and pass `os.Stdout` as the deny writer:

```go
	autonomous := os.Getenv("CCPOOL_AUTONOMOUS") == "1"
	if err := handleHookN(event, os.Stdin, st, os.Getenv("CCPOOL_EXTERNAL_ID"), n, cfg.Notify.On, ra, autonomous, os.Stdout); err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: %v", event, err))
	}
```

(`io` is already imported in `hook.go`; `os` is too.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'HandleAskHook' -v`
Expected: PASS — both new tests green; the existing `ask`/notify tests still pass (they call `handleHook` or the attended path → `autonomous=false`, no stdout).

- [ ] **Step 7: Run the full cmd/ccpool package to confirm no signature breakage**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -v`
Expected: PASS — every existing caller of `handleHookN`/`handleHook` compiles against the new arity (the wrapper absorbed the change).

- [ ] **Step 8: Commit**

```bash
git add packages/ccpool/cmd/ccpool/hook.go packages/ccpool/cmd/ccpool/hook_test.go
git commit -m "feat(ccpool): autonomous-mode AskUserQuestion blocking deny in hook ask"
```

---

### Task 2 (ccpool): inject `CCPOOL_AUTONOMOUS` into the session env

**Files:**

- Modify: `packages/ccpool/internal/session/session.go` (`EnsureOpts` ~line 143-160; `launchAndWait` ~line 343-355; the three `EnsureOpts`-consuming `launch.Spec` sites are unchanged — autonomous is NOT a claude flag)
- Test: `packages/ccpool/internal/session/session_test.go`

- [ ] **Step 1: Write the failing test**

Add to `packages/ccpool/internal/session/session_test.go`. The package's existing tests inject a fake `Tmux` whose `NewSession(name, cwd, env, argv)` captures `env` — find it (`grep -n "func.*NewSession\|fakeTmux\|capturedEnv\|gotEnv" packages/ccpool/internal/session/session_test.go`) and reuse that fake. The test drives a brand-new `Ensure` with `Autonomous: true` and asserts the captured env:

```go
func TestEnsure_autonomous_injectsCCPOOLAutonomousEnv(t *testing.T) {
	svc, fake := newTestService(t) // existing constructor used by other Ensure tests
	_, err := svc.Ensure(context.Background(), "zr-1", t.TempDir(), "", EnsureOpts{Autonomous: true})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := fake.lastEnv["CCPOOL_AUTONOMOUS"]; got != "1" {
		t.Errorf("CCPOOL_AUTONOMOUS = %q, want \"1\"", got)
	}
}

func TestEnsure_attended_omitsCCPOOLAutonomousEnv(t *testing.T) {
	svc, fake := newTestService(t)
	_, err := svc.Ensure(context.Background(), "zr-1", t.TempDir(), "", EnsureOpts{}) // Autonomous false
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, present := fake.lastEnv["CCPOOL_AUTONOMOUS"]; present {
		t.Errorf("attended launch must not set CCPOOL_AUTONOMOUS; env = %v", fake.lastEnv)
	}
}
```

> **Note for the implementer:** `newTestService`/`fake.lastEnv` are placeholders — use the EXACT fake-Tmux constructor and captured-env field the existing `Ensure` tests in this file use. If those tests assert on argv rather than env, add an env capture to the existing fake (it already receives `env` as a `NewSession` param). Keep the assertion: env has/omits `CCPOOL_AUTONOMOUS=1`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/session/ -run 'Ensure_(autonomous|attended)' -v`
Expected: FAIL — `EnsureOpts` has no field `Autonomous` (compile error).

- [ ] **Step 3: Add the field to `EnsureOpts`**

In `packages/ccpool/internal/session/session.go`, inside `type EnsureOpts struct`, after `Effort` (around line 159):

```go
	// Autonomous, when true, injects CCPOOL_AUTONOMOUS=1 into the session env so
	// the `ccpool hook ask` PreToolUse hook BLOCKS AskUserQuestion (emits a deny)
	// instead of only recording the needs_input edge. Set by pr-pool for human-less
	// workers; unset for attended sessions (which keep pg2-7a5b detection).
	Autonomous bool
```

- [ ] **Step 4: Inject the env in `launchAndWait`**

In `packages/ccpool/internal/session/session.go`, `launchAndWait` builds the `env` map (lines 344-355). The function signature does NOT currently carry `opts`; thread the flag in. Two equivalent options — pick the one that matches the existing call shape:

Preferred (minimal): add a parameter. Change the signature (line 343) and the three call sites (`:246`, `:273`, `:292`) to pass `opts.Autonomous`:

```go
func (s *Service) launchAndWait(ctx context.Context, externalID, tmuxName, csid, name, cwd string, since int64, argv []string, extraEnv map[string]string, autonomous bool) (Handle, error) {
	env := make(map[string]string, len(extraEnv)+len(claudeChildMarkers)+4)
	maps.Copy(env, extraEnv)
	env["CCPOOL_EXTERNAL_ID"] = externalID
	env["PA_MONITOR_NO_NUDGE"] = "1"
	if s.d.PoolPath != "" {
		env["CCPOOL_POOL"] = s.d.PoolPath
	}
	if autonomous {
		env["CCPOOL_AUTONOMOUS"] = "1"
	}
	for _, k := range claudeChildMarkers {
		env[k] = ""
	}
	// ... unchanged ...
```

At each of the three `return s.launchAndWait(...)` call sites (resume at `:246` and `:273`, new at `:292`), append `, opts.Autonomous` as the final argument.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/session/ -v`
Expected: PASS — new tests green; all existing `Ensure`/launch tests still pass (Autonomous false → no env key, no behavior change).

- [ ] **Step 6: Commit**

```bash
git add packages/ccpool/internal/session/session.go packages/ccpool/internal/session/session_test.go
git commit -m "feat(ccpool): EnsureOpts.Autonomous injects CCPOOL_AUTONOMOUS env at launch"
```

---

### Task 3 (ccpool): `ccpool new --autonomous` flag

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/new.go` (flag block lines 22-28; usage line 31; `EnsureOpts` literal lines 72-77)
- Test: `packages/ccpool/cmd/ccpool/new_test.go`

- [ ] **Step 1: Write the failing test**

Add to `packages/ccpool/cmd/ccpool/new_test.go` (mirror `TestEnvFlag_*` style; asserts the flag parses interspersed with the positional id via `parseInterspersed`, defined in `cmd/ccpool/args.go`):

```go
func TestRunNew_parsesAutonomousFlag(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	autonomous := fs.Bool("autonomous", false, "")
	pos := parseInterspersed(fs, []string{"zr-1", "--autonomous"})
	if len(pos) != 1 || pos[0] != "zr-1" {
		t.Fatalf("positional parse = %v, want [zr-1]", pos)
	}
	if !*autonomous {
		t.Error("--autonomous should parse to true")
	}
}

func TestRunNew_autonomousDefaultsFalse(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	autonomous := fs.Bool("autonomous", false, "")
	_ = parseInterspersed(fs, []string{"zr-1"})
	if *autonomous {
		t.Error("--autonomous must default to false (attended)")
	}
}
```

(`flag` must be imported in `new_test.go` — add it; `reflect` and `launch` are already imported there.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'RunNew_(parsesAutonomous|autonomousDefaults)' -v`
Expected: FAIL — `flag` not imported yet (compile error) or, once imported, the test passes trivially against its own flagset. Either way, Step 3 wires the REAL flag into `runNew` so production behavior matches; the integration check in Task 5 Step 2 verifies the usage string.

- [ ] **Step 3: Add the flag to `runNew` and pass it through**

In `packages/ccpool/cmd/ccpool/new.go`, after the `effort` flag (line 28):

```go
	autonomous := fs.Bool("autonomous", false, "autonomous mode: block AskUserQuestion (the hook denies it so a human-less worker never stalls on the picker); injects CCPOOL_AUTONOMOUS into the session")
```

Update the usage string (line 31) to include `[--autonomous]`:

```go
		fmt.Fprintln(os.Stderr, "usage: ccpool new <external_id> [--name label] [--cwd dir] [--model m] [--env KEY=VAL ...] [--permission-mode m] [--effort v] [--autonomous]")
```

In the `session.EnsureOpts{...}` literal (lines 72-77), add:

```go
		Autonomous: *autonomous,
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/ccpool/cmd/ccpool/new.go packages/ccpool/cmd/ccpool/new_test.go
git commit -m "feat(ccpool): add 'ccpool new --autonomous' flag"
```

---

### Task 4 (pr-pool): config `Autonomous` + `--autonomous` passthrough + prompt-forbid

**Files:**

- Modify: `packages/pr-pool/internal/config/config.go` (`Config` struct ~line 37, `Default()` ~line 80, `Load()` env overlay ~line 113)
- Modify: `packages/pr-pool/internal/ccpool/cli.go` (`CLIRunner` ~line 39-48, `NewCLIRunner` ~line 50-56, `Ensure` ~line 111-135)
- Modify: `packages/pr-pool/internal/roles/builtin.go` (`workerPromptBody` line 26)
- Test: `packages/pr-pool/internal/config/config_test.go`, `packages/pr-pool/internal/ccpool/cli_test.go`, `packages/pr-pool/internal/roles/roles_test.go`

- [ ] **Step 1: Write the failing config test**

Add to `packages/pr-pool/internal/config/config_test.go` (find the existing env-overlay test, e.g. `grep -n "PR_POOL_PERMISSION_MODE\|t.Setenv\|func TestLoad" packages/pr-pool/internal/config/config_test.go`, and mirror its `t.Setenv` style):

```go
func TestDefault_autonomousTrue(t *testing.T) {
	if !Default().Autonomous {
		t.Error("Default().Autonomous should be true (workers are human-less by default)")
	}
}

func TestLoad_autonomousEnvOverlay(t *testing.T) {
	t.Setenv("PR_POOL_AUTONOMOUS", "false")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Autonomous {
		t.Error("PR_POOL_AUTONOMOUS=false should disable autonomous")
	}
}
```

> **Note for the implementer:** confirm whether the package already has a bool env helper (`grep -n "func envBool\|func envStr\|ParseBool" packages/pr-pool/internal/config/config.go`). If `envBool` exists, use it in Step 3; if not, add a tiny one mirroring `envStr` (shown in Step 3).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'autonomous|Autonomous' -v`
Expected: FAIL — `Config` has no field `Autonomous` (compile error).

- [ ] **Step 3: Add the config field, default, and env overlay**

In `packages/pr-pool/internal/config/config.go`, add to the `Config` struct after `PermissionMode` (line 37):

```go
	// Autonomous, when true, passes `--autonomous` to `ccpool new` so workers'
	// AskUserQuestion is structurally blocked (no human to answer). Default true.
	Autonomous bool
```

In `Default()` (after line 80, `PermissionMode: "bypassPermissions",`):

```go
		Autonomous:     true,
```

In `Load()` (after line 113, the `PR_POOL_PERMISSION_MODE` overlay):

```go
	c.Autonomous = envBool("PR_POOL_AUTONOMOUS", c.Autonomous)
```

If `envBool` does not already exist in the package, add it next to `envStr`:

```go
// envBool overlays a bool from env: "false"/"0"/"no" → false, "true"/"1"/"yes" →
// true; an unset or unparseable value keeps def.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
```

(Add `"strconv"` to the imports if not present.)

- [ ] **Step 4: Run the config test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'autonomous|Autonomous' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing CLI argv test**

Add to `packages/pr-pool/internal/ccpool/cli_test.go` (mirror `TestEnsure_argv` exactly — note `Default()` now sets `Autonomous=true`, so the spy default-config Ensure should emit `--autonomous`):

```go
func TestEnsure_argv_includesAutonomous(t *testing.T) {
	cli, got, _ := newSpy() // config.Default() => Autonomous true, PermissionMode bypassPermissions, Effort max
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--permission-mode", "bypassPermissions", "--effort", "max", "--autonomous"}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("argv =\n %v\nwant\n %v", (*got)[0], want)
	}
}

func TestEnsure_argv_omitsAutonomousWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Autonomous = false
	var got [][]string
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range got[0] {
		if a == "--autonomous" {
			t.Fatalf("--autonomous must be omitted when Autonomous=false; argv = %v", got[0])
		}
	}
}
```

> **Implementer caution:** adding `Autonomous` to `Default()` changes the argv produced by the EXISTING `TestEnsure_argv` (the `newSpy()` one at cli_test.go:29). That test's `want` (cli_test.go:37-44) must gain a trailing `"--autonomous"` or it will break. Update it in this step (it is part of the same surface). `TestEnsure_argv_withModel_noPermissionMode_noName` sets up its own `cfg := config.Default()` and does NOT override `Autonomous`, so it ALSO needs `--autonomous` appended to its `want` (cli_test.go:65) — OR set `cfg.Autonomous = false` in that test to keep it focused on model/permission. Choose explicitly; do not leave a stale `want`.

- [ ] **Step 6: Run the CLI test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -run 'Ensure_argv' -v`
Expected: FAIL — `CLIRunner` has no `Autonomous` field; `Ensure` does not emit `--autonomous`.

- [ ] **Step 7: Add `Autonomous` to `CLIRunner` and emit `--autonomous` in `Ensure`**

In `packages/pr-pool/internal/ccpool/cli.go`, add to the `CLIRunner` struct after `PermissionMode` (line 42):

```go
	Autonomous     bool   // emits --autonomous on `ccpool new` when true (block AskUserQuestion)
```

In `NewCLIRunner` (line 51), set it from config:

```go
	c := &CLIRunner{Effort: cfg.Effort, Model: cfg.Model, PermissionMode: cfg.PermissionMode, Autonomous: cfg.Autonomous, bin: "ccpool"}
```

In `Ensure`, append `--autonomous` after the `--model` block (after line 132, before the `c.ccpool(...)` call). The flag is a bool toggle with no value, placed last to mirror `ccpool new`'s own ordering:

```go
	if c.Autonomous {
		args = append(args, "--autonomous")
	}
```

- [ ] **Step 8: Run the CLI tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -v`
Expected: PASS — new tests green; the updated `TestEnsure_argv`/`TestEnsure_argv_withModel_*` `want` slices match.

- [ ] **Step 9: Write the failing prompt-forbid test**

Add to `packages/pr-pool/internal/roles/roles_test.go` (mirror `TestBuiltinWorkerPrompt_taskBodyHasNoRails` at line 35):

```go
func TestBuiltinWorkerPrompt_forbidsAskUserQuestion(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	body := rs[1].CCPool.PromptBody
	if !strings.Contains(body, "AskUserQuestion") {
		t.Fatalf("worker prompt must explicitly name and forbid AskUserQuestion (D2 prompt-forbid lever); body:\n%s", body)
	}
}
```

- [ ] **Step 10: Run the prompt test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/roles/ -run forbidsAskUserQuestion -v`
Expected: FAIL — `workerPromptBody` does not mention `AskUserQuestion`.

- [ ] **Step 11: Append the prompt-forbid sentence to `workerPromptBody`**

In `packages/pr-pool/internal/roles/builtin.go`, append to the END of the `workerPromptBody` string (line 26), inside the backticks, after the existing final sentence (`...do not push by default.`):

```
 You are running autonomously with no human available: do NOT use the AskUserQuestion tool — it is disabled and will be denied. If you would otherwise ask, instead proceed with your best judgment; if a decision genuinely needs a human, record it with bd comment on the bead and continue or hand the bead back.
```

(Leading space joins it to the prior sentence; keep it one continuous backtick string.)

- [ ] **Step 12: Run the prompt test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/roles/ -v`
Expected: PASS — `forbidsAskUserQuestion` green; `TestBuiltinWorkerPrompt_taskBodyHasNoRails` still green (the appended text contains neither `phillipg.` nor `git push --force`).

- [ ] **Step 13: Update the serialized-default golden if one exists**

The worker prompt body is also serialized by `config/example.go` (the `[[role]]` TOML default). Check for a golden/round-trip test that pins the exact prompt text:

Run: `cd packages/pr-pool && go test ./internal/config/ -v 2>&1 | grep -i -E "fail|prompt|example|roundtrip" || true`
If a test fails because the embedded prompt changed, regenerate/update its expectation (the round-trip test asserts `Default()` re-parses identically, not exact bytes; if it's a byte-golden, update the golden). Expected after fix: PASS.

- [ ] **Step 14: Commit**

```bash
git add packages/pr-pool/internal/config/config.go packages/pr-pool/internal/config/config_test.go \
        packages/pr-pool/internal/ccpool/cli.go packages/pr-pool/internal/ccpool/cli_test.go \
        packages/pr-pool/internal/roles/builtin.go packages/pr-pool/internal/roles/roles_test.go
git commit -m "feat(pr-pool): set ccpool --autonomous for workers + forbid AskUserQuestion in worker prompt"
```

---

### Task 5: Full verification + repo checks

- [ ] **Step 1: Full Go test + vet for both packages**

Run:

```bash
cd packages/ccpool && go test ./... && go vet ./...
cd ../pr-pool && go test ./... && go vet ./...
```

Expected: all PASS. (No new third-party deps were added, so no `gomod2nix.toml` change in either package.)

- [ ] **Step 2: Manual smoke — `ccpool new` help lists the flag**

Run: `cd packages/ccpool && go run ./cmd/ccpool new 2>&1 | grep -- --autonomous`
Expected: the usage line lists `--autonomous`.

- [ ] **Step 3: Manual smoke — pr-pool worker dispatch emits `--autonomous`**

This is covered deterministically by `TestEnsure_argv_includesAutonomous` (Task 4). No live worker run required. (Optional live check is in the verification-gaps note below.)

- [ ] **Step 4: Confirm the attended-detection contract is untouched (`pg2-7a5b`)**

Run:

```bash
cd packages/ccpool && go test ./cmd/ccpool/ -run 'Plugin|NeedsInput|Ask|Notify' -v
```

Expected: PASS — `TestPluginHooksJSON_hasWrapperAndAllEvents` (the hooks.json still wires `PreToolUse`/`AskUserQuestion`→`ccpool hook ask`, unchanged), and the contract/notify tests for non-blocking detection still pass. **The hooks.json file is intentionally NOT modified by this plan.**

- [ ] **Step 5: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):

```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```

Expected: both PASS. (Per-source content digests rebuild only the two changed Go packages; no `vendorHash`/`gomod2nix.toml` churn since no deps changed.)

- [ ] **Step 6: Close the bead**

```bash
bd update pg2-2f9d --claim   # if not already claimed
bd comment pg2-2f9d "Implemented D2: ccpool --autonomous launch flag injects CCPOOL_AUTONOMOUS=1; 'ccpool hook ask' emits a PreToolUse permissionDecision:deny (hookSpecificOutput, exit 0) in autonomous mode (SUPERSEDES non-blocking detection for that session) and is unchanged for attended sessions (pg2-7a5b intact — hooks.json untouched). pr-pool sets --autonomous for workers (config.Autonomous default true, PR_POOL_AUTONOMOUS overlay) AND the mandatory prompt-forbid sentence forbids AskUserQuestion in the worker prompt. Deny reason tells the model it's autonomous (proceed / bd comment) per the D2 caveat. Verified deny JSON field names against claude hooks docs."
bd close pg2-2f9d
```

---

## Live-verification gap (cannot be fully closed by a subagent)

- AC "autonomous workers no longer stall indefinitely on an AskUserQuestion picker" is fully exercised by deterministic unit tests (hook emits the exact deny JSON; the flag/env/prompt all wire through). A true end-to-end confirmation — a live worker that _attempts_ AskUserQuestion, is denied, and continues without idling — needs a running pool and is flagged for the operator. The deterministic tests plus the verified deny-JSON contract (claude-code-guide, docs `code.claude.com/docs/en/hooks.md`) substitute for the live repro.

---

## Self-review checklist (done while writing)

- **Spec coverage (3 ACs):**
  1. _Autonomous workers no longer stall on AskUserQuestion_ — ccpool blocking-deny (Task 1) + flag/env wiring (Tasks 2-3) + pr-pool sets it (Task 4). ✅
  2. _Chosen approach implemented (prompt-forbid AND/OR blocking hook)_ — BOTH levers per D2: blocking hook (Task 1) AND mandatory prompt-forbid (Task 4 Steps 9-12). ✅
  3. _Does not break ccpool's non-blocking needs_input detection (pg2-7a5b)_ — attended path (env unset) is byte-for-byte the old behavior; hooks.json is NOT modified; verified by Task 5 Step 4 + the attended-emits-no-JSON test (Task 1). ✅
- **D2 caveat coverage:** the deny `permissionDecisionReason` is NOT bare — it states "autonomous mode, proceed / bd comment," and is paired with the prompt-forbid lever (both mandatory). ✅
- **Coexist/supersede:** the SAME static `ccpool hook ask` command branches on `CCPOOL_AUTONOMOUS`; autonomous = detection-record (harmless) + blocking-deny (supersedes); attended = detection only. No per-session hooks.json fork, no plugin-dir duplication. ✅
- **Placeholder scan:** the only intentional non-literals are test-helper names (`newTestStore`/`mustSeedRow`/`newTestService`/`fake.lastEnv`/`newSpy`) flagged with explicit "use the existing helper, grep here" implementer notes, because the exact helper names live in test files not fully read — every PRODUCTION edit is a literal. ✅
- **Type consistency:** `Autonomous bool` is the field name in `EnsureOpts` (ccpool), `Config` (pr-pool), and `CLIRunner` (pr-pool); the env marker is `CCPOOL_AUTONOMOUS` everywhere; the CLI flag is `--autonomous` (ccpool `new` AND pr-pool `Ensure` argv); the deny JSON fields are `hookSpecificOutput`/`hookEventName`/`permissionDecision`/`permissionDecisionReason` (verified casing). `handleAskHook` new params are `(autonomous bool, denyOut io.Writer)` consistently across the signature, the `handleHookN` forward, and both test calls. ✅
- **No claude-flag confusion:** `--autonomous` is a ccpool/pr-pool flag and a session env marker; it is deliberately NOT added to `launch.Spec`/`appendFlags` (it is not a `claude` argv flag), distinguishing it from the `--allowed-tools`/`--permission-mode` passthroughs. ✅
