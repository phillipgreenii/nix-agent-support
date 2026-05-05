package nix

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestNix_ReadOnly_Approve(t *testing.T) {
	approve := []string{
		"nix log /nix/store/abc123",
		"nix show-derivation /nix/store/abc123",
		"nix path-info /nix/store/abc123",
		"nix eval .#myPackage",
		"nix build .#myPackage",
		"nix develop",
		"nix search nixpkgs hello",
		"nix doctor",
		"nix hash path /nix/store/abc123",
		"nix why-depends .#a .#b",
		"nix store info",
		"nix print-dev-env",
		"nix derivation show /nix/store/abc123.drv",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestNix_FlakeApprove(t *testing.T) {
	approve := []string{
		"nix flake show",
		"nix flake metadata",
		"nix flake check",
		"nix flake lock",
		"nix flake update",
		"nix flake info",
		"nix flake prefetch",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestNix_Run_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "nix run nixpkgs#hello"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("nix run: got %s, want abstain (executes arbitrary code)", got.Decision)
	}
}

func TestNix_DarwinRebuildSwitch_Reject(t *testing.T) {
	reject := []string{
		"darwin-rebuild switch --flake .",
		"darwin-rebuild activate",
		"nixos-rebuild switch",
		"nixos-rebuild boot",
		"nixos-rebuild test",
		"home-manager switch",
	}
	r := New()
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject", cmd, got.Decision)
		}
	}
}

func TestNix_DarwinRebuildBuild_Approve(t *testing.T) {
	approve := []string{
		"darwin-rebuild build --flake .",
		"darwin-rebuild check --flake .",
		"nixos-rebuild build --flake .",
		"home-manager build --flake .",
		"darwin-rebuild dry-build --flake .",
		"darwin-rebuild dry-activate --flake .",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestNix_NixEnvInstall_Reject(t *testing.T) {
	reject := []string{
		"nix-env --install hello",
		"nix-env -i hello",
		"nix-env --upgrade",
		"nix-env -u",
		"nix-env --uninstall hello",
		"nix-env -e hello",
		"nix-env --set hello",
	}
	r := New()
	for _, cmd := range reject {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject", cmd, got.Decision)
		}
	}
}

func TestNix_NixEnvQuery_Approve(t *testing.T) {
	approve := []string{
		"nix-env --query",
		"nix-env -q",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestNix_NixStore_Approve(t *testing.T) {
	approve := []string{
		"nix-store --query /nix/store/abc123",
		"nix-store -q /nix/store/abc123",
		"nix-store --print-env /nix/store/abc123.drv",
		"nix-store --verify",
		"nix-store --verify-path /nix/store/abc123",
		"nix-store --dump /nix/store/abc123",
		"nix-store --export /nix/store/abc123",
		"nix-store --read-log /nix/store/abc123.drv",
		"nix-store -l /nix/store/abc123.drv",
		"nix-store --dump-db",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestNix_NixInstantiate_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "nix-instantiate --eval -E '1+1'"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("nix-instantiate: got %s, want approve", got.Decision)
	}
}

func TestNix_NonNix_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls: got %s, want abstain", got.Decision)
	}
}

func TestNix_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "nix" {
		t.Errorf("Name() = %q, want nix", got)
	}
}

type mockEvaluator struct {
	results       map[string]hookio.RuleResult
	defaultResult hookio.RuleResult
}

func (m *mockEvaluator) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	expr = strings.TrimSpace(expr)
	if r, ok := m.results[expr]; ok {
		return r
	}
	return m.defaultResult
}

func TestNixRule_ShellCommand(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"shellcheck --exclude=SC1091 /tmp/test.sh": {Decision: hookio.Approve, Reason: "approved", Module: "mock"},
			"rm -rf /": {Decision: hookio.Reject, Reason: "rejected", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.Abstain, Module: "mock"},
	}
	r := NewWithEvaluator(mockEval)

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"shell -c safe", "nix shell nixpkgs#shellcheck -c shellcheck --exclude=SC1091 /tmp/test.sh", hookio.Approve},
		{"shell -c dangerous", "nix shell nixpkgs#coreutils -c rm -rf /", hookio.Reject},
		{"shell -c unknown", "nix shell nixpkgs#hello -c unknown-tool", hookio.Abstain},
		{"shell --command", "nix shell nixpkgs#shellcheck --command shellcheck --exclude=SC1091 /tmp/test.sh", hookio.Approve},
		{"shell without command", "nix shell nixpkgs#hello", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command}), CWD: "/tmp/project"}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestNixRule_DevelopCommand(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":     {Decision: hookio.Approve, Reason: "approved", Module: "mock"},
			"rm -rf /": {Decision: hookio.Reject, Reason: "rejected", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.Abstain, Module: "mock"},
	}
	r := NewWithEvaluator(mockEval)

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"develop command bats", "nix develop --command bats", hookio.Approve},
		{"develop command dangerous", "nix develop --command rm -rf /", hookio.Reject},
		{"develop command unknown", "nix develop --command unknown-tool", hookio.Abstain},
		{"develop without command", "nix develop", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command}), CWD: "/tmp/project"}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}
