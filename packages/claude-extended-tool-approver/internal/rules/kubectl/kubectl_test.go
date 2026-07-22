package kubectl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
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

func TestKubectl_ReadOnly_Approve(t *testing.T) {
	r := New(nil, nil)
	commands := []string{
		"kubectl get pods",
		"kubectl describe pod foo",
		"kubectl logs deploy/foo",
		"kubectl top pods",
		"kubectl cluster-info",
		"kubectl config view",
		"kubectl api-resources",
		"kubectl version",
		"bin/kc get pods",
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

func TestKubectl_KubeconfigReadOnly_Approve(t *testing.T) {
	r := New(nil, nil)
	commands := []string{
		"KUBECONFIG=/other kubectl get pods",
		"KUBECONFIG=/other kubectl describe pod foo",
		"KUBECONFIG=/other kubectl logs deploy/foo",
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

func TestKubectl_KubeconfigModifying_Abstain(t *testing.T) {
	r := New(nil, nil)
	commands := []string{
		"KUBECONFIG=/other kubectl apply -f x.yaml",
		"KUBECONFIG=/other kubectl delete pod foo",
		"KUBECONFIG=/other kubectl scale deploy foo --replicas=2",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_Modifying_Abstain(t *testing.T) {
	r := New(nil, nil)
	commands := []string{
		"kubectl apply -f deploy.yaml",
		"kubectl delete pod foo",
		"kubectl scale deploy foo --replicas=2",
		"kubectl port-forward svc/foo 8080:80",
		"kubectl edit deployment foo",
		"kubectl patch deployment foo -p '{}'",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_DoubleDash_Abstain(t *testing.T) {
	r := New(nil, nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "kubectl -- get pods"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("kubectl -- get pods: got %s, want abstain (-- before operation)", got.Decision)
	}
}

func TestKubectl_NonKubectl_Abstain(t *testing.T) {
	r := New(nil, nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestKubectl_FlagValueNotOperation(t *testing.T) {
	r := New(nil, nil)
	// -n's value "get" must NOT be read as the operation; the real op is "delete".
	cmds := []string{
		"kubectl --namespace get delete pod foo",
		"kubectl -n sync delete pod foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (delete is modifying)", cmd, got.Decision)
		}
	}
}

func TestKubectl_ReadOnlyAdditions_Approve(t *testing.T) {
	r := New(nil, nil)
	cmds := []string{
		"kubectl events", "kubectl diff -f x.yaml", "kubectl wait --for=condition=Ready pod/foo",
		"bin/kc wslogs -n mp--ui--customer", "bin/kc zrlog -n mp--ui--customer",
		"bin/kc wsfirstpod --ws d-phillipg01",
		"kubectl rollout status deploy/foo", "bin/kc rollout history deploy/foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestKubectl_RolloutMutating_Abstain(t *testing.T) { // regression guard
	r := New(nil, nil)
	for _, cmd := range []string{"kubectl rollout restart deploy/foo", "kubectl rollout undo deploy/foo"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_DevxpNative(t *testing.T) {
	r := New(nil, nil)
	approve := []string{
		"AWS_PROFILE=dev/developers-dev bin/kc sync -f mp/ui/customer/layouts/test-runner --ws d-phillipg01",
		"bin/kc workspace list --ws d-phillipg01",
		"bin/kc syncdev --ws d-phillipg01",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
	// Non-dev scope must NOT be auto-approved.
	abstain := []string{
		"bin/kc sync -f x -n prod",
		"AWS_PROFILE=prod/admin bin/kc workspace delete --ws d-phillipg01",
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_ExecRecursion(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":                              {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"prove -v t/foo.t":                  {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"shell zr-sqitch deploy zr_finance": {Decision: hookio.Ask, Reason: "unknown", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.Abstain, Module: "mock"},
	}
	r := New(mockEval, nil)
	tests := []struct {
		name, command string
		want          hookio.Decision
	}{
		{"dev exe safe inner", "bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c test-runner -- bats", hookio.Approve},
		{"dev shell bash -c inner", "bin/kc shell --ws d-phillipg01 -n X -c test-runner -- bash -c 'prove -v t/foo.t'", hookio.Approve},
		{"dev exe sqitch inner asks", "bin/kc exe -n d-phillipgs0-db--sqitch -c sqitch-ui -- shell zr-sqitch deploy zr_finance", hookio.Ask},
		{"NON-dev exec stays abstain", "kubectl exec -n prod pod -- rm -rf /var/lib/data", hookio.Abstain},
		{"NON-dev exec no ns stays abstain", "kubectl exec -it pod/foo -- bash", hookio.Abstain},
		{"dev exe no double-dash abstains", "bin/kc exe --ws d-phillipg01 -c test-runner", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := r.Evaluate(input); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s want %s (reason %q)", tt.command, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestKubectl_IsDevWorkspaceScope(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"ws d- prefix", "bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c c -- bats", true},
		{"ns d- prefix", "bin/kc exe -n d-phillipgs0-db--sqitch -c c -- shell", true},
		{"ws= form", "bin/kc exe --ws=d-phillipg01 -- bats", true},
		{"aws dev profile + dev ws", "AWS_PROFILE=dev/developers-dev bin/kc exe --ws d-phillipg01 -- bats", true},
		{"no dev signal", "kubectl exec -it pod/foo -- bash", false},
		{"prod namespace", "kubectl exec -n prod pod -- rm -rf /x", false},
		{"prod aws profile overrides dev ws", "AWS_PROFILE=prod/admin bin/kc exe --ws d-phillipg01 -- rm -rf /x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := cmdparse.Parse(tt.cmd)
			// take the leaf whose executable is kc/kubectl
			var pc cmdparse.ParsedCommand
			for _, p := range parsed {
				if isKubectlExecutable(p.Executable) {
					pc = p
					break
				}
			}
			if got := isDevWorkspaceScope(pc.Args, pc.EnvVars); got != tt.want {
				t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestKubectl_Name(t *testing.T) {
	r := New(nil, nil)
	if got := r.Name(); got != "kubectl" {
		t.Errorf("Name() = %q, want kubectl", got)
	}
}
