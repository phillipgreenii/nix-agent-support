package kubectl

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestKubectl_ReadOnly_Approve(t *testing.T) {
	r := New()
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
	r := New()
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

func TestKubectl_KubeconfigModifying_Ask(t *testing.T) {
	r := New()
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
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

func TestKubectl_Modifying_Ask(t *testing.T) {
	r := New()
	commands := []string{
		"kubectl apply -f deploy.yaml",
		"kubectl delete pod foo",
		"kubectl scale deploy foo --replicas=2",
		"kubectl exec -it pod/foo -- bash",
		"kubectl port-forward svc/foo 8080:80",
		"kubectl edit deployment foo",
		"kubectl patch deployment foo -p '{}'",
		"kubectl rollout restart deployment foo",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

func TestKubectl_DoubleDash_Abstain(t *testing.T) {
	r := New()
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
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestKubectl_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "kubectl" {
		t.Errorf("Name() = %q, want kubectl", got)
	}
}
