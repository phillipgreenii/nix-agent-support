package docker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
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

func TestDockerRule(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":       {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"rm -rf /":   {Decision: hookio.Reject, Reason: "no", Module: "mock"},
			"whoami":              {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"ls":                  {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"echo hello":          {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"true && ls":          {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"bash -c echo hello":  {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.Abstain, Module: "mock"},
	}
	r := New(mockEval, nil)

	tests := []struct {
		name    string
		command string
		tool    string
		want    hookio.Decision
	}{
		{"docker build", "docker build -t myimage .", "Bash", hookio.Approve},
		{"docker ps", "docker ps", "Bash", hookio.Approve},
		{"docker images", "docker images", "Bash", hookio.Approve},
		{"docker logs", "docker logs container1", "Bash", hookio.Approve},
		{"docker inspect", "docker inspect container1", "Bash", hookio.Approve},
		{"docker start", "docker start container1", "Bash", hookio.Approve},
		{"docker stop", "docker stop container1", "Bash", hookio.Approve},
		{"docker rm", "docker rm container1", "Bash", hookio.Approve},
		{"docker rmi", "docker rmi myimage", "Bash", hookio.Approve},
		{"docker run --rm safe cmd", "docker run --rm myimage bats", "Bash", hookio.Approve},
		{"docker run --rm dangerous cmd", "docker run --rm myimage rm -rf /", "Bash", hookio.Reject},
		{"docker run --rm bash -c safe", "docker run --rm myimage bash -c 'bats'", "Bash", hookio.Approve},
		{"docker exec safe", "docker exec container1 bats", "Bash", hookio.Approve},
		{"docker run no --rm no cmd", "docker run myimage", "Bash", hookio.Abstain},
		{"docker run --rm no cmd", "docker run --rm myimage", "Bash", hookio.Abstain},
		{"docker run no --rm with cmd", "docker run myimage bats", "Bash", hookio.Abstain},
		{"not docker", "ls -la", "Bash", hookio.Abstain},
		{"docker run --rm gosu passthrough", "docker run --rm img gosu claude whoami", "Bash", hookio.Approve},
		{"docker run --rm bash -c init-firewall and gosu", `docker run --rm img bash -c "init-firewall.sh && gosu claude ls"`, "Bash", hookio.Approve},
		{"docker run --rm bash -c gosu nested bash", `docker run --rm img bash -c "gosu claude bash -c 'echo hello'"`, "Bash", hookio.Approve},
		{"docker run --rm bash -c su passthrough", `docker run --rm img bash -c "su claude -s /bin/bash -c 'whoami'"`, "Bash", hookio.Approve},
		{"non-bash", "", "Read", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  tt.tool,
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       "/tmp/project",
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestParseRunArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantImage string
		wantCmd   []string
	}{
		{"standard", []string{"--rm", "-it", "myimage", "bash"}, "myimage", []string{"bash"}},
		{"flags with values", []string{"--rm", "-e", "FOO=bar", "-v", "/a:/b", "img", "cmd"}, "img", []string{"cmd"}},
		{"flag=value style", []string{"--rm", "--name=mycontainer", "img", "cmd"}, "img", []string{"cmd"}},
		{"entrypoint override", []string{"--rm", "--entrypoint", "bash", "img", "-c", "expr"}, "img", []string{"-c", "expr"}},
		{"repeated -e flags", []string{"--rm", "-e", "A=1", "-e", "B=2", "img", "cmd"}, "img", []string{"cmd"}},
		{"combined short booleans -itd", []string{"--rm", "-itd", "img"}, "img", nil},
		{"combined short booleans -dit", []string{"--rm", "-dit", "img"}, "img", nil},
		{"image only no cmd", []string{"--rm", "img"}, "img", nil},
		{"no args", []string{}, "", nil},
		{"unknown flag heuristic", []string{"--rm", "--shm-size", "256m", "myimage", "cmd"}, "myimage", []string{"cmd"}},
		{"unknown flag followed by flag", []string{"--rm", "--unknown-bool", "--other", "myimage"}, "myimage", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotImage, gotCmd := parseRunArgs(tt.args)
			if gotImage != tt.wantImage {
				t.Errorf("image = %q, want %q", gotImage, tt.wantImage)
			}
			if tt.wantCmd == nil && gotCmd != nil {
				t.Errorf("cmd = %v, want nil", gotCmd)
			} else if tt.wantCmd != nil && !reflect.DeepEqual(gotCmd, tt.wantCmd) {
				t.Errorf("cmd = %v, want %v", gotCmd, tt.wantCmd)
			}
		})
	}
}

type capturingEvaluator struct {
	lastOrigin *hookio.HookInput
	result     hookio.RuleResult
}

func (c *capturingEvaluator) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	c.lastOrigin = origin
	return c.result
}

func TestParseMounts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []patheval.Mount
		ok   bool
	}{
		{"no mounts", []string{"--rm", "img", "cmd"}, nil, true},
		{"-v rw", []string{"-v", "/host/a:/container/a", "img"}, []patheval.Mount{{HostPath: "/host/a", ContainerPath: "/container/a"}}, true},
		{"-v ro", []string{"-v", "/host/a:/container/a:ro", "img"}, []patheval.Mount{{HostPath: "/host/a", ContainerPath: "/container/a", ReadOnly: true}}, true},
		{"--volume=", []string{"--volume=/h:/c:ro", "img"}, []patheval.Mount{{HostPath: "/h", ContainerPath: "/c", ReadOnly: true}}, true},
		{"--mount bind", []string{"--mount", "type=bind,src=/h,dst=/c,readonly", "img"}, []patheval.Mount{{HostPath: "/h", ContainerPath: "/c", ReadOnly: true}}, true},
		{"named volume ignored", []string{"-v", "myvolume:/data", "img"}, nil, true},
		{"malformed -v", []string{"-v", "/only-host", "img"}, nil, false},
		{"malformed --mount missing dst", []string{"--mount", "type=bind,src=/h", "img"}, nil, false},
		{"tmpfs ignored", []string{"--mount", "type=tmpfs,dst=/tmp", "img"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMounts(tt.args)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mounts = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestDockerRule_MountAwareRegressions covers the four affected asklog rows
// from pg2-0ybw. Each case verifies the docker rule scopes inner expression
// evaluation with a container-mode PathEvaluator whose mount list matches the
// docker command.
func TestDockerRule_MountAwareRegressions(t *testing.T) {
	projectRoot := t.TempDir()
	pe := patheval.NewWithCWD(projectRoot, projectRoot)

	tests := []struct {
		name       string
		command    string
		wantMounts int
		// probe path to evaluate via the scoped evaluator after the rule runs
		probePath       string
		wantProbeAccess patheval.PathAccess
	}{
		{
			name:            "row 922 no mounts, find container-internal",
			command:         `docker run --rm contained-claude:latest bash -c 'find / -name "bwrap"'`,
			wantMounts:      0,
			probePath:       "/",
			wantProbeAccess: patheval.PathReadWrite,
		},
		{
			name:            "row 1083 no mounts, grep /nix/store container-internal",
			command:         `docker run --rm contained-claude:latest bash -c 'grep foo /nix/store/xyz/cli.js'`,
			wantMounts:      0,
			probePath:       "/nix/store/xyz/cli.js",
			wantProbeAccess: patheval.PathReadWrite,
		},
		{
			name:            "row 1065 rw mount, container path on mount",
			command:         `docker run --rm -v /host/claude:/home/claude/.claude contained-claude:latest cat /home/claude/.claude/debug/x.txt`,
			wantMounts:      1,
			probePath:       "/home/claude/.claude/debug/x.txt",
			wantProbeAccess: patheval.PathUnknown, // /host/claude is not in any host zone
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := &capturingEvaluator{result: hookio.RuleResult{Decision: hookio.Approve, Module: "mock"}}
			r := New(cap, pe)
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       projectRoot,
			}
			got := r.Evaluate(input)
			if got.Decision == hookio.Abstain {
				t.Fatalf("rule abstained: %s", got.Reason)
			}
			if cap.lastOrigin == nil || cap.lastOrigin.PathEval == nil {
				t.Fatal("docker rule did not set origin.PathEval")
			}
			access := cap.lastOrigin.PathEval.Evaluate(tt.probePath)
			if access != tt.wantProbeAccess {
				t.Errorf("probe %q: got %v, want %v", tt.probePath, access, tt.wantProbeAccess)
			}
		})
	}
}

func TestDockerRule_Name(t *testing.T) {
	r := New(&mockEvaluator{}, nil)
	if got := r.Name(); got != "docker" {
		t.Errorf("Name() = %q, want docker", got)
	}
}
