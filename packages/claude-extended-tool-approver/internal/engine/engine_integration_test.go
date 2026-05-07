package engine

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/assume"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/buildtools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/claudetools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/curl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/docker"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/envvars"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/gh"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/git"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/kubectl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/mcp"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/monorepo"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/nix"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pathsafety"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/safecmds"
	sqlite3rule "github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/sqlite3"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/webfetch"
)

func buildFullEngine(projectRoot, cwd string) *Engine {
	pe := patheval.NewWithCWD(projectRoot, cwd)
	eng := New()
	eng.SetPathEvaluator(pe)
	nixRule := nix.NewWithEvaluator(eng)
	dockerRule := docker.New(eng, pe)

	eng.RegisterRules(
		envvars.New(),
		assume.New(),
		webfetch.New(),
		claudetools.New(),
		pathsafety.New(pe),
		mcp.New(),
		git.New(),
		gh.New(nil),
		monorepo.New(pe),
		nixRule,
		dockerRule,
		safecmds.New(pe),
		curl.New(),
		kubectl.New(),
		buildtools.New(),
		sqlite3rule.New(pe),
	)
	return eng
}

func TestIntegration_RegressionSuite(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")

	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	tests := []struct {
		name    string
		command string
		tool    string
		want    hookio.Decision
	}{
		// Safe commands
		{"ls in project", "ls -la /Users/testuser/workspace/my-project/src", "Bash", hookio.Approve},
		{"git log", "git log --oneline -10", "Bash", hookio.Approve},
		{"jq in project", "jq '.key' /Users/testuser/workspace/my-project/data.json", "Bash", hookio.Approve},

		// Cross-repo (workspace root)
		{"ls sibling repo", "ls /Users/testuser/workspace/other-repo/src", "Bash", hookio.Approve},

		// Build tools
		{"gradle build", "gradle build", "Bash", hookio.Approve},
		{"bats test", "bats tests/", "Bash", hookio.Approve},
		{"jar xf", "jar xf /tmp/cache/some.jar", "Bash", hookio.Approve},

		// Nix
		{"nix flake check", "nix flake check", "Bash", hookio.Approve},
		{"nix build", "nix build", "Bash", hookio.Approve},
		{"darwin-rebuild switch rejected", "darwin-rebuild switch", "Bash", hookio.Reject},

		// Assume rejected
		{"assume rejected", "assume my-role", "Bash", hookio.Reject},

		// Curl to allowed domain
		{"curl to localhost", "curl http://localhost:8080/health", "Bash", hookio.Approve},

		// SQLite3
		{"sqlite3 select on project db", `sqlite3 /Users/testuser/workspace/my-project/test.db "SELECT 1"`, "Bash", hookio.Approve},

		// Docker
		{"docker ps", "docker ps", "Bash", hookio.Approve},
		{"docker build", "docker build -t myimg .", "Bash", hookio.Approve},

		// New safe commands
		{"df", "df -h", "Bash", hookio.Approve},
		{"du in project", "du -sh /Users/testuser/workspace/my-project/src", "Bash", hookio.Approve},

	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  tt.tool,
				CWD:       cwd,
				ToolInput: makeBashJSON(tt.command),
			}
			got := eng.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v (%s: %s), want %v", got.Decision, got.Module, got.Reason, tt.want)
			}
		})
	}
}

func makeBashJSON(cmd string) json.RawMessage {
	if cmd == "" {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}
