package safecmds

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var alwaysSafe = map[string]bool{
	"echo": true, "test": true, "true": true, "false": true, "printf": true,
	"cut": true, "df": true, "ps": true, "tr": true, "where": true, "pgrep": true,
	"sleep": true, "tree": true,
	// Shell builtins and environment queries (no filesystem access)
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"which": true, "type": true, "command": true, "unset": true, "export": true,
	"env": true, "printenv": true, "id": true, "whoami": true,
	"date": true, "uname": true, "hostname": true, "pwd": true, "cd": true,
	"sw_vers": true,
	// macOS system tools (read-only inspection)
	"sfltool": true, "plutil": true, "system_profiler": true, "launchctl": true,
	"claude-extended-tool-approver": true, "claude-pretool-hook": true,
	"shellcheck": true, "colima": true, "contained-claude": true,
	"my-code-review-support-cli": true,
}

// browsingCmds list/stat filesystem entries but don't read file contents.
// Safe to run on any path since they only expose names, sizes, timestamps.
var browsingCmds = map[string]bool{
	"ls": true, "find": true, "fd": true, "du": true, "stat": true, "file": true,
	"lsof": true,
}

// safeReadCmds read file contents — require path to be in a known zone.
var safeReadCmds = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"wc": true, "diff": true,
	"sort": true, "uniq": true, "awk": true,
	"jq": true, "tq": true, "xxd": true,
	// strings dumps a file's printable content, so it reads file contents and
	// is routed through the same hasUnsafeReadPath zone check as cat/head/tail
	// (pg2-t76k8). Previously it hit the unknown-command fallthrough and abstained.
	"strings": true,
}

// logReadSubcommands are the macOS unified-logging verbs that only read; the
// mutating verbs (erase/config/collect) are NOT approved.
var logReadSubcommands = map[string]bool{
	"show": true, "stream": true, "stats": true,
}

var safeWriteCmds = map[string]bool{
	"rm": true, "cp": true, "mv": true,
	"mkdir": true, "touch": true, "chmod": true,
	"tee": true,
}

var lspServices = map[string]bool{
	"typescript-language-server": true, "gopls": true, "bash-language-server": true,
	"pylsp": true, "rust-analyzer": true,
}

type Rule struct {
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "safe-commands"
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
	if len(parsed) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	baseEval := r.eval
	if input.PathEval != nil {
		baseEval = input.PathEval
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = baseEval.ProjectRoot()
	}
	pe := baseEval.WithCWD(cwd)
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if alwaysSafe[basename] || lspServices[basename] {
			continue
		}
		if browsingCmds[basename] {
			if hasRejectPath(pc.Args, pe) {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references rejected path (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// "$command --help" or "$command $subcommand --help" is always safe
		if isHelpRequest(basename, pc.Args) {
			continue
		}
		// xargs: evaluate the inner command being run
		if basename == "xargs" {
			innerExec, innerArgs := extractXargsCommand(pc.Args)
			if innerExec == "" {
				return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
			}
			innerBase := filepath.Base(innerExec)
			// sh/bash -c '<cmd>': parse the -c argument and evaluate it recursively
			if (innerBase == "sh" || innerBase == "bash") && len(innerArgs) >= 2 && innerArgs[0] == "-c" {
				shellCmd := strings.Join(innerArgs[1:], " ")
				innerParsed := cmdparse.Parse(shellCmd)
				if len(innerParsed) == 0 {
					return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
				}
				// Re-evaluate by constructing a synthetic hook input with the shell command
				syntheticInput := &hookio.HookInput{
					ToolName:  "Bash",
					CWD:       cwd,
					ToolInput: mustMarshalCommand(shellCmd),
				}
				result := r.Evaluate(syntheticInput)
				if result.Decision != hookio.Approve {
					return result
				}
				continue
			}
			if alwaysSafe[innerBase] || lspServices[innerBase] {
				continue
			}
			if browsingCmds[innerBase] {
				if hasRejectPath(innerArgs, pe) {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references rejected path (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			// grep/rg: skip pattern arg before path checking
			if innerBase == "grep" || innerBase == "rg" {
				fileArgs := cmdparse.SkipGrepPattern(innerBase, innerArgs)
				if unsafe, path := hasUnsafeReadPath(fileArgs, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references unknown path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if safeReadCmds[innerBase] {
				if unsafe, path := hasUnsafeReadPath(innerArgs, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references unknown path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if safeWriteCmds[innerBase] {
				if unsafe, path := hasUnsafeWritePath(innerArgs, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			// Unknown inner command — abstain
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// bash/sh -n: syntax check only, no execution — safe read command
		if (basename == "bash" || basename == "sh") && hasBashSyntaxCheckFlag(pc.Args) {
			fileArgs := extractBashSyntaxCheckFiles(pc.Args)
			if unsafe, path := hasUnsafeReadPath(fileArgs, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " -n references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// unzip: read archive, optionally write to -d destination or cwd
		if basename == "unzip" {
			result := evaluateUnzip(pc.Args, pe, cwd, r.Name())
			if result.Decision != hookio.Approve {
				return result
			}
			continue
		}
		// jar: tf/xf are safe read operations
		if basename == "jar" {
			if len(pc.Args) >= 1 && (pc.Args[0] == "tf" || pc.Args[0] == "xf") {
				if unsafe, path := hasUnsafeReadPath(pc.Args[1:], pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: jar " + pc.Args[0] + " references unknown path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// log (macOS unified logging): show/stream/stats read; erase/config/
		// collect mutate — approve only the read verbs, defer the rest.
		if basename == "log" {
			sub := ""
			for _, a := range pc.Args {
				if !strings.HasPrefix(a, "-") {
					sub = a
					break
				}
			}
			if logReadSubcommands[sub] {
				continue
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// yq: read command unless -i/--inplace is present
		if basename == "yq" {
			if isYqInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: yq -i references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if unsafe, path := hasUnsafeReadPath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: yq references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// sed: read command unless -i/--in-place is present
		if basename == "sed" {
			if isSedInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: sed -i references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if unsafe, path := hasUnsafeReadPath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: sed references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// gofmt: read-only unless it WRITES. -l (list), -d (diff), -s (simplify),
		// -e (all errors) and bare/path/stdin forms only print to stdout; -w
		// rewrites files in place. A -w invocation is NOT approved (deferred to the
		// normal flow); a read-only invocation is treated like a read command —
		// path-like args must be in a readable zone (matches cat/sed/yq).
		if basename == "gofmt" {
			if isGofmtWrite(pc.Args) {
				return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
			}
			if unsafe, path := hasUnsafeReadPath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: gofmt references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// grep/rg: first non-flag arg is a pattern, not a file — skip it in path checks
		if basename == "grep" || basename == "rg" {
			fileArgs := cmdparse.SkipGrepPattern(basename, pc.Args)
			if unsafe, path := hasUnsafeReadPath(fileArgs, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// jq: skip value arguments for --arg, --argjson, --slurpfile, --rawfile
		// which take two args (name value) that may look like paths but aren't.
		if basename == "jq" {
			fileArgs := cmdparse.SkipJqValueFlags(pc.Args)
			if unsafe, path := hasUnsafeReadPath(fileArgs, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: jq references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		if safeReadCmds[basename] {
			if unsafe, path := hasUnsafeReadPath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// A write command with a dynamically-expanded path arg ($VAR / $(...) /
		// backtick) hides its real target from path evaluation (looksLikePath only
		// matches literal /, ./, ../, ~/). Defer such writes to Claude's prompt.
		// Scoped to write commands so read commands with $VAR (cat $f, ls $d) are
		// not needlessly gated; command substitution is also caught at the engine
		// choke point for all commands.
		if safeWriteCmds[basename] && argsHaveDynamicExpansion(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: " + basename + " has a dynamically-expanded path arg (deferred to claude-code)",
				Module:   r.Name(),
			}
		}
		if basename == "cp" {
			result := evaluateCp(pc.Args, pe, r.Name())
			if result.Decision != hookio.Approve {
				return result
			}
			continue
		}
		if safeWriteCmds[basename] {
			if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references non-writable path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// Unknown command - not our jurisdiction
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "safe-commands: all commands are safe",
		Module:   r.Name(),
	}
}

// hasSubcommands lists commands known to use subcommand syntax (e.g. "git log", "kubectl apply").
var hasSubcommands = map[string]bool{
	"git": true, "gh": true,
	"docker": true, "docker-compose": true, "podman": true,
	"kubectl": true,
	"nix":     true, "nix-env": true, "nix-store": true,
	"darwin-rebuild": true, "nixos-rebuild": true, "home-manager": true,
	"cargo": true, "go": true, "rustup": true,
	"npm": true, "yarn": true, "pnpm": true, "npx": true,
	"pip": true, "uv": true, "poetry": true,
	"gradle": true, "gradlew": true,
	"helm": true, "terraform": true, "aws": true, "gcloud": true,
	"bd": true,
}

// isHelpRequest returns true if the args represent a safe help invocation.
// Matches:
//   - "$command --help"
//   - "$command $subcommand --help" (if command has subcommands and subcommand starts with a letter)
//   - "$command help" (if command has subcommands)
//   - "$command help $subcommand" (if command has subcommands and subcommand starts with a letter)
func isHelpRequest(basename string, args []string) bool {
	if len(args) == 1 && args[0] == "--help" {
		return true
	}
	if len(args) == 2 && args[1] == "--help" && startsWithLetter(args[0]) && hasSubcommands[basename] {
		return true
	}
	// "help" subcommand form: "$command help" or "$command help $subcommand"
	if hasSubcommands[basename] && len(args) >= 1 && args[0] == "help" {
		if len(args) == 1 {
			return true
		}
		if len(args) == 2 && startsWithLetter(args[1]) {
			return true
		}
	}
	return false
}

// startsWithLetter returns true if s is non-empty and starts with an ASCII letter.
func startsWithLetter(s string) bool {
	if len(s) == 0 {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func looksLikePath(arg string) bool {
	return strings.HasPrefix(arg, "/") ||
		strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, "~/")
}

// argsHaveDynamicExpansion reports whether any non-flag arg contains a shell
// expansion ($VAR, ${VAR}, $(...), backtick) that would resolve a path at
// runtime, hiding it from static path evaluation.
func argsHaveDynamicExpansion(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.ContainsAny(a, "$`") {
			return true
		}
	}
	return false
}

// hasRejectPath returns true if any path-like arg is in a rejected zone.
func hasRejectPath(args []string, pe *patheval.PathEvaluator) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			if pe.Evaluate(a) == patheval.PathReject {
				return true
			}
		}
	}
	return false
}

// hasUnsafeReadPath returns (true, path) if any path-like arg is not in a readable zone.
// ReadOnly and ReadWrite paths are acceptable for read operations.
func hasUnsafeReadPath(args []string, pe *patheval.PathEvaluator) (bool, string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			if !pe.Evaluate(a).CanRead() {
				return true, a
			}
		}
	}
	return false, ""
}

// hasUnsafeWritePath returns (true, path) if any path-like arg is not in a writable zone.
// Only ReadWrite paths are acceptable for write operations.
func hasUnsafeWritePath(args []string, pe *patheval.PathEvaluator) (bool, string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			if !pe.Evaluate(a).CanWrite() {
				return true, a
			}
		}
	}
	return false, ""
}

// evaluateCp handles cp with source (read) and destination (write) semantics.
func evaluateCp(args []string, pe *patheval.PathEvaluator, module string) hookio.RuleResult {
	// Check for -t/--target-directory
	targetDir := ""
	for i, a := range args {
		if (a == "-t" || a == "--target-directory") && i+1 < len(args) {
			targetDir = args[i+1]
			break
		}
		if v, ok := strings.CutPrefix(a, "--target-directory="); ok {
			targetDir = v
			break
		}
	}

	if targetDir != "" {
		if looksLikePath(targetDir) && !pe.Evaluate(targetDir).CanWrite() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: cp target directory is not writable " + targetDir + " (deferred to claude-code)",
				Module:   module,
			}
		}
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if a == targetDir {
				continue
			}
			if looksLikePath(a) && !pe.Evaluate(a).CanRead() {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: cp source references non-readable path " + a + " (deferred to claude-code)",
					Module:   module,
				}
			}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}
	}

	// Standard mode: last path-like arg is destination (write), rest are sources (read)
	var pathArgs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			pathArgs = append(pathArgs, a)
		}
	}

	if len(pathArgs) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with no explicit paths", Module: module}
	}

	dest := pathArgs[len(pathArgs)-1]
	if !pe.Evaluate(dest).CanWrite() {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "safe-commands: cp destination is not writable " + dest + " (deferred to claude-code)",
			Module:   module,
		}
	}

	for _, src := range pathArgs[:len(pathArgs)-1] {
		if !pe.Evaluate(src).CanRead() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: cp source references non-readable path " + src + " (deferred to claude-code)",
				Module:   module,
			}
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}
}

// mustMarshalCommand creates a JSON ToolInput for a Bash command string.
func mustMarshalCommand(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// xargsValueFlags are xargs flags that consume the next argument as a value.
var xargsValueFlags = map[string]bool{
	"-I": true, "-L": true, "-n": true, "-P": true, "-d": true,
}

// xargsNoValueFlags are xargs flags that take no value.
var xargsNoValueFlags = map[string]bool{
	"-0": true, "--null": true,
	"-r": true, "--no-run-if-empty": true,
	"-t": true, "--verbose": true,
}

// extractXargsCommand skips xargs flags and returns the inner command executable
// and its remaining arguments. Returns ("", nil) if no command is found.
func extractXargsCommand(args []string) (string, []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			// Found the command executable
			return a, args[i+1:]
		}
		if xargsValueFlags[a] {
			i += 2 // skip flag and its value
			continue
		}
		if xargsNoValueFlags[a] {
			i++
			continue
		}
		// Could be a combined flag like -I{} or unknown flag — try to detect
		// -I with attached replacement string (e.g., -I{})
		for prefix := range xargsValueFlags {
			if strings.HasPrefix(a, prefix) && len(a) > len(prefix) {
				// Value is attached to the flag (e.g., -I{}, -n5, -d,)
				goto nextArg
			}
		}
		// Unknown flag — skip it
		i++
		continue
	nextArg:
		i++
	}
	return "", nil
}

// isYqInPlace returns true if args contain -i or --inplace.
func isYqInPlace(args []string) bool {
	for _, a := range args {
		if a == "-i" || a == "--inplace" {
			return true
		}
	}
	return false
}

// isSedInPlace returns true if args contain -i, -i<suffix>, or --in-place.
func isSedInPlace(args []string) bool {
	for _, a := range args {
		if a == "-i" || a == "--in-place" || (strings.HasPrefix(a, "-i") && !strings.HasPrefix(a, "-in")) {
			return true
		}
	}
	return false
}

// isGofmtWrite reports whether gofmt args request writing files in place.
// gofmt only mutates with -w (a bool flag); every other flag (-l, -d, -s, -e,
// -r) and bare/path/stdin invocations print to stdout and leave files
// untouched. Go's flag package treats -w and --w identically and accepts
// -w=<bool>, so all those forms are matched. Anything containing -w is
// conservatively classified as a write so a mutating invocation is NEVER
// approved (the rare -w=false read-only form Abstains, which is safe).
func isGofmtWrite(args []string) bool {
	for _, a := range args {
		if a == "-w" || a == "--w" ||
			strings.HasPrefix(a, "-w=") || strings.HasPrefix(a, "--w=") {
			return true
		}
	}
	return false
}

// unzipValueFlags lists unzip flags that consume the next argument.
var unzipValueFlags = map[string]bool{
	"-d": true, "-x": true, "-P": true,
}

// evaluateUnzip handles unzip with archive (read) and destination (write) semantics.
func evaluateUnzip(args []string, pe *patheval.PathEvaluator, cwd string, module string) hookio.RuleResult {
	var archivePath, destDir string
	readOnly := false // -l or -t means list/test only — no extraction

	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-l" || a == "-t" {
			readOnly = true
			i++
			continue
		}
		if a == "-d" && i+1 < len(args) {
			destDir = args[i+1]
			i += 2
			continue
		}
		if unzipValueFlags[a] && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if archivePath == "" {
			archivePath = a
		}
		i++
	}

	// Validate archive path is readable
	if archivePath != "" && looksLikePath(archivePath) {
		if !pe.Evaluate(archivePath).CanRead() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: unzip archive references unknown path " + archivePath + " (deferred to claude-code)",
				Module:   module,
			}
		}
	}

	// For read-only operations (-l, -t), no write check needed
	if readOnly {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip read-only operation", Module: module}
	}

	// Validate write destination
	writeDest := destDir
	if writeDest == "" {
		writeDest = cwd
	}
	if looksLikePath(writeDest) && !pe.Evaluate(writeDest).CanWrite() {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "safe-commands: unzip destination is not writable " + writeDest + " (deferred to claude-code)",
			Module:   module,
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip with known paths", Module: module}
}

// hasBashSyntaxCheckFlag returns true if args contain -n as a standalone flag.
func hasBashSyntaxCheckFlag(args []string) bool {
	for _, a := range args {
		if a == "-n" {
			return true
		}
	}
	return false
}

// extractBashSyntaxCheckFiles extracts file path arguments from bash -n args,
// skipping flags. Returns only path-like arguments for validation.
func extractBashSyntaxCheckFiles(args []string) []string {
	var files []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		files = append(files, a)
	}
	return files
}
