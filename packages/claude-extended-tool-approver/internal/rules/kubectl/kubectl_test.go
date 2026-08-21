package kubectl

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// zrKubectlConfig loads the ZR consumer config fixture and returns its kubectl
// sub-config. This is the golden-set source of truth: it mirrors the inline
// builtins.toJSON block in the ZR machine config
// (phillipg-nix-ziprecruiter machines/phillipg-mbp-02/default.nix). Every test
// below that used to exercise baked-in ZR behavior now injects THIS config, so
// the verdicts are identical to pre-refactor behavior yet fully config-driven.
func zrKubectlConfig(t *testing.T) configrules.KubectlConfig {
	t.Helper()
	return configrules.Load("../configrules/testdata/zr-rules.json").Kubectl
}

func TestKubectl_ReadOnly_Approve(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestKubectl_KubeconfigReadOnly_Approve(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestKubectl_KubeconfigModifying_Abstain(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_Modifying_Abstain(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_DoubleDash_Abstain(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "kubectl -- get pods"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("kubectl -- get pods: got %s, want abstain (-- before operation)", got.Decision)
	}
}

func TestKubectl_NonKubectl_Abstain(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestKubectl_FlagValueNotOperation(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	// -n's value "get" must NOT be read as the operation; the real op is "delete".
	cmds := []string{
		"kubectl --namespace get delete pod foo",
		"kubectl -n sync delete pod foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain (delete is modifying)", cmd, got.Decision)
		}
	}
}

// TestKubectl_GlobalValueFlags_OperationNotFlagValue is the pg2-ursuo
// regression: every kubectl GLOBAL flag that takes a value (per `kubectl
// options`, client v1.25.16) must have its value token skipped by
// extractOperation, so the flag's value is never mistaken for the verb and
// the real (modifying) verb after it is still classified correctly. Generic
// values only — no ZR-specific config needed, since these are base
// (baseValueFlags) global flags.
func TestKubectl_GlobalValueFlags_OperationNotFlagValue(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	cmds := []string{
		"kubectl --context get delete pod foo",
		"kubectl --cluster get delete pod foo",
		"kubectl --server get delete pod foo",
		"kubectl -s get delete pod foo",
		"kubectl --token get delete pod foo",
		"kubectl --user get delete pod foo",
		"kubectl --username get delete pod foo",
		"kubectl --password get delete pod foo",
		"kubectl --as get delete pod foo",
		"kubectl --as-group get delete pod foo",
		"kubectl --as-uid get delete pod foo",
		"kubectl --certificate-authority get delete pod foo",
		"kubectl --client-certificate get delete pod foo",
		"kubectl --client-key get delete pod foo",
		"kubectl --tls-server-name get delete pod foo",
		"kubectl --request-timeout get delete pod foo",
		"kubectl --cache-dir get delete pod foo",
		"kubectl -v get delete pod foo",
		"kubectl --v get delete pod foo",
		"kubectl --vmodule get delete pod foo",
		"kubectl --log-backtrace-at get delete pod foo",
		"kubectl --log-dir get delete pod foo",
		"kubectl --log-file get delete pod foo",
		"kubectl --log-file-max-size get delete pod foo",
		"kubectl --log-flush-frequency get delete pod foo",
		"kubectl --stderrthreshold get delete pod foo",
		"kubectl --profile get delete pod foo",
		"kubectl --profile-output get delete pod foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain (real op is delete, a global value-flag's value \"get\" must not be read as the op)", cmd, got.Decision)
		}
	}
}

// TestKubectl_GlobalBooleanFlags_NotValueFlags pins the OTHER direction: a
// boolean global flag (no value token) must NOT be in baseValueFlags, or its
// next real token would be wrongly skipped as if it were that flag's value —
// which here would swallow the actual "delete" verb and leave "pod" (a bare
// positional) misread as the operation, an approval-widening bug this table
// must not introduce.
func TestKubectl_GlobalBooleanFlags_NotValueFlags(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	cmds := []string{
		"kubectl --insecure-skip-tls-verify delete pod foo",
		"kubectl --warnings-as-errors delete pod foo",
		"kubectl --match-server-version delete pod foo",
		"kubectl --logtostderr delete pod foo",
		"kubectl --alsologtostderr delete pod foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain (delete is the op; a boolean flag must not swallow it as a fake value)", cmd, got.Decision)
		}
	}
}

func TestKubectl_ReadOnlyAdditions_Approve(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	cmds := []string{
		"kubectl events", "kubectl diff -f x.yaml", "kubectl wait --for=condition=Ready pod/foo",
		"bin/kc wslogs -n mp--ui--customer", "bin/kc zrlog -n mp--ui--customer",
		"bin/kc wsfirstpod --ws d-phillipg01",
		"kubectl rollout status deploy/foo", "bin/kc rollout history deploy/foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestKubectl_RolloutMutating_Abstain(t *testing.T) { // regression guard
	r := New(nil, nil, zrKubectlConfig(t))
	for _, cmd := range []string{"kubectl rollout restart deploy/foo", "kubectl rollout undo deploy/foo"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestKubectl_DevxpNative(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	approve := []string{
		"AWS_PROFILE=dev/developers-dev bin/kc sync -f mp/ui/customer/layouts/test-runner --ws d-phillipg01",
		"bin/kc workspace list --ws d-phillipg01",
		"bin/kc syncdev --ws d-phillipg01",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
	// Non-dev scope must NOT be auto-approved.
	abstain := []string{
		"bin/kc sync -f x -n prod",
		"AWS_PROFILE=prod/admin bin/kc workspace delete --ws d-phillipg01",
		"bin/kc sync -f x -n prod -- extra -n d-fake",
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
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

// EvaluateStructure satisfies hookio.Evaluator's I13 structural delegate
// method (pg2-m1i6r), and IS now exercised: kubectl's evaluateExec (pg2-9aqol)
// calls this instead of EvaluateExpression. It reconstructs the same
// space-joined lookup key EvaluateExpression's callers used to build by hand
// — but from the STRUCTURAL leaves argument itself (each leaf's Executable
// then its Args, every leaf concatenated in order), never from `source`. That
// mirrors real callers, which must treat leaves as authoritative and source
// as only the cycle-detection key (see EvaluateStructure's own doc), and it
// is what keeps every existing table row below passing unchanged: none of
// their fixture commands carries an inner argument whose reconstruction
// would differ from its pre-migration plain-text spelling.
func (m *mockEvaluator) EvaluateStructure(source string, leaves any, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	parsed, ok := leaves.([]cmdparse.ParsedCommand)
	if !ok {
		return m.defaultResult
	}
	var tokens []string
	for _, pc := range parsed {
		if pc.Executable == "" {
			continue
		}
		tokens = append(tokens, pc.Executable)
		tokens = append(tokens, pc.Args...)
	}
	return m.EvaluateExpression(strings.Join(tokens, " "), stack, origin)
}

func TestKubectl_ExecRecursion(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":                              {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"prove -v t/foo.t":                  {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"shell zr-sqitch deploy zr_finance": {Decision: hookio.Ask, Reason: "unknown", Module: "mock"},
			// Deliberately Approve: if isDevWorkspaceScope wrongly scans past `--`
			// and mistakes this inner `-n d-fake` for the outer scope, evaluateExec
			// recurses here and the mock would let it through — exposing the spoof.
			"bats -n d-fake": {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			// pg2-9aqol: proves the plain-args (non "bash -c") shape's single-quote
			// structural encoding round-trips through the mock exactly. If the
			// former `strings.Join(cmdArgs, " ")` text-join were still in effect and
			// this string were re-PARSED as shell text (which it is not, by design,
			// here in evaluateExec — the parse now happens once, inside
			// structuralInnerCommand, over the QUOTED form), the embedded ";" would
			// split it into two leaves ("echo safe" and "rm -rf /") instead of one
			// ("echo" with the whole string as its single argument). This key is
			// reachable only because that split does NOT happen.
			"echo safe; rm -rf /": {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.NoOpinion, Module: "mock"},
	}
	r := New(mockEval, nil, zrKubectlConfig(t))
	tests := []struct {
		name, command string
		want          hookio.Decision
	}{
		{"dev exe safe inner", "bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c test-runner -- bats", hookio.Approve},
		{"dev shell bash -c inner", "bin/kc shell --ws d-phillipg01 -n X -c test-runner -- bash -c 'prove -v t/foo.t'", hookio.Approve},
		{"dev exe sqitch inner asks", "bin/kc exe -n d-phillipgs0-db--sqitch -c sqitch-ui -- shell zr-sqitch deploy zr_finance", hookio.Ask},
		{"NON-dev exec stays abstain", "kubectl exec -n prod pod -- rm -rf /var/lib/data", hookio.NoOpinion},
		{"NON-dev exec no ns stays abstain", "kubectl exec -it pod/foo -- bash", hookio.NoOpinion},
		{"dev exe no double-dash abstains", "bin/kc exe --ws d-phillipg01 -c test-runner", hookio.NoOpinion},
		{"prod exec with decoy inner d- flag", "kubectl exec -n prod pod -- bats -n d-fake", hookio.NoOpinion},
		{
			"dev exe inner arg with embedded shell metacharacters stays one leaf",
			`bin/kc exe --ws d-phillipg01 -n test -c test-runner -- echo "safe; rm -rf /"`,
			hookio.Approve,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := hookio.Verdict(r.Evaluate(input)); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s want %s (reason %q)", tt.command, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestStructuralInnerCommand_PlainArgsPreserveQuotingAgainstReparse is the
// pg2-9aqol regression test: an inner command argument that CONTAINS shell
// metacharacters/operators must survive as one literal argument to one leaf,
// never be split apart as if it were rejoined shell TEXT. The pre-fix
// `strings.Join(cmdArgs, " ")` produced this EXACT string, and handing it to
// a shell parser (as EvaluateExpression's text entry point would) split it on
// the unquoted ";" into two commands — which is the quoting-loss defect this
// migration removes. structuralInnerCommand must yield exactly one leaf whose
// single argument still carries the ";" as data.
func TestStructuralInnerCommand_PlainArgsPreserveQuotingAgainstReparse(t *testing.T) {
	source, leaves, ok := structuralInnerCommand([]string{"echo", "safe; rm -rf /"})
	if !ok {
		t.Fatalf("structuralInnerCommand returned ok=false for source %q", source)
	}
	if len(leaves) != 1 {
		t.Fatalf("got %d leaves, want exactly 1 (a real re-parse of the naively-joined text "+
			"would split on the unquoted ';' into an \"echo safe\" leaf and a bare \"rm -rf /\" "+
			"leaf): %+v", len(leaves), leaves)
	}
	leaf := leaves[0]
	if leaf.Executable != "echo" {
		t.Errorf("Executable = %q, want %q", leaf.Executable, "echo")
	}
	if len(leaf.Args) != 1 || leaf.Args[0] != "safe; rm -rf /" {
		t.Errorf("Args = %#v, want a single arg %q (quoting loss would instead produce "+
			"a bare \"-rf\"/\"/\" argv or a second leaf)", leaf.Args, "safe; rm -rf /")
	}
}

// TestStructuralInnerCommand_BashDashCUsesScriptAloneAsRealShellSource covers
// the OTHER shape: "bash -c SCRIPT". The script (cmdArgs[2]) is genuine shell
// source and must be parsed AS-IS, honoring its own real operators — and any
// further cmdArgs (bash -c's own positional parameters) must NOT be appended
// to it, which is what the former `strings.Join(cmdArgs[2:], " ")` did.
func TestStructuralInnerCommand_BashDashCUsesScriptAloneAsRealShellSource(t *testing.T) {
	source, leaves, ok := structuralInnerCommand([]string{"bash", "-c", "echo one && echo two", "posarg0", "posarg1"})
	if !ok {
		t.Fatalf("structuralInnerCommand returned ok=false for source %q", source)
	}
	if source != "echo one && echo two" {
		t.Errorf("source = %q, want exactly cmdArgs[2] with the trailing positional "+
			"parameters excluded", source)
	}
	if len(leaves) != 2 {
		t.Fatalf("got %d leaves, want exactly 2 (the script's own \"&&\" is a real "+
			"operator and must be honored): %+v", len(leaves), leaves)
	}
	if leaves[0].Executable != "echo" || len(leaves[0].Args) != 1 || leaves[0].Args[0] != "one" {
		t.Errorf("leaves[0] = %+v, want echo one", leaves[0])
	}
	if leaves[1].Executable != "echo" || len(leaves[1].Args) != 1 || leaves[1].Args[0] != "two" {
		t.Errorf("leaves[1] = %+v, want echo two", leaves[1])
	}
}

// TestStructuralInnerCommand_UnparseableFailsClosed pins the ok=false branch:
// a script that cannot be parsed at all must not be handed to EvaluateStructure
// as an empty leaf set (which the engine's EvaluateStructure would otherwise
// read as "structurally empty" rather than "could not determine").
func TestStructuralInnerCommand_UnparseableFailsClosed(t *testing.T) {
	_, _, ok := structuralInnerCommand([]string{"bash", "-c", "echo $(unterminated"})
	if ok {
		t.Fatalf("structuralInnerCommand returned ok=true for an unparseable script")
	}
}

func TestKubectl_IsDevWorkspaceScope(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
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
		{"decoy inner flag after --", "kubectl exec -n prod pod -- bats -n d-fake", false},
		// sync/syncdev take the workspace as a bare POSITIONAL arg (not --ws/-n).
		{"sync positional d- workspace", "AWS_PROFILE=dev/developers-dev bin/kc sync -f mp/ui/customer/layouts/test-runner d-phillipg01", true},
		{"syncdev positional d- workspace", "bin/kc syncdev d-phillipg01", true},
		{"sync positional non-dev target", "bin/kc sync -f x prod-target", false},
		// positional detection must NOT leak to exec: a d- pod name is not a scope signal.
		{"exec positional d- pod not dev-scoped", "kubectl exec d-somepod -- rm -rf /x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := cmdparse.Parse(tt.cmd)
			// take the leaf whose executable is kc/kubectl
			var pc cmdparse.ParsedCommand
			for _, p := range parsed {
				if r.isKubectlExecutable(p.Executable) {
					pc = p
					break
				}
			}
			if got := r.isDevWorkspaceScope(r.extractOperation(pc.Args), pc.Args, pc.EnvVars); got != tt.want {
				t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestKubectl_Name(t *testing.T) {
	r := New(nil, nil, zrKubectlConfig(t))
	if got := r.Name(); got != "kubectl" {
		t.Errorf("Name() = %q, want kubectl", got)
	}
}

// --- Base-only (empty config) guards: the base binary must carry NO ZR
// literals. Under a zero KubectlConfig the rule recognizes only `kubectl`, the
// generic verbs, and no dev-workspace scope. ---

func emptyRule() *Rule { return New(nil, nil, configrules.KubectlConfig{}) }

func TestKubectl_EmptyConfig_BaseGenericApproves(t *testing.T) {
	r := emptyRule()
	// Generic kubectl read-only verbs still approve with no config.
	for _, cmd := range []string{"kubectl get pods", "kubectl version", "kubectl rollout status deploy/foo"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve (base generic)", cmd, got.Decision)
		}
	}
}

func TestKubectl_EmptyConfig_NoKcAlias(t *testing.T) {
	r := emptyRule()
	// `kc` is NOT recognized as kubectl without executableAliases config, so the
	// rule abstains (leaves it to other rules / the user prompt).
	if got := hookio.Verdict(r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "bin/kc get pods"})})); got.Decision != hookio.NoOpinion {
		t.Errorf("bin/kc get pods under empty config: got %s, want abstain (kc not a base alias)", got.Decision)
	}
	// isKubectlExecutable must not treat kc as kubectl under empty config.
	if r.isKubectlExecutable("bin/kc") {
		t.Error("empty config: kc must not be recognized as kubectl")
	}
	if !r.isKubectlExecutable("kubectl") {
		t.Error("empty config: kubectl must still be recognized")
	}
}

func TestKubectl_EmptyConfig_ZRVerbsAbstain(t *testing.T) {
	r := emptyRule()
	// ZR plugin verbs are NOT base read-only/exec verbs; on real kubectl they fall
	// through to "modifying -> abstain".
	for _, cmd := range []string{
		"kubectl wslogs -n x", "kubectl zrlog -n x",
		"kubectl exe -c c -- bats", "kubectl shell -c c -- bash", "kubectl wsexec -- bats",
		"kubectl sync -f x d-phillipg01", "kubectl syncdev d-phillipg01",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q under empty config: got %s, want abstain (ZR verb not baked in base)", cmd, got.Decision)
		}
	}
}

func TestKubectl_EmptyConfig_NoDevWorkspaceScope(t *testing.T) {
	r := emptyRule()
	// With no devWorkspacePrefix / devWorkspaceFlags / nonDevAccounts, NOTHING is
	// ever dev-scoped — a `d-` token and `--ws` flag carry no meaning in the base.
	for _, cmd := range []string{
		"AWS_PROFILE=dev/developers-dev kubectl exec --ws d-phillipg01 -c c -- rm -rf /",
		"kubectl exec -n d-phillipg01 pod -- rm -rf /",
	} {
		parsed := cmdparse.Parse(cmd)
		var pc cmdparse.ParsedCommand
		for _, p := range parsed {
			if r.isKubectlExecutable(p.Executable) {
				pc = p
				break
			}
		}
		if r.isDevWorkspaceScope(r.extractOperation(pc.Args), pc.Args, pc.EnvVars) {
			t.Errorf("cmd %q under empty config: unexpectedly dev-scoped (base must have no d- literal)", cmd)
		}
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision == hookio.Approve {
			t.Errorf("cmd %q under empty config: got Approve, want non-approve (no dev-scope in base)", cmd)
		}
	}
}

// TestKubectl_EnvScopeParameterizedByConfig proves the dev-scope env axes
// (non-dev accounts, cluster env var + prefixes) are driven ENTIRELY by config,
// not baked `prod`/`d1-` literals: a custom config with different values changes
// the outcome accordingly.
func TestKubectl_EnvScopeParameterizedByConfig(t *testing.T) {
	cfg := configrules.KubectlConfig{
		ExecutableAliases:  []string{"kc"},
		ScopedApproveVerbs: []string{"sync"},
		DevWorkspacePrefix: "z-",
		NonDevAccounts:     []string{"blocked-acct"},
		ClusterEnvVar:      "MY_CLUSTER",
		DevClusterPrefixes: []string{"ok-"},
		DevWorkspaceFlags:  []string{"--ws"},
	}
	r := New(nil, nil, cfg)
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"custom dev prefix z- matches", "kc sync --ws z-me", true},
		{"old d- prefix no longer matches", "kc sync --ws d-me", false},
		{"custom non-dev account blocks", "AWS_PROFILE=blocked-acct/x kc sync --ws z-me", false},
		{"prod no longer hardcoded non-dev", "AWS_PROFILE=prod/x kc sync --ws z-me", true},
		{"custom cluster prefix ok- allowed", "MY_CLUSTER=ok-1 kc sync --ws z-me", true},
		{"non-matching cluster blocks", "MY_CLUSTER=nope-1 kc sync --ws z-me", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed := cmdparse.Parse(tc.cmd)
			var pc cmdparse.ParsedCommand
			for _, p := range parsed {
				if r.isKubectlExecutable(p.Executable) {
					pc = p
					break
				}
			}
			if got := r.isDevWorkspaceScope(r.extractOperation(pc.Args), pc.Args, pc.EnvVars); got != tc.want {
				t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestKubectl_NoZRLiteralsInSource is the optional base-has-no-ZR-literals source
// scan (acceptance criterion): the quoted ZR-specific tokens MUST NOT appear as
// string literals in the base kubectl rule — they belong exclusively in config.
func TestKubectl_NoZRLiteralsInSource(t *testing.T) {
	src, err := os.ReadFile("kubectl.go")
	if err != nil {
		t.Fatalf("read kubectl.go: %v", err)
	}
	text := string(src)
	// Search for the QUOTED literal (e.g. `"kc"`) to avoid substring false
	// positives (`"exec"` contains exe; comments explain the extraction).
	forbidden := []string{
		`"kc"`, `"wslogs"`, `"zrlog"`, `"wsfirstpod"`,
		`"exe"`, `"shell"`, `"wsexec"`,
		`"sync"`, `"syncdev"`, `"workspace"`,
		`"--ws"`, `"--workspace"`,
		`"KC_CLUSTER"`, `"d1-"`, `"dd1-"`, `"d-"`,
		`"prod"`, `"dprod"`, `"euprod"`, `"fastlane"`,
	}
	for _, lit := range forbidden {
		if strings.Contains(text, lit) {
			t.Errorf("ZR literal %s found in kubectl.go — must live only in config", lit)
		}
	}
}

// TestADR0044_Kubectl_RefusedSites is the per-rule half of pg2-qxe85's census: every one
// of kubectl's five sites that pg2-d0ja3 left as ErrNotApplicable now REFUSES.
//
// It asserts a RELATION rather than a hardcoded verdict, and the relation is the one the
// bead's gate is worded in: `errors.Is(err, hookio.ErrRefused)` plus a floor no weaker
// than NoOpinion. A refusal can only ever demote a later rule's Approve, so a site that
// satisfies this can never widen approval — whereas the ErrNotApplicable it replaced let
// the leaf be reported as an EXHAUSTION, which is the half a consumer may clear.
//
// The two `wantRefused: false` rows are the DECLINED sites, and they are asserted here so
// the decline is a tested decision rather than an oversight: neither has classified a verb
// (the first has no verb at all, the second has no evaluator), so neither has examined
// anything it could withhold, and claiming a refusal would attribute a judgement that was
// never made.
func TestADR0044_Kubectl_RefusedSites(t *testing.T) {
	cfg := zrKubectlConfig(t)
	tests := []struct {
		site        string
		cmd         string
		rule        *Rule
		wantRefused bool
	}{
		{site: "exec verb outside a dev workspace", cmd: "kubectl exec mypod -- ls /", rule: New(nil, nil, cfg), wantRefused: true},
		{site: "rollout with a mutating sub-verb", cmd: "kubectl rollout restart deploy/x", rule: New(nil, nil, cfg), wantRefused: true},
		{site: "everything else (apply/delete/scale)", cmd: "kubectl delete pod x", rule: New(nil, nil, cfg), wantRefused: true},
		{site: "scoped-approve verb outside a dev workspace", cmd: "kc restart --ws not-a-dev-ws", rule: New(nil, nil, cfg), wantRefused: true},
		// DECLINED: no verb was classified, so nothing was examined.
		{site: "declined — no operation token", cmd: "kubectl", rule: New(nil, nil, cfg), wantRefused: false},
	}
	for _, tt := range tests {
		t.Run(tt.site, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.cmd})}
			res, err := tt.rule.Evaluate(input)
			gotRefused := errors.Is(err, hookio.ErrRefused)
			if gotRefused != tt.wantRefused {
				t.Fatalf("%q: refused=%v, want %v (err=%v, res=%+v)", tt.cmd, gotRefused, tt.wantRefused, err, res)
			}
			if !tt.wantRefused {
				return
			}
			if res.Decision < hookio.NoOpinion {
				t.Errorf("%q: floor is %s, weaker than NoOpinion — a refusal must never be less restrictive than the abstain it replaced", tt.cmd, res.Decision)
			}
			if res.Reason == "" {
				t.Errorf("%q: refusal carries no Reason; the restored text is the only record of WHY (ADR 0044)", tt.cmd)
			}
			if res.Module != tt.rule.Name() {
				t.Errorf("%q: refusal Module = %q, want %q — provenance needs the refusing rule's identity", tt.cmd, res.Module, tt.rule.Name())
			}
			// A refusal MUST still read as not-applicable to an un-upgraded consumer,
			// which is what makes the conversion test-compatible (ADR 0044's subtype claim).
			if !errors.Is(err, hookio.ErrNotApplicable) {
				t.Errorf("%q: refusal does not match ErrNotApplicable; the chain would treat it as a FAILURE", tt.cmd)
			}
		})
	}
}

// TestADR0044_Kubectl_KcExecWithoutInnerCommandRefuses covers the fifth site, which needs
// a dev-workspace scope to be reachable at all: the invocation IS a dev-scoped exec, so
// the rule committed to delegating, and the missing inner command after `--` is what stops
// it. That is an examined leaf, not an unexamined one.
func TestADR0044_Kubectl_KcExecWithoutInnerCommandRefuses(t *testing.T) {
	r := New(nil, nil, configrules.KubectlConfig{
		ExecutableAliases:  []string{"kc"},
		ExecVerbs:          []string{"exe"},
		DevWorkspaceFlags:  []string{"--ws"},
		DevWorkspacePrefix: "devws-",
	})
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "kc exe --ws devws-me"})}
	res, err := r.Evaluate(input)
	if !errors.Is(err, hookio.ErrRefused) {
		t.Fatalf("kc exec with no inner command: err=%v res=%+v, want ErrRefused", err, res)
	}
	if res.Decision < hookio.NoOpinion || res.Module != r.Name() {
		t.Errorf("floor = %+v, want a NoOpinion refusal attributed to %q", res, r.Name())
	}
}
