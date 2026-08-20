package docker

import (
	"encoding/json"
	"errors"
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

// EvaluateStructure satisfies hookio.Evaluator's I13 structural delegate
// method (pg2-m1i6r). No docker test exercises structural delegation yet —
// the docker rule itself is not migrated by that bead — so this simply
// reuses the same expr-keyed lookup EvaluateExpression already provides.
func (m *mockEvaluator) EvaluateStructure(source string, leaves any, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	return m.EvaluateExpression(source, stack, origin)
}

func TestDockerRule(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":               {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"rm -rf /":           {Decision: hookio.Reject, Reason: "no", Module: "mock"},
			"whoami":             {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"ls":                 {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"echo hello":         {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"true && ls":         {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"bash -c echo hello": {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.NoOpinion, Module: "mock"},
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
		{"docker run no --rm no cmd", "docker run myimage", "Bash", hookio.NoOpinion},
		{"docker run --rm no cmd", "docker run --rm myimage", "Bash", hookio.NoOpinion},
		{"docker run no --rm with cmd", "docker run myimage bats", "Bash", hookio.NoOpinion},
		{"not docker", "ls -la", "Bash", hookio.NoOpinion},
		{"docker run --rm gosu passthrough", "docker run --rm img gosu claude whoami", "Bash", hookio.Approve},
		{"docker run --rm bash -c init-firewall and gosu", `docker run --rm img bash -c "init-firewall.sh && gosu claude ls"`, "Bash", hookio.Approve},
		{"docker run --rm bash -c gosu nested bash", `docker run --rm img bash -c "gosu claude bash -c 'echo hello'"`, "Bash", hookio.Approve},
		{"docker run --rm bash -c su passthrough", `docker run --rm img bash -c "su claude -s /bin/bash -c 'whoami'"`, "Bash", hookio.Approve},
		{"non-bash", "", "Read", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  tt.tool,
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
				CWD:       "/tmp/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
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

// EvaluateStructure satisfies hookio.Evaluator's I13 structural delegate
// method (pg2-m1i6r); unused by any test here, so it mirrors
// EvaluateExpression's own capture behaviour.
func (c *capturingEvaluator) EvaluateStructure(source string, leaves any, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
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
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision == hookio.NoOpinion {
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

// TestADR0044_Docker_UnparseableMountRefuses is the per-rule half of pg2-qxe85's census for
// docker: its single site now REFUSES.
//
// The shape is the strongest argument in the whole census. Everything the rule needs in
// order to delegate is present — `run`, `--rm`, an image, an inner command — and the ONE
// thing that stops it is a mount spec it could not parse. The unparseable mount is exactly
// what makes the inner command's paths unjudgeable, so the leaf is un-clearable BECAUSE
// this rule examined it. Reported as not-applicable it reads as "no rule models docker",
// which is the approval-widening direction and the worst possible one for a container
// mounting the host somewhere unknown.
//
// It is the same fail-safe reading as the engine's unparseable-expression floor (ADR 0039's
// I1b): "I could not read this" is a floor, never an absence.
func TestADR0044_Docker_UnparseableMountRefuses(t *testing.T) {
	mockEval := &mockEvaluator{defaultResult: hookio.RuleResult{Decision: hookio.Approve, Reason: "ok", Module: "mock"}}
	r := New(mockEval, patheval.New("/home/user/project"))
	in := func(cmd string) *hookio.HookInput {
		return &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": cmd})}
	}

	res, err := r.Evaluate(in(`docker run --rm -v :::: alpine sh -c "ls /"`))
	if !errors.Is(err, hookio.ErrRefused) {
		t.Fatalf("unparseable mount spec: err=%v res=%+v, want ErrRefused", err, res)
	}
	if res.Decision < hookio.NoOpinion {
		t.Errorf("floor is %s, weaker than NoOpinion", res.Decision)
	}
	if res.Reason == "" || res.Module != r.Name() {
		t.Errorf("floor = %+v, want a reasoned refusal attributed to %q", res, r.Name())
	}
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Error("refusal does not match ErrNotApplicable; the engine would file it as a FAILURE")
	}

	// A well-formed mount is UNAFFECTED — the mock clears the inner command, so the rule
	// still forwards an approve. This is what proves the floor is scoped to the parse
	// failure and not to `docker run` as a family.
	res, err = r.Evaluate(in(`docker run --rm -v /home/user/project:/w alpine ls`))
	if err != nil || res.Decision != hookio.Approve {
		t.Errorf("well-formed mount = %+v (err=%v), want the inner verdict forwarded (approve)", res, err)
	}
}
