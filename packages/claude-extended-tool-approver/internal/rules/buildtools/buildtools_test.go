package buildtools

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestBuildtools_Approved_Approve(t *testing.T) {
	r := New()
	commands := []string{
		"gradle build",
		"./gradlew test",
		"pre-commit run --all-files",
		"bats tests/test.bats",
		"bd doctor",
		"bd onboard",
	}
	for _, cmd := range commands {
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

func TestBuildtools_BdAllSubcommands_Approve(t *testing.T) {
	r := New()
	commands := []string{
		"bd ready --json",
		"bd show pg2-ce6 --json",
		"bd update pg2-ce6 --claim --json",
		`bd create "Issue title" --description="Details" -t task -p 1 --json`,
		`bd close pg2-ce6 --reason "Done" --json`,
		"bd sync",
		"bd list --json",
		"bd search something --json",
		"bd children pg2-e6p --json",
		`bd comments pg2-ce6 --json`,
		`bd dep add pg2-ce6 --blocked-by pg2-abc`,
		"bd graph pg2-e6p",
		"bd status",
		"bd count",
		"bd version",
		`bd update pg2-ce6 --priority 1 --json`,
		`bd create "Found bug" --description="Details" -p 1 --deps discovered-from:pg2-ce6 --json`,
		`bd supersede pg2-abc --with pg2-xyz`,
		`bd reopen pg2-ce6`,
		`bd query "status:open priority:1" --json`,
	}
	for _, cmd := range commands {
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

func TestBuildtools_DevboxSearch_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "devbox search nodejs"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("devbox search nodejs: got %s, want approve", got.Decision)
	}
}

func TestBuildtools_Npm_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "npm install"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("npm install: got %s, want abstain", got.Decision)
	}
}

func TestBuildtools_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "build-tools" {
		t.Errorf("Name() = %q, want build-tools", got)
	}
}

func TestBuildtools_JarXf(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"jar xf", "jar xf /tmp/cache/some.jar", hookio.Approve},
		{"jar cf not approved", "jar cf output.jar src/", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func TestBuildtools_GenerateBuildDeps(t *testing.T) {
	r := New()
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "bin/generate-build-deps"})}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
}

func TestBuildtools_CueVet(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"cue vet approve", "cue vet ./schemas/ 2>&1", hookio.Approve},
		{"cue vet with path", "cue vet ./common/schemas/", hookio.Approve},
		{"cue export abstain", "cue export ./schemas/", hookio.Abstain},
		{"cue eval abstain", "cue eval ./schemas/", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}
