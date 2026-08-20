package docker

import (
	"fmt"
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("docker: read bash command: %w", err)
	}
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if basename != "docker" {
			continue
		}
		return r.evaluateDocker(pc, input)
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluateDocker(pc cmdparse.ParsedCommand, input *hookio.HookInput) (hookio.RuleResult, error) {
	args := pc.Args
	subcmd := firstNonFlag(args)
	if subcmd == "" {
		return hookio.NotApplicable()
	}

	if safeSubcommands[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "docker: docker " + subcmd + " is approved",
			Module:   r.Name(),
		}, nil
	}

	if subcmd == "run" {
		return r.evaluateRun(pc, input)
	}
	if subcmd == "exec" {
		return r.evaluateExec(pc, input)
	}

	return hookio.NotApplicable()
}

func (r *Rule) evaluateRun(pc cmdparse.ParsedCommand, input *hookio.HookInput) (hookio.RuleResult, error) {
	args := pc.Args
	// Find args after "run"
	runIdx := -1
	for i, a := range args {
		if a == "run" {
			runIdx = i
			break
		}
	}
	if runIdx < 0 {
		return hookio.NotApplicable()
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
		return hookio.NotApplicable()
	}

	// Parse past flags to find image and command
	image, cmdArgs := parseRunArgs(runArgs)
	if image == "" {
		return hookio.NotApplicable()
	}
	if len(cmdArgs) == 0 {
		return hookio.NotApplicable()
	}

	// Parse bind mounts from run args. Malformed mount syntax → abstain.
	mounts, ok := parseMounts(runArgs)
	if !ok {
		// ADR 0044 REFUSAL, not a not-applicable. This is a `docker run --rm` with an
		// image and an inner command — everything this rule needs to delegate — and the
		// ONE thing that stops it is a mount spec it could not parse. The unparseable
		// mount is precisely what makes the inner command's paths unjudgeable, so the
		// leaf is un-clearable BECAUSE this rule examined it, not because nobody did.
		// Reported as a not-applicable it reads as an EXHAUSTION, which is the half a
		// consumer may clear — the APPROVAL-WIDENING direction, and the worst possible
		// one for a container that mounts the host somewhere unknown.
		//
		// This is the same fail-safe reading as the engine's unparseable-expression floor
		// (ADR 0039's I1b): "I could not read this" is a floor, never an absence. The
		// chain still continues, so a later rule's Ask/Reject wins and only an Approve is
		// demoted.
		return hookio.Refused(r.Name(), "docker: unparseable mount spec")
	}

	// Resolve the inner command STRUCTURALLY (I13, pg2-lwwwk): bash/sh -c and
	// the docker-context-safe passthrough wrappers (gosu, su ... -c,
	// init-firewall.sh) are all resolved on already-tokenized args/leaves,
	// never by rejoining text and handing it back through EvaluateExpression.
	// See resolveInnerCommand's doc for why `source` is safe to use here even
	// though it is not, in every branch, a byte-exact slice of `pc.Raw`.
	source, leaves := resolveInnerCommand(pc, cmdArgs)

	scopedInput := r.withContainerEval(input, mounts)
	// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
	// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
	// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
	// hookio.FromRecursion states the translation in one place.
	//
	// stack is nil: the OLD single-frame recursion guard here compared the
	// OUTER docker leaf's own args (missing the leading "docker" token) against
	// the INNER extracted expression, which could never match — it was inert
	// decoration (pg2-lwwwk's own investigation; docker.go's prior revision
	// documented this at :167/:200). It is not replaced with a corrected
	// version: docker's OWN unwrapping (gosu/su/-c) is resolved entirely by
	// resolveInnerCommand's Go-level recursion BEFORE this call, bounded by the
	// finite text the caller wrote (a string cannot embed itself infinitely),
	// and any FURTHER nesting (a resolved leaf that is itself `docker run ...`)
	// re-enters this same function fresh, again bounded by the finite input.
	// No unbounded loop is reachable through this path, so there is nothing
	// for a stack frame to usefully guard here.
	return hookio.FromRecursion(r.exprEval.EvaluateStructure(source, leaves, nil, scopedInput))
}

func (r *Rule) evaluateExec(pc cmdparse.ParsedCommand, input *hookio.HookInput) (hookio.RuleResult, error) {
	args := pc.Args
	// Find args after "exec"
	execIdx := -1
	for i, a := range args {
		if a == "exec" {
			execIdx = i
			break
		}
	}
	if execIdx < 0 {
		return hookio.NotApplicable()
	}
	execArgs := args[execIdx+1:]

	// Skip flags, find container name, then command
	_, cmdArgs := parseRunArgs(execArgs)
	if len(cmdArgs) == 0 {
		return hookio.NotApplicable()
	}

	// See evaluateRun's comment: same structural resolution, same reasoning
	// for passing a nil stack.
	source, leaves := resolveInnerCommand(pc, cmdArgs)

	// docker exec cannot observe the container's mount list from the command
	// line; treat all inner paths as container-internal.
	scopedInput := r.withContainerEval(input, []patheval.Mount{})
	// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
	// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
	// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
	// hookio.FromRecursion states the translation in one place.
	return hookio.FromRecursion(r.exprEval.EvaluateStructure(source, leaves, nil, scopedInput))
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

// knownSafeDockerScripts lists script basenames that are always safe inside a
// docker container (e.g. container-only init scripts).
var knownSafeDockerScripts = map[string]bool{
	"init-firewall.sh": true,
}

// resolveInnerCommand is docker run/exec's I13 structural delegate builder
// (pg2-lwwwk): it turns cmdArgs — the ALREADY-TOKENIZED args after the
// image/container — into the (source, leaves) pair EvaluateStructure
// consumes, WITHOUT ever rejoining cmdArgs into text and handing that text
// back through EvaluateExpression (the defect this bead removes: the old
// stripDockerPassthroughs/splitOnShellOperators pipeline did exactly that,
// and a rejoin that lost quoting — e.g. `gosu u sh -c "a; b"` — silently
// promoted `b` out of the `-c` script it belonged to).
//
// cmdArgs is resolved structurally in two steps:
//  1. unwrapPassthroughs strips docker-context-safe wrapper PREFIXES (gosu,
//     su ... -c, init-firewall.sh) by reslicing already-tokenized args —
//     never a join.
//  2. If what remains is `bash -c <script>` / `sh -c <script>`, <script> is
//     cmdArgs[2] — a REAL value already sitting in cmdArgs, never text this
//     function builds — and is parsed via cmdparse.Parse exactly once. That
//     is a FIRST-time interpretation of a shell word's own value as the
//     script it names (the same category as a heredoc body or a command
//     substitution's body), not a re-parse of anything already parsed (I7).
//     resolveScriptLeaves then resolves passthrough-wrapping recursively on
//     the resulting leaves, since a `-c` script can itself contain more of
//     the same wrappers.
//
// source is the exact value leaves were lowered from when step 2 fires
// (I12: <script> itself, untouched). When cmdArgs is a direct exec with no
// `-c` script at all — docker execs argv directly; no shell parses it, so
// there is no script text to point `source` at — pc.Raw (this rule's own
// leaf's exact raw text, a genuinely real slice, never text this function
// built) is used instead. Both branches are real, never-rejoined values;
// there is no third branch that fabricates one.
func resolveInnerCommand(pc cmdparse.ParsedCommand, cmdArgs []string) (string, []cmdparse.ParsedCommand) {
	stripped := unwrapPassthroughs(cmdArgs)

	if script, ok := scriptArg(stripped); ok {
		return script, resolveScriptLeaves(cmdparse.Parse(script))
	}

	if len(stripped) == 0 {
		return pc.Raw, nil
	}
	return pc.Raw, []cmdparse.ParsedCommand{{Executable: stripped[0], Args: stripped[1:]}}
}

// scriptArg reports whether cmdArgs is a `bash -c <script>` / `sh -c
// <script>` invocation and, if so, returns <script> — a REAL element of
// cmdArgs, never text built or mutated by this package (I12/I13).
func scriptArg(cmdArgs []string) (string, bool) {
	if len(cmdArgs) >= 3 && (cmdArgs[0] == "bash" || cmdArgs[0] == "sh") && cmdArgs[1] == "-c" {
		return cmdArgs[2], true
	}
	return "", false
}

// unwrapPassthroughs repeatedly strips docker-context-safe passthrough
// wrappers from cmdArgs (see stripPassthroughWrapper) until none match,
// operating purely on already-tokenized args and never on text.
func unwrapPassthroughs(cmdArgs []string) []string {
	for {
		stripped, matched := stripPassthroughWrapper(cmdArgs)
		if !matched {
			return cmdArgs
		}
		cmdArgs = stripped
	}
}

// stripPassthroughWrapper strips ONE docker-context-safe passthrough wrapper
// from cmdArgs — an already-tokenized executable+args list — returning the
// wrapped command's own remaining tokens UNCHANGED: no join, no re-quoting,
// no mutation of any token's value (this is what closes out the defect in
// the deleted stripSinglePassthrough, which re-emitted these same tokens via
// strings.Join, corrupting any quoting they carried):
//
//   - gosu <user> <command...> → <command...>
//   - su <user> ... -c <command...> → <command...>
//   - init-firewall.sh (any args) → true (no further args)
//
// ok is false, and cmdArgs is returned UNCHANGED, when nothing matched.
func stripPassthroughWrapper(cmdArgs []string) ([]string, bool) {
	if len(cmdArgs) == 0 {
		return cmdArgs, false
	}
	base := filepath.Base(cmdArgs[0])

	// gosu <user> <command...> → <command...>
	if base == "gosu" && len(cmdArgs) >= 3 {
		return cmdArgs[2:], true
	}

	// su <user> -s <shell> -c '<command>' → <command>
	if base == "su" && len(cmdArgs) >= 5 {
		for i := 1; i < len(cmdArgs)-1; i++ {
			if cmdArgs[i] == "-c" {
				return cmdArgs[i+1:], true
			}
		}
	}

	// init-firewall.sh → true
	if knownSafeDockerScripts[base] {
		return []string{"true"}, true
	}

	return cmdArgs, false
}

// resolveScriptLeaves applies passthrough-wrapper resolution to each of a
// parsed script's own top-level leaves. A `-c` script can itself split into
// several leaves on &&/||/; (cmdparse.Parse's own splitCompound, quote-aware
// and run exactly once), and each leaf may independently be wrapped — e.g.
// the second half of `init-firewall.sh && gosu claude ls`. No leaf's text is
// ever rejoined here.
func resolveScriptLeaves(leaves []cmdparse.ParsedCommand) []cmdparse.ParsedCommand {
	resolved := make([]cmdparse.ParsedCommand, 0, len(leaves))
	for _, leaf := range leaves {
		resolved = append(resolved, resolveLeaf(leaf)...)
	}
	return resolved
}

// resolveLeaf structurally resolves ONE parsed leaf's own passthrough
// wrapping, recursively (a wrapper may itself wrap another wrapper, or a
// nested bash/sh -c script). It never emits text: gosu/su unwrapping
// reslices the leaf's own already-parsed Args, and init-firewall.sh replaces
// the leaf's Executable/Args directly, leaving every OTHER field of the leaf
// (Redirections, Heredocs, PipelineID, ...) untouched. A nested `-c` script's
// value is parsed via cmdparse.Parse on its own real text (never a rejoin),
// which is why this can return MORE than one leaf.
func resolveLeaf(leaf cmdparse.ParsedCommand) []cmdparse.ParsedCommand {
	cmdArgs := unwrapPassthroughs(append([]string{leaf.Executable}, leaf.Args...))

	if script, ok := scriptArg(cmdArgs); ok {
		return resolveScriptLeaves(cmdparse.Parse(script))
	}

	if len(cmdArgs) == 0 {
		return nil
	}
	leaf.Executable = cmdArgs[0]
	leaf.Args = cmdArgs[1:]
	return []cmdparse.ParsedCommand{leaf}
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
