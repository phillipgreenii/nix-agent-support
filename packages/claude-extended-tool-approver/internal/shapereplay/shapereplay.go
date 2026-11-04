// Package shapereplay replays one decision-DB row through the NEW effect
// engine (claudecodeadapter.Evaluate) and reports whether the verdict
// depended on live filesystem, config or env state (Phase 4, docket
// tc-o14i5.5, packet tc-o14i5.5.2).
//
// # State-dependence detection (the packet's freedom boundary)
//
// The engine reads state from dozens of sites (pathspec, patheval, specfmt,
// userconfig, git subprocesses), so instrumenting them would miss a site the
// day one is added. This package instead REPLAYS THE SAME INPUT UNDER THREE
// HERMETIC TWINS and diffs the decision against the live run:
//
//   - env: the process environment is cleared down to HOME and PATH
//     (PATH only so the engine's git probes still start). A flip means the
//     verdict read an environment variable (TMPDIR, XDG_*, MONOREPO_ROOT, ...).
//   - config: HOME is pointed at an empty directory and every occurrence of
//     the real HOME in the input is rewritten to it, so ~/.claude settings,
//     the user config and the spec repository all read as absent. A flip
//     means the verdict read operator configuration.
//   - filesystem: the live project root (patheval.DetectProjectRoot) is
//     replaced by an empty directory tree (no .git, no symlinks, no files) and
//     every occurrence of the live root in the cwd and the tool input is
//     rewritten to it. A flip means the verdict read the project's
//     filesystem state: worktree markers, git tracking, symlinks, existing
//     files. When the live root is "/" or contains HOME, relocating it would
//     also relocate HOME-relative zones and manufacture flips, so the
//     filesystem twin is skipped for that row (documented limitation; the
//     env and config twins still run).
//
// Only the DECISION is compared: a reason that merely mentions a different
// path is not a verdict flip. An engine error in a twin where the live run
// succeeded counts as a flip (conservative: it can only add a tag).
//
// The twins mutate the process environment, so a Replayer is NOT safe for
// concurrent use.
package shapereplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// State dimensions a verdict can depend on, in the order they are reported.
const (
	StateFilesystem = "filesystem"
	StateConfig     = "config"
	StateEnv        = "env"
)

// Input is the part of a decision-DB row replay needs.
type Input struct {
	ToolName       string
	ToolInputJSON  string
	CWD            string
	PermissionMode string
}

// Result is the new engine's verdict for one Input.
type Result struct {
	// Decision is approve, reject or abstain (the new engine never emits ask;
	// a reserved Ask is reported as abstain).
	Decision string
	// Reason is the engine reason, or the adapter error message when the
	// adapter does not model the tool.
	Reason string
	// Unmapped is true when the adapter returned an error because it does not
	// model Input.ToolName at all.
	Unmapped bool
	// Consulted lists the state dimensions whose hermetic twin flipped the
	// decision, drawn from StateFilesystem, StateConfig, StateEnv in that
	// order. Empty (non-nil) when the verdict is state-independent.
	Consulted []string
}

// Replayer holds the engine inputs that do not depend on the row.
type Replayer struct {
	reg           cmddesc.Registry
	policies      []effectpolicy.Policy
	graphPolicies []effectpolicy.GraphPolicy
}

// New builds a Replayer exactly as evaluate --corpus builds its engine.
func New() *Replayer {
	return &Replayer{
		reg:           cmddesc.DefaultRegistry(),
		policies:      effectpolicy.DefaultPolicies(),
		graphPolicies: effectpolicy.DefaultGraphPolicies(),
	}
}

// Mapped reports whether the adapter models toolName.
func Mapped(toolName string) bool {
	return toolName == "Bash" ||
		claudecodeadapter.IsFileEditTool(toolName) ||
		claudecodeadapter.IsReadSearchTool(toolName)
}

func (r *Replayer) evalOnce(in Input, projectRoot string) (evalcontract.Response, error) {
	hi := &hookio.HookInput{
		ToolName:       in.ToolName,
		ToolInput:      json.RawMessage(in.ToolInputJSON),
		CWD:            in.CWD,
		PermissionMode: in.PermissionMode,
	}
	cfg := claudecodeadapter.RequestConfig{ProjectRoot: projectRoot}
	return claudecodeadapter.Evaluate(hi, cfg, r.reg, r.policies, r.graphPolicies)
}

func decisionName(d evalcontract.Decision) string {
	switch d {
	case evalcontract.Approve:
		return "approve"
	case evalcontract.Reject:
		return "reject"
	default:
		return "abstain"
	}
}

// Replay evaluates in against the live state and the three hermetic twins.
func (r *Replayer) Replay(in Input) Result {
	resp, err := r.evalOnce(in, "")
	if err != nil {
		// The adapter has no opinion: ABSTAIN. No state was consulted that a
		// twin could change, so no twin runs.
		return Result{
			Decision:  "abstain",
			Reason:    err.Error(),
			Unmapped:  !Mapped(in.ToolName),
			Consulted: []string{},
		}
	}
	base := decisionName(resp.Decision)
	res := Result{Decision: base, Reason: resp.Reason, Consulted: []string{}}

	flipped := func(d string, derr error) bool { return derr != nil || d != base }

	if d, derr := r.fsTwin(in); d != "" || derr != nil {
		if flipped(d, derr) {
			res.Consulted = append(res.Consulted, StateFilesystem)
		}
	}
	if d, derr := r.configTwin(in); flipped(d, derr) {
		res.Consulted = append(res.Consulted, StateConfig)
	}
	if d, derr := r.envTwin(in); flipped(d, derr) {
		res.Consulted = append(res.Consulted, StateEnv)
	}
	return res
}

// envTwin replays under an environment cleared down to HOME and PATH.
func (r *Replayer) envTwin(in Input) (string, error) {
	saved := os.Environ()
	home, path := os.Getenv("HOME"), os.Getenv("PATH")
	os.Clearenv()
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("PATH", path)
	defer func() {
		os.Clearenv()
		for _, kv := range saved {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
	}()
	resp, err := r.evalOnce(in, "")
	if err != nil {
		return "", err
	}
	return decisionName(resp.Decision), nil
}

// configTwin replays with HOME pointing at an empty directory.
func (r *Replayer) configTwin(in Input) (string, error) {
	realHome := os.Getenv("HOME")
	tmp, err := os.MkdirTemp("", "shapereplay-home-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	twinHome := filepath.Join(tmp, "home")
	if err := os.MkdirAll(twinHome, 0o755); err != nil {
		return "", err
	}
	_ = os.Setenv("HOME", twinHome)
	defer func() { _ = os.Setenv("HOME", realHome) }()

	twin := in
	if realHome != "" && realHome != "/" {
		twin.ToolInputJSON = rewritePrefix(in.ToolInputJSON, realHome, twinHome)
		twin.CWD = rewritePrefix(in.CWD, realHome, twinHome)
	}
	resp, err := r.evalOnce(twin, "")
	if err != nil {
		return "", err
	}
	return decisionName(resp.Decision), nil
}

// fsTwin replays with the live project root replaced by an empty tree. It
// returns ("", nil) when the twin does not apply to this row.
func (r *Replayer) fsTwin(in Input) (string, error) {
	if in.CWD == "" || !filepath.IsAbs(in.CWD) {
		return "", nil
	}
	root := patheval.DetectProjectRoot(in.CWD)
	home := os.Getenv("HOME")
	if root == "" || root == "/" || home == "" ||
		root == home || strings.HasPrefix(home+"/", root+"/") {
		return "", nil
	}
	rel, err := filepath.Rel(root, filepath.Clean(in.CWD))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", nil
	}
	tmp, err := os.MkdirTemp("", "shapereplay-fs-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	twinRoot := filepath.Join(tmp, "proj")
	twinCWD := filepath.Join(twinRoot, rel)
	if err := os.MkdirAll(twinCWD, 0o755); err != nil {
		return "", err
	}
	twin := in
	twin.CWD = twinCWD
	twin.ToolInputJSON = rewritePrefix(in.ToolInputJSON, root, twinRoot)
	resp, err := r.evalOnce(twin, twinRoot)
	if err != nil {
		return "", err
	}
	return decisionName(resp.Decision), nil
}

// rewritePrefix replaces every occurrence of old in s that is a whole path
// prefix (the next byte is end-of-string or not a path-name character) with
// repl, so "/ws/repo" does not rewrite "/ws/repo2".
func rewritePrefix(s, old, repl string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(old)
		if end < len(s) && isNameByte(s[end]) {
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s = s[end:]
	}
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == '-'
}
