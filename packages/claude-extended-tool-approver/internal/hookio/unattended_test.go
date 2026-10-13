package hookio

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIsUnattended(t *testing.T) {
	tests := []struct {
		name string
		mode string
		flag bool
		want bool
	}{
		{"dontAsk never asks a human", "dontAsk", false, true},
		{"autonomous flag alone is enough", "default", true, true},
		{"both signals", "dontAsk", true, true},
		{"attended default", "default", false, false},
		{"attended plan", "plan", false, false},
		{"attended acceptEdits", "acceptEdits", false, false},
		{"attended auto still prompts", "auto", false, false},
		{"bypassPermissions silently accepts an Ask, no dialog to stall on", "bypassPermissions", false, false},
		{"old payload without a mode", "", false, false},
		{"mode name is case-sensitive like Claude Code's own", "dontask", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &HookInput{PermissionMode: tt.mode, Unattended: tt.flag}
			if got := in.IsUnattended(); got != tt.want {
				t.Errorf("IsUnattended(mode=%q, flag=%v) = %v, want %v", tt.mode, tt.flag, got, tt.want)
			}
		})
	}
}

// A hook payload must never be able to assert that the session is unattended: the
// flag is derived from the process environment alone (json:"-"), so a crafted
// tool_input or top-level field cannot flip Ask into Reject for someone else's session.
func TestUnattendedCannotBeAssertedByAPayload(t *testing.T) {
	for _, body := range []string{
		`{"tool_name":"Bash","Unattended":true}`,
		`{"tool_name":"Bash","unattended":true}`,
		`{"tool_name":"Bash","-":true}`,
	} {
		var in HookInput
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		if in.Unattended {
			t.Errorf("payload %s set Unattended; it must be settable only by the entry point", body)
		}
	}
}

func TestNeverAskWhenUnattended_OnlyAskIsConverted(t *testing.T) {
	trace := []TraceEntry{{RuleName: "secrets", Decision: Ask, Reason: "r"}}
	for _, d := range []Decision{Approve, NoOpinion, Ask, Reject} {
		original := RuleResult{Decision: d, Reason: "original reason", Module: "some-rule", Trace: trace, Provenance: ProvenanceExhaustion}

		attended := NeverAskWhenUnattended(&HookInput{PermissionMode: "default"}, original)
		if !reflect.DeepEqual(attended, original) {
			t.Errorf("attended %v: result changed: got %+v, want %+v", d, attended, original)
		}

		got := NeverAskWhenUnattended(&HookInput{PermissionMode: "dontAsk"}, original)
		if d != Ask {
			if !reflect.DeepEqual(got, original) {
				t.Errorf("unattended %v: only an Ask may change, got %+v, want %+v", d, got, original)
			}
			continue
		}
		if got.Decision != Reject {
			t.Errorf("unattended Ask became %v, want Reject", got.Decision)
		}
		if got.Module != original.Module {
			t.Errorf("Module = %q, want the originating rule %q kept for ask-log attribution", got.Module, original.Module)
		}
		if !reflect.DeepEqual(got.Trace, original.Trace) {
			t.Errorf("Trace was not preserved: got %+v", got.Trace)
		}
	}
}

// The denial is read by a model in a session this package knows nothing about, and
// whether a tool is on that session's PATH is a fact of its launch configuration. An
// earlier draft told workers the toolchain "is expected on PATH"; on the pool that was
// false, and a denial that lies sends the worker looking for a way around it. No
// reason may assert what is installed or on PATH, whichever rule asked.
func TestUnattendedAdvice_NeverAssertsWhatIsOnPath(t *testing.T) {
	for _, module := range []string{"secrets", "env-vars", "killshell", "primary-commit", ""} {
		t.Run("module="+module, func(t *testing.T) {
			got := NeverAskWhenUnattended(&HookInput{PermissionMode: "dontAsk"}, RuleResult{Decision: Ask, Module: module, Reason: "r"})
			for _, claim := range []string{"expected on PATH", "on PATH already", "is on PATH", "are on PATH", "available on PATH", "go, gofumpt", "prettier"} {
				if strings.Contains(got.Reason, claim) {
					t.Errorf("reason asserts %q, which is a claim about the worker's environment this package cannot know:\n%s", claim, got.Reason)
				}
			}
		})
	}
}

func TestNeverAskWhenUnattended_ReasonIsActionable(t *testing.T) {
	tests := []struct {
		name   string
		in     *HookInput
		result RuleResult
		want   []string
		notIn  []string
	}{
		{
			name:   "secret path read names the rule, keeps its words, says what to do instead",
			in:     &HookInput{PermissionMode: "dontAsk"},
			result: RuleResult{Decision: Ask, Module: "secrets", Reason: "references credential/secret path /w/.envrc — prompting instead of auto-approving"},
			want: []string{
				"unattended session never asks", "permission mode dontAsk",
				"Rule secrets needed a human", "references credential/secret path /w/.envrc",
				"Do not read credential or environment files", ".envrc",
				"search /nix/store for a toolchain", "report it as `stuck` naming the missing tool", "orchestrator", "do not retry",
			},
		},
		{
			name:   "env-var ask says how to prepend to PATH instead of replacing it",
			in:     &HookInput{PermissionMode: "dontAsk"},
			result: RuleResult{Decision: Ask, Module: "env-vars", Reason: "setting sensitive env var requires confirmation: PATH"},
			want: []string{
				"Rule env-vars needed a human", "requires confirmation: PATH", "PATH=/some/bin:$PATH", "literal replacement",
				"search /nix/store for a toolchain", "report it as `stuck` naming the missing tool",
			},
		},
		{
			name:   "any other rule gets the generic instruction, not another rule's advice",
			in:     &HookInput{PermissionMode: "dontAsk"},
			result: RuleResult{Decision: Ask, Module: "killshell", Reason: "cannot verify shell ownership"},
			want:   []string{"Rule killshell needed a human", "cannot verify shell ownership", "Do not retry this call or work around the denial", "final report"},
			notIn:  []string{".envrc", "PATH=/some/bin"},
		},
		{
			name:   "the autonomous flag is named as the reason when the mode is not dontAsk",
			in:     &HookInput{PermissionMode: "default", Unattended: true},
			result: RuleResult{Decision: Ask, Module: "secrets", Reason: "x"},
			want:   []string{"autonomous worker (" + AutonomousEnvVar + "=1)"},
			notIn:  []string{"permission mode"},
		},
		{
			name:   "both signals are both named",
			in:     &HookInput{PermissionMode: "dontAsk", Unattended: true},
			result: RuleResult{Decision: Ask, Module: "secrets", Reason: "x"},
			want:   []string{"permission mode dontAsk and autonomous worker"},
		},
		{
			name:   "an Ask with no rule attribution still yields a complete, actionable reason",
			in:     &HookInput{PermissionMode: "dontAsk"},
			result: RuleResult{Decision: Ask},
			want:   []string{"unattended session never asks", "final report"},
			notIn:  []string{"Rule "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeverAskWhenUnattended(tt.in, tt.result)
			if got.Decision != Reject {
				t.Fatalf("Decision = %v, want Reject", got.Decision)
			}
			for _, w := range tt.want {
				if !strings.Contains(got.Reason, w) {
					t.Errorf("reason missing %q:\n%s", w, got.Reason)
				}
			}
			for _, n := range tt.notIn {
				if strings.Contains(got.Reason, n) {
					t.Errorf("reason must not contain %q:\n%s", n, got.Reason)
				}
			}
		})
	}
}
