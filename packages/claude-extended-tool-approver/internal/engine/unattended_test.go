package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// These tests drive the REAL composed rule chain (setup.RuleChain, via buildFullEngine)
// through EvaluateHook — the same facade the live PreToolUse hook and offline replay
// use — to pin the "an unattended session never asks" policy end to end: every Ask
// the chain can produce becomes a Reject with instructions when no human can answer a
// permission dialog, an attended session is exactly what it always was, and nothing
// but an Ask is ever touched. The binary-level twin is
// cmd/claude-extended-tool-approver/hook_unattended_integration_test.go.
//
// Why this matters: a hook Ask opens a permission dialog even under permission mode
// dontAsk, and a dialog in a session nobody is watching stalls it until a person
// attaches. Two real stalls came from the two rules below — `secrets` (a
// credential-path read) and `env-vars` (a literal PATH replacement).

const (
	unattendedProjectRoot = "/Users/testuser/workspace/my-project"
	unattendedSecretRead  = "cat " + unattendedProjectRoot + "/.envrc"
	// The command shape that stalled a real drain worker: a literal PATH replacement
	// behind two wrappers. Paths are neutral stand-ins.
	unattendedPathReplace = `bgrun job -- nice -n 10 env CGO_ENABLED=0 PATH=/opt/toolchain/go/bin:/usr/bin:/bin go -C /srv/work/pkg test -p 4 -count=1 ./...`
	// The same command with PATH prepended to the caller's value, which the env-vars
	// rule approves without a human; the denial text recommends exactly this form.
	unattendedPathPrepend = `bgrun job -- nice -n 10 env CGO_ENABLED=0 PATH=/opt/toolchain/go/bin:$PATH go -C /srv/work/pkg test -p 4 -count=1 ./...`
)

func unattendedBashInput(command, mode string) *hookio.HookInput {
	return &hookio.HookInput{
		ToolName:       "Bash",
		ToolInput:      makeBashJSON(command),
		CWD:            unattendedProjectRoot,
		PermissionMode: mode,
		HookEventName:  "PreToolUse",
	}
}

func TestEvaluateHook_UnattendedSecretPathReadIsADeterministicDeny(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)

	readTool, _ := json.Marshal(hookio.FileToolInput{FilePath: unattendedProjectRoot + "/.envrc"})
	inputs := map[string]*hookio.HookInput{
		"Bash cat .envrc under dontAsk":        unattendedBashInput(unattendedSecretRead, "dontAsk"),
		"Bash cat .env.yarn under dontAsk":     unattendedBashInput("cat "+unattendedProjectRoot+"/.env.yarn", "dontAsk"),
		"Bash cat ~/.ssh/id_rsa under dontAsk": unattendedBashInput("cat /Users/testuser/.ssh/id_rsa", "dontAsk"),
		"Read tool .envrc under dontAsk": {
			ToolName: "Read", ToolInput: readTool, CWD: unattendedProjectRoot, PermissionMode: "dontAsk", HookEventName: "PreToolUse",
		},
		"compound with a secret read under dontAsk": unattendedBashInput("git log --oneline && "+unattendedSecretRead, "dontAsk"),
		"autonomous flag with an attended mode": func() *hookio.HookInput {
			in := unattendedBashInput(unattendedSecretRead, "default")
			in.Unattended = true
			return in
		}(),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			got := eng.EvaluateHook(in)
			if got.Decision != hookio.Reject {
				t.Fatalf("Decision = %v (%s: %s), want Reject — an unattended session must never get an Ask", got.Decision, got.Module, got.Reason)
			}
			for _, want := range []string{"unattended session never asks", "references credential/secret path", "Do not read credential or environment files", "search /nix/store for a toolchain", "report it as `stuck` naming the missing tool"} {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("reason missing %q (the model must be told what to do instead):\n%s", want, got.Reason)
				}
			}
			if strings.Contains(got.Reason, "expected on PATH") {
				t.Errorf("reason must not claim what is on PATH (false on the pool today):\n%s", got.Reason)
			}
			if got.Module != "secrets" {
				t.Errorf("Module = %q, want the originating rule %q", got.Module, "secrets")
			}
		})
	}
}

// The attended control: the SAME secret-path read still asks a human when a human is
// there to answer, with the rule's own words and no unattended text.
func TestEvaluateHook_AttendedSessionStillAsksOnASecretPath(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)
	for _, mode := range []string{"default", "plan", "acceptEdits", "auto", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			got := eng.EvaluateHook(unattendedBashInput(unattendedSecretRead, mode))
			if got.Decision != hookio.Ask {
				t.Fatalf("Decision = %v (%s: %s), want Ask — an attended session must still be asked", got.Decision, got.Module, got.Reason)
			}
			if strings.Contains(got.Reason, "unattended") {
				t.Errorf("an attended Ask must carry the rule's own reason only, got:\n%s", got.Reason)
			}
		})
	}
}

// The existence check a real review worker ran names secret-looking tokens that are
// only PATHSPECS handed to `git ls-files`, which lists tracked names and opens
// nothing. It must pass in both kinds of session; the same tokens given to an option
// that makes git OPEN the file (`-X`) must still be screened.
func TestEvaluateHook_GitLsFilesPathspecsAreNotSecretReads(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)
	for _, mode := range []string{"dontAsk", "default"} {
		got := eng.EvaluateHook(unattendedBashInput("git ls-files -- .env.yarn */.env.yarn | head", mode))
		if got.Decision != hookio.Approve {
			t.Errorf("mode=%s: ls-files existence check = %v (%s: %s), want Approve", mode, got.Decision, got.Module, got.Reason)
		}
	}

	attended := eng.EvaluateHook(unattendedBashInput("git ls-files -X .envrc", "default"))
	unattended := eng.EvaluateHook(unattendedBashInput("git ls-files -X .envrc", "dontAsk"))
	if attended.Decision != hookio.Ask || unattended.Decision != hookio.Reject {
		t.Errorf("git ls-files -X reads .envrc: attended = %v, unattended = %v, want Ask and Reject", attended.Decision, unattended.Decision)
	}
}

// An Ask from a rule other than `secrets` is converted too: the policy lives at the
// facade, not in the rule that happened to stall the first worker.
func TestEvaluateHook_AnyAskProducingRuleBecomesADenyWhenUnattended(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)

	attended := eng.EvaluateHook(unattendedBashInput(unattendedPathReplace, "default"))
	if attended.Decision != hookio.Ask || attended.Module != "env-vars" {
		t.Fatalf("control: attended literal PATH replacement = %v from %q (%s), want Ask from env-vars", attended.Decision, attended.Module, attended.Reason)
	}

	got := eng.EvaluateHook(unattendedBashInput(unattendedPathReplace, "dontAsk"))
	if got.Decision != hookio.Reject {
		t.Fatalf("unattended literal PATH replacement = %v (%s: %s), want Reject", got.Decision, got.Module, got.Reason)
	}
	if got.Module != "env-vars" {
		t.Errorf("Module = %q, want env-vars kept for attribution", got.Module)
	}
	for _, want := range []string{"unattended session never asks", "setting sensitive env var requires confirmation: PATH", "PATH=/some/bin:$PATH", "search /nix/store for a toolchain", "report it as `stuck` naming the missing tool"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason missing %q:\n%s", want, got.Reason)
		}
	}
	for _, claim := range []string{"expected on PATH", "on PATH already"} {
		if strings.Contains(got.Reason, claim) {
			t.Errorf("reason must not claim what is on PATH (%q):\n%s", claim, got.Reason)
		}
	}
}

// The alternative the denial recommends really is approved, in BOTH kinds of session:
// if the env-vars rule ever stopped approving a PATH prepend, the advice would be a
// lie and this test is where that is noticed.
func TestEvaluateHook_TheRecommendedPathPrependIsApprovedWhenUnattended(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)
	for _, mode := range []string{"dontAsk", "default"} {
		got := eng.EvaluateHook(unattendedBashInput(unattendedPathPrepend, mode))
		if got.Decision != hookio.Approve {
			t.Errorf("mode=%s: PATH prepend = %v (%s: %s), want Approve", mode, got.Decision, got.Module, got.Reason)
		}
	}
}

// Only an Ask is ever converted. An approval, an abstain, and a rule's own hard
// deny pass through byte-for-byte, so the policy cannot widen or narrow anything
// but the dialog.
func TestEvaluateHook_UnattendedLeavesEveryOtherVerdictAlone(t *testing.T) {
	eng := buildFullEngine(unattendedProjectRoot, unattendedProjectRoot)
	for _, command := range []string{
		"git log --oneline",              // Approve
		"some-random-command",            // NoOpinion (abstain)
		"LD_PRELOAD=/evil.so git status", // Reject from a rule, with that rule's own reason
	} {
		t.Run(command, func(t *testing.T) {
			attended := eng.EvaluateHook(unattendedBashInput(command, "default"))
			unattended := eng.EvaluateHook(unattendedBashInput(command, "dontAsk"))
			if attended.Decision != unattended.Decision || attended.Reason != unattended.Reason || attended.Module != unattended.Module {
				t.Errorf("verdict changed with the session kind:\n attended:   %v %s: %s\n unattended: %v %s: %s",
					attended.Decision, attended.Module, attended.Reason, unattended.Decision, unattended.Module, unattended.Reason)
			}
			if strings.Contains(unattended.Reason, "unattended session never asks") {
				t.Errorf("a non-Ask verdict must not be annotated as converted: %s", unattended.Reason)
			}
		})
	}
}
