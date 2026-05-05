package docker

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var safeSubcommands = map[string]bool{
	"build": true, "start": true, "stop": true,
	"rm": true, "rmi": true, "ps": true,
	"images": true, "logs": true, "inspect": true,
}

// flagsWithValues lists docker run/exec flags that consume the next argument.
var flagsWithValues = map[string]bool{
	"-e": true, "-v": true, "-p": true, "-w": true,
	"--name": true, "--network": true, "--entrypoint": true,
	"-u": true, "--cpus": true, "-m": true, "--memory": true,
	"--platform": true, "--label": true, "-l": true,
	"--shm-size": true, "--runtime": true, "--volume": true,
	"--env": true, "--workdir": true, "--user": true,
	"--hostname": true, "-h": true, "--ip": true,
	"--mount": true, "--device": true, "--dns": true,
	"--add-host": true, "--tmpfs": true, "--ulimit": true,
	"--log-driver": true, "--log-opt": true, "--restart": true,
	"--stop-signal": true, "--stop-timeout": true,
	"--health-cmd": true, "--pid": true, "--ipc": true,
	"--uts": true, "--cgroupns": true, "--cap-add": true,
	"--cap-drop": true, "--security-opt": true,
	"--storage-opt": true, "--sysctl": true, "--gpus": true,
}

var knownBooleanShortFlags = map[byte]bool{
	'i': true, 't': true, 'd': true,
}

func isBooleanFlagCluster(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	for i := 1; i < len(arg); i++ {
		if !knownBooleanShortFlags[arg[i]] {
			return false
		}
	}
	return true
}

type Rule struct {
	exprEval hookio.Evaluator
	pe       *patheval.PathEvaluator
}

func New(eval hookio.Evaluator, pe *patheval.PathEvaluator) *Rule {
	return &Rule{exprEval: eval, pe: pe}
}

func (r *Rule) Name() string {
	return "docker"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if basename != "docker" {
			continue
		}
		return r.evaluateDocker(pc.Args, input)
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateDocker(args []string, input *hookio.HookInput) hookio.RuleResult {
	subcmd := firstNonFlag(args)
	if subcmd == "" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	if safeSubcommands[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "docker: docker " + subcmd + " is approved",
			Module:   r.Name(),
		}
	}

	if subcmd == "run" {
		return r.evaluateRun(args, input)
	}
	if subcmd == "exec" {
		return r.evaluateExec(args, input)
	}

	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateRun(args []string, input *hookio.HookInput) hookio.RuleResult {
	// Find args after "run"
	runIdx := -1
	for i, a := range args {
		if a == "run" {
			runIdx = i
			break
		}
	}
	if runIdx < 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	runArgs := args[runIdx+1:]

	// Check for --rm flag
	hasRM := false
	for _, a := range runArgs {
		if a == "--rm" {
			hasRM = true
			break
		}
	}
	if !hasRM {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	// Parse past flags to find image and command
	image, cmdArgs := parseRunArgs(runArgs)
	if image == "" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if len(cmdArgs) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	// Parse bind mounts from run args. Malformed mount syntax → abstain.
	mounts, ok := parseMounts(runArgs)
	if !ok {
		return hookio.RuleResult{Decision: hookio.Abstain, Reason: "docker: unparseable mount spec", Module: r.Name()}
	}

	// Check for bash -c pattern
	innerExpr := extractInnerCommand(cmdArgs)
	innerExpr = stripDockerPassthroughs(innerExpr)
	// Stack frame records the outer docker command (not inner) to prevent re-entering docker evaluation
	outerExpr := normalizeExpr(strings.Join(args, " "))
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "docker run", Expression: outerExpr}}

	scopedInput := r.withContainerEval(input, mounts)
	return r.exprEval.EvaluateExpression(innerExpr, stack, scopedInput)
}

func (r *Rule) evaluateExec(args []string, input *hookio.HookInput) hookio.RuleResult {
	// Find args after "exec"
	execIdx := -1
	for i, a := range args {
		if a == "exec" {
			execIdx = i
			break
		}
	}
	if execIdx < 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	execArgs := args[execIdx+1:]

	// Skip flags, find container name, then command
	_, cmdArgs := parseRunArgs(execArgs)
	if len(cmdArgs) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	innerExpr := extractInnerCommand(cmdArgs)
	innerExpr = stripDockerPassthroughs(innerExpr)
	outerExpr := normalizeExpr(strings.Join(args, " "))
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "docker exec", Expression: outerExpr}}

	// docker exec cannot observe the container's mount list from the command
	// line; treat all inner paths as container-internal.
	scopedInput := r.withContainerEval(input, []patheval.Mount{})
	return r.exprEval.EvaluateExpression(innerExpr, stack, scopedInput)
}

// withContainerEval returns a clone of input with PathEval set to a
// container-mode evaluator carrying the supplied mounts.
func (r *Rule) withContainerEval(input *hookio.HookInput, mounts []patheval.Mount) *hookio.HookInput {
	cloned := *input
	if r.pe != nil {
		cloned.PathEval = r.pe.WithMounts(mounts)
	}
	return &cloned
}

// parseMounts extracts bind mounts from docker run/exec args. Returns (nil,
// true) when no mounts are present, (_, false) when any mount spec is
// malformed.
func parseMounts(args []string) ([]patheval.Mount, bool) {
	var mounts []patheval.Mount
	i := 0
	for i < len(args) {
		a := args[i]
		// -v <spec> / --volume <spec>
		if a == "-v" || a == "--volume" {
			if i+1 >= len(args) {
				return nil, false
			}
			m, ok := parseVolumeSpec(args[i+1])
			if !ok {
				return nil, false
			}
			if m != nil {
				mounts = append(mounts, *m)
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--volume=") {
			m, ok := parseVolumeSpec(strings.TrimPrefix(a, "--volume="))
			if !ok {
				return nil, false
			}
			if m != nil {
				mounts = append(mounts, *m)
			}
			i++
			continue
		}
		// --mount type=bind,src=...,dst=...[,readonly]
		if a == "--mount" {
			if i+1 >= len(args) {
				return nil, false
			}
			m, ok := parseMountFlag(args[i+1])
			if !ok {
				return nil, false
			}
			if m != nil {
				mounts = append(mounts, *m)
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--mount=") {
			m, ok := parseMountFlag(strings.TrimPrefix(a, "--mount="))
			if !ok {
				return nil, false
			}
			if m != nil {
				mounts = append(mounts, *m)
			}
			i++
			continue
		}
		i++
	}
	return mounts, true
}

// parseVolumeSpec parses "host:container[:mode]". Returns (nil, true) for
// named volumes (no leading /) which do not map host filesystem paths.
// Returns (_, false) for malformed specs.
func parseVolumeSpec(spec string) (*patheval.Mount, bool) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false
	}
	host, container := parts[0], parts[1]
	if host == "" || container == "" {
		return nil, false
	}
	// Named volume (not a host path) — ignore; its container contents are
	// ephemeral from our perspective.
	if !strings.HasPrefix(host, "/") && !strings.HasPrefix(host, "~") && !strings.HasPrefix(host, "$") {
		return nil, true
	}
	if !filepath.IsAbs(container) {
		return nil, false
	}
	readOnly := false
	if len(parts) == 3 {
		for _, opt := range strings.Split(parts[2], ",") {
			if opt == "ro" || opt == "readonly" {
				readOnly = true
			}
		}
	}
	return &patheval.Mount{
		HostPath:      host,
		ContainerPath: container,
		ReadOnly:      readOnly,
	}, true
}

// parseMountFlag parses "type=bind,src=/h,dst=/c[,readonly|,ro=true]".
func parseMountFlag(spec string) (*patheval.Mount, bool) {
	kv := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		if part == "readonly" || part == "ro" {
			kv["readonly"] = "true"
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return nil, false
		}
		kv[part[:eq]] = part[eq+1:]
	}
	mtype := kv["type"]
	if mtype != "" && mtype != "bind" {
		// volume/tmpfs/etc — no host path mapping to track.
		return nil, true
	}
	src := kv["src"]
	if src == "" {
		src = kv["source"]
	}
	dst := kv["dst"]
	if dst == "" {
		dst = kv["destination"]
	}
	if dst == "" {
		dst = kv["target"]
	}
	if src == "" || dst == "" {
		return nil, false
	}
	if !filepath.IsAbs(dst) {
		return nil, false
	}
	readOnly := kv["readonly"] == "true" || kv["ro"] == "true"
	return &patheval.Mount{
		HostPath:      src,
		ContainerPath: dst,
		ReadOnly:      readOnly,
	}, true
}

// parseRunArgs skips flags (and --rm) to find the first positional arg (image/container)
// and returns it along with any remaining args (the command).
func parseRunArgs(args []string) (string, []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--rm" || a == "--detach" || a == "--privileged" || a == "--interactive" || a == "--tty" || isBooleanFlagCluster(a) {
			i++
			continue
		}
		if flagsWithValues[a] {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			if strings.Contains(a, "=") {
				i++
				continue
			}
			i++
			continue
		}
		// First non-flag is image/container
		if i+1 < len(args) {
			return a, args[i+1:]
		}
		return a, nil
	}
	return "", nil
}

// extractInnerCommand converts command args into an expression string.
// For "bash -c 'expr'" it extracts the expression; otherwise joins args.
func extractInnerCommand(cmdArgs []string) string {
	if len(cmdArgs) >= 3 && (cmdArgs[0] == "bash" || cmdArgs[0] == "sh") && cmdArgs[1] == "-c" {
		return strings.Join(cmdArgs[2:], " ")
	}
	return strings.Join(cmdArgs, " ")
}

func normalizeExpr(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// knownSafeDockerScripts lists script basenames that are always safe inside a
// docker container (e.g. container-only init scripts).
var knownSafeDockerScripts = map[string]bool{
	"init-firewall.sh": true,
}

// stripDockerPassthroughs pre-processes an inner command expression extracted
// from docker run/exec to strip docker-context-safe wrappers:
//   - gosu <user> <cmd...> → <cmd...>
//   - su <user> -s <shell> -c '<cmd>' → <cmd>
//   - init-firewall.sh (with any args) → true
//
// Compound expressions (&&, ||, ;) are split, each part processed, then rejoined.
func stripDockerPassthroughs(expr string) string {
	segments := splitOnShellOperators(expr)
	var buf strings.Builder
	for i, seg := range segments {
		if i > 0 {
			buf.WriteString(" " + seg.operator + " ")
		}
		buf.WriteString(stripSinglePassthrough(strings.TrimSpace(seg.command)))
	}
	return buf.String()
}

type shellSegment struct {
	command  string
	operator string // the operator that preceded this segment ("" for the first)
}

// splitOnShellOperators splits an expression on unquoted &&, ||, ; operators.
func splitOnShellOperators(expr string) []shellSegment {
	var result []shellSegment
	var buf strings.Builder
	inSingle, inDouble := false, false
	currentOp := ""
	i := 0
	for i < len(expr) {
		c := expr[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			buf.WriteByte(c)
			i++
		case c == '"' && !inSingle:
			inDouble = !inDouble
			buf.WriteByte(c)
			i++
		case c == '\\' && inDouble && i+1 < len(expr):
			buf.WriteByte(c)
			i++
			buf.WriteByte(expr[i])
			i++
		case inSingle || inDouble:
			buf.WriteByte(c)
			i++
		case i+1 < len(expr) && (expr[i:i+2] == "&&" || expr[i:i+2] == "||"):
			result = append(result, shellSegment{command: buf.String(), operator: currentOp})
			currentOp = expr[i : i+2]
			buf.Reset()
			i += 2
		case c == ';':
			result = append(result, shellSegment{command: buf.String(), operator: currentOp})
			currentOp = ";"
			buf.Reset()
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	result = append(result, shellSegment{command: buf.String(), operator: currentOp})
	return result
}

// stripSinglePassthrough strips a single docker passthrough wrapper from a
// command string, returning the inner command.
func stripSinglePassthrough(cmd string) string {
	if cmd == "" {
		return cmd
	}

	parsed := cmdparse.Parse(cmd)
	if len(parsed) == 0 {
		return cmd
	}
	pc := parsed[0]
	base := filepath.Base(pc.Executable)

	// gosu <user> <command...> → <command...>
	if base == "gosu" && len(pc.Args) >= 2 {
		// Args[0] is user, Args[1:] is the real command
		return strings.Join(pc.Args[1:], " ")
	}

	// su <user> -s <shell> -c '<command>' → <command>
	if base == "su" && len(pc.Args) >= 4 {
		// Look for -c flag pattern: su <user> -s <shell> -c <cmd>
		for i := 0; i < len(pc.Args)-1; i++ {
			if pc.Args[i] == "-c" {
				return strings.Join(pc.Args[i+1:], " ")
			}
		}
	}

	// init-firewall.sh → true
	if knownSafeDockerScripts[base] {
		return "true"
	}

	return cmd
}

func firstNonFlag(args []string) string {
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}
