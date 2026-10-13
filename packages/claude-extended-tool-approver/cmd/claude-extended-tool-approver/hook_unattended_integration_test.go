//go:build integration

// Hook-mode tests for the "an unattended session never asks" policy: they exec the
// COMPILED hook binary (testBinary) with a real PreToolUse payload on stdin and read
// what it prints on stdout, exactly as Claude Code does — no mock of the approver and
// no in-process shortcut, so the whole path (payload parse, engine, the unattended
// policy, the output encoder, the ask log) is exercised.
//
// THE DEFECT THESE GUARD. A hook that prints permissionDecision "ask" opens a
// permission dialog even when the session runs in permission mode dontAsk, and in a
// pool worker nobody is watching: the session sits at the dialog until a person
// attaches. Seen five times in a day on real workers (the transcripts of two of them
// record this hook's own `ask` output): twice for a credential-path read
// (`cat .envrc`, `git ls-files -- .env.yarn`) from the `secrets` rule, once for a
// literal `PATH=` replacement from the `env-vars` rule. The expected outcome for an
// unattended session is a deterministic `deny` whose reason tells the model what to
// do instead; an attended session must still be asked exactly as before.
//
// HERMETIC BY CONSTRUCTION. Every run gets an environment rebuilt from an allowlist
// (PATH and a few locale variables) plus a throwaway HOME/XDG tree, so nothing the
// machine that runs the suite carries — a pool worker has CCPOOL_AUTONOMOUS=1,
// BEADS_*, a real ~/.claude/settings.json deny-list — can change a verdict. The one
// ambient-session variable a test depends on is set explicitly by that test.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// preToolUsePayload is the shape Claude Code sends a PreToolUse hook (documented hook
// input; the field names are hookio.HookInput's own json tags).
type preToolUsePayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

func bashPayload(cwd, mode, command string) preToolUsePayload {
	ti, _ := json.Marshal(map[string]string{"command": command})
	return preToolUsePayload{
		SessionID: "sess-unattended-test", TranscriptPath: filepath.Join(cwd, "transcript.jsonl"), CWD: cwd,
		PermissionMode: mode, HookEventName: "PreToolUse", ToolName: "Bash", ToolInput: ti, ToolUseID: "toolu_unattended_test",
	}
}

func readToolPayload(cwd, mode, path string) preToolUsePayload {
	ti, _ := json.Marshal(map[string]string{"file_path": path})
	p := bashPayload(cwd, mode, "")
	p.ToolName, p.ToolInput = "Read", ti
	return p
}

// hermeticEnv rebuilds the child environment from an allowlist and a throwaway
// HOME/XDG tree, then appends the test's explicit overrides.
func hermeticEnv(t *testing.T, overrides ...string) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMPDIR=" + os.TempDir(),
	}
	// PATH is needed to find git/gh; the locale keeps parsing byte-stable.
	for _, name := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return append(env, overrides...)
}

type hookRun struct {
	out    map[string]any
	stdout string
	stderr string
}

func (r hookRun) decision() string {
	hso, _ := r.out["hookSpecificOutput"].(map[string]any)
	d, _ := hso["permissionDecision"].(string)
	return d
}

func (r hookRun) reason() string {
	hso, _ := r.out["hookSpecificOutput"].(map[string]any)
	s, _ := hso["permissionDecisionReason"].(string)
	return s
}

// runPreToolUse execs the compiled hook with payload on stdin and the hermetic
// environment, and logs both streams so a failure shows exactly what was emitted.
func runPreToolUse(t *testing.T, payload preToolUsePayload, envOverrides ...string) hookRun {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cmd := exec.Command(testBinary(t))
	cmd.Env = hermeticEnv(t, envOverrides...)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook exited with error: %v\nstderr: %s", err, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); err != nil {
		t.Fatalf("hook stdout is not one JSON object: %v\nstdout: %s", err, stdout.String())
	}
	t.Logf("mode=%q command/tool=%s payload=%s\n  stdout: %s", payload.PermissionMode, payload.ToolName, body, strings.TrimSpace(stdout.String()))
	return hookRun{out: out, stdout: stdout.String(), stderr: stderr.String()}
}

const (
	// A neutral absolute path: the classifier is lexical, so nothing here is opened.
	secretCWD      = "/srv/work/project"
	envrcRead      = "cat /srv/work/project/.envrc"
	pathReplace    = `bgrun job -- nice -n 10 env CGO_ENABLED=0 PATH=/opt/toolchain/go/bin:/usr/bin:/bin go -C /srv/work/pkg test -p 4 -count=1 -v -run "TestA|TestB" ./internal/x/ .`
	denyReasonHead = "unattended session never asks"
)

// TestUnattendedHook_SecretPathReadIsADeterministicDeny is the acceptance case: under
// dontAsk, a credential-path read gets a tool_result-producing denial straight from
// the hook — never an `ask` — and the reason tells the model what to do instead.
func TestUnattendedHook_SecretPathReadIsADeterministicDeny(t *testing.T) {
	cases := map[string]preToolUsePayload{
		"cat .envrc":                        bashPayload(secretCWD, "dontAsk", envrcRead),
		"cat .env.yarn":                     bashPayload(secretCWD, "dontAsk", "cat /srv/work/project/.env.yarn"),
		"git ls-files with .env.yarn paths": bashPayload(secretCWD, "dontAsk", "git ls-files -- .env.yarn */.env.yarn | head"),
		"Read tool on .envrc":               readToolPayload(secretCWD, "dontAsk", "/srv/work/project/.envrc"),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPreToolUse(t, payload)
			if got := run.decision(); got != "deny" {
				t.Fatalf("permissionDecision = %q, want deny: an unattended worker must never get an ask (stdout: %s)", got, run.stdout)
			}
			reason := run.reason()
			for _, want := range []string{denyReasonHead, "permission mode dontAsk", "credential/secret path", "Do not read credential or environment files", "search /nix/store for a toolchain", "report it as `stuck` naming the missing tool", "orchestrator"} {
				if !strings.Contains(reason, want) {
					t.Errorf("reason missing %q:\n%s", want, reason)
				}
			}
			if strings.Contains(reason, "expected on PATH") {
				t.Errorf("reason must not claim what is on PATH (false on the pool today):\n%s", reason)
			}
		})
	}
}

// TestUnattendedHook_AutonomousEnvAloneIsEnough: ccpool's launch-injected
// CCPOOL_AUTONOMOUS=1 marks a worker unattended whatever its permission mode, so a
// pool launched outside dontAsk is covered too. Only the exact value "1" counts.
func TestUnattendedHook_AutonomousEnvAloneIsEnough(t *testing.T) {
	run := runPreToolUse(t, bashPayload(secretCWD, "default", envrcRead), hookio.AutonomousEnvVar+"=1")
	if got := run.decision(); got != "deny" {
		t.Fatalf("with %s=1 permissionDecision = %q, want deny (stdout: %s)", hookio.AutonomousEnvVar, got, run.stdout)
	}
	if !strings.Contains(run.reason(), "autonomous worker ("+hookio.AutonomousEnvVar+"=1)") {
		t.Errorf("reason does not name the autonomous signal:\n%s", run.reason())
	}

	for _, v := range []string{"0", "true", ""} {
		run := runPreToolUse(t, bashPayload(secretCWD, "default", envrcRead), hookio.AutonomousEnvVar+"="+v)
		if got := run.decision(); got != "ask" {
			t.Errorf("%s=%q: permissionDecision = %q, want ask (only \"1\" is the contract)", hookio.AutonomousEnvVar, v, got)
		}
	}
}

// TestUnattendedHook_AskProducingRuleBecomesADeny: the policy is not specific to the
// secrets rule. The literal PATH replacement that stalled a real drain worker is an
// `ask` from the env-vars rule when a human is there (the control), and a deny that
// says how to prepend instead when none is.
func TestUnattendedHook_AskProducingRuleBecomesADeny(t *testing.T) {
	attended := runPreToolUse(t, bashPayload(secretCWD, "default", pathReplace))
	if got := attended.decision(); got != "ask" {
		t.Fatalf("control: attended literal PATH replacement permissionDecision = %q, want ask (stdout: %s)", got, attended.stdout)
	}
	if !strings.Contains(attended.stderr, "env-vars -> ask") {
		t.Errorf("control: expected the env-vars rule to be the asker, stderr:\n%s", attended.stderr)
	}

	run := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", pathReplace))
	if got := run.decision(); got != "deny" {
		t.Fatalf("unattended literal PATH replacement permissionDecision = %q, want deny (stdout: %s)", got, run.stdout)
	}
	for _, want := range []string{denyReasonHead, "setting sensitive env var requires confirmation: PATH", "PATH=/some/bin:$PATH", "search /nix/store for a toolchain", "report it as `stuck` naming the missing tool"} {
		if !strings.Contains(run.reason(), want) {
			t.Errorf("reason missing %q:\n%s", want, run.reason())
		}
	}
}

// TestUnattendedHook_AttendedSessionStillAsks is the control the policy must not
// break: with a human present (or no permission mode at all, as in older payloads) a
// secret-path read is still an `ask` carrying the rule's own reason, unannotated.
func TestUnattendedHook_AttendedSessionStillAsks(t *testing.T) {
	for _, mode := range []string{"default", "plan", "acceptEdits", "auto", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			run := runPreToolUse(t, bashPayload(secretCWD, mode, envrcRead))
			if got := run.decision(); got != "ask" {
				t.Fatalf("permissionDecision = %q, want ask for an attended session (stdout: %s)", got, run.stdout)
			}
			if strings.Contains(run.reason(), "unattended") {
				t.Errorf("an attended ask must carry the rule's own reason only:\n%s", run.reason())
			}
			if !strings.Contains(run.reason(), "references credential/secret path /srv/work/project/.envrc") {
				t.Errorf("reason lost the rule's own words:\n%s", run.reason())
			}
		})
	}
}

// TestUnattendedHook_OnlyAsksAreConverted: an approval stays an approval, an abstain
// stays the empty object (Claude Code's own allowlist still decides it), and a rule's
// own hard deny keeps its own reason — in an unattended session exactly as in an
// attended one.
func TestUnattendedHook_OnlyAsksAreConverted(t *testing.T) {
	approve := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", "git log --oneline"))
	if got := approve.decision(); got != "allow" {
		t.Errorf("benign allow-listed command under dontAsk: permissionDecision = %q, want allow (stdout: %s)", got, approve.stdout)
	}

	abstain := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", "some-random-command"))
	if len(abstain.out) != 0 {
		t.Errorf("unmodeled command under dontAsk: stdout = %s, want the empty object {}", abstain.stdout)
	}

	cmd := "LD_PRELOAD=/evil.so git status"
	attended := runPreToolUse(t, bashPayload(secretCWD, "default", cmd))
	unattended := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd))
	if attended.decision() != "deny" || unattended.decision() != "deny" {
		t.Fatalf("rule Reject must stay deny in both session kinds, got attended=%q unattended=%q", attended.decision(), unattended.decision())
	}
	if attended.reason() != unattended.reason() {
		t.Errorf("a rule's own Reject reason must not change with the session kind:\n attended:   %s\n unattended: %s", attended.reason(), unattended.reason())
	}
}
