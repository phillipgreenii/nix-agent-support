// Package pggwmworktree denies creating a git worktree outside pg-gwm in a
// repository pg-gwm manages, unless the caller states a logged exception reason.
//
// # Why
//
// Using pg-gwm for the ZR monorepo's worktrees is the norm and any exception
// MUST be explicit. Before this rule nothing stopped an agent from creating a raw
// worktree: claudetools auto-approves EnterWorktree and the git rule approves
// `git worktree add`.
//
// # What it denies (Reject), when the target repository is listed in the pg-gwm
// configuration
//
//	EnterWorktree                    (repository = the session cwd's)
//	Agent with isolation:"worktree"  (repository = the session cwd's)
//	git worktree add                 plain, `git -C <dir>`, after `cd <dir> &&`,
//	                                 and as a leaf of any compound command
//	bin/worktree create              (repository = the one enclosing the script,
//	                                 resolved from the command's cwd)
//
// `git worktree list|remove|prune|...` and every other command stay untouched.
//
// # The exception marker, per tool
//
// A hook sees the Claude session's environment, NOT the environment of the Bash
// command it judges, so the marker differs by tool:
//
//	git worktree add, bin/worktree create  a command-text prefix,
//	    PG_GWM_EXCEPTION='<reason>' git worktree add ...
//	    read from the parsed command's environment assignments.
//	EnterWorktree, Agent isolation:worktree  these carry no environment, so the
//	    marker is the PG_GWM_EXCEPTION variable of the SESSION, read from the hook
//	    process's own environment.
//
// An empty (or whitespace) reason is a deny. A honored exception is logged and
// counted (observe.go) and the rule steps aside so the rest of the chain judges
// the command as before.
//
// # Standing exceptions
//
// A `git worktree add <path>` whose path matches an entry of the repository's
// pg-gwm exceptions list is allowed without a marker (no log line: it is a
// standing, reviewed exception, the same source the weekly audit reads).
//
// # Repository detection
//
// The rule reads the pg-gwm configuration file directly (config.go) rather than
// running `pg-gwm config check`: the hook runs on every Bash call and a
// subprocess per judgment is not affordable, and the file is the single source
// either path would read. A repository is "listed" when the file has a
// [repos."<main checkout>"] table whose key names the repository's main checkout.
// An unreadable or unparsable file FAILS OPEN (the rule steps aside) and warns on
// stderr; a missing file means no repository is managed.
//
// A `git -C "$VAR"` whose directory the command text cannot establish falls back
// to the best-effort directory (the running cwd), like primary-commit's lookup
// side.
package pggwmworktree

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
)

// ExceptionEnv is the exception marker's name.
const ExceptionEnv = "PG_GWM_EXCEPTION"

// Tool labels used in the exception log and counters.
const (
	toolEnterWorktree = "EnterWorktree"
	toolAgent         = "Agent"
	toolGitAdd        = "git worktree add"
	toolBinWorktree   = "bin/worktree create"
)

// Rule is the pg-gwm worktree deny rule.
type Rule struct {
	configPath string
	stateDir   string
	getenv     func(string) string
	warnTo     io.Writer
	clock      func() time.Time
}

// New builds the production rule: default config path, default state directory,
// the hook process's environment, warnings to stderr.
func New() *Rule {
	return &Rule{
		configPath: defaultConfigPath(),
		stateDir:   defaultStateDir(),
		getenv:     os.Getenv,
		warnTo:     os.Stderr,
	}
}

// NewWith builds a rule with explicit dependencies (tests). Nil getenv reads
// nothing; nil warnTo discards warnings; empty stateDir disables the log.
func NewWith(configPath, stateDir string, getenv func(string) string, warnTo io.Writer, clock func() time.Time) *Rule {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if warnTo == nil {
		warnTo = io.Discard
	}
	return &Rule{configPath: configPath, stateDir: stateDir, getenv: getenv, warnTo: warnTo, clock: clock}
}

func (r *Rule) Name() string { return "pg-gwm-worktree" }

func (r *Rule) warn(format string, a ...any) {
	fmt.Fprintf(r.warnTo, "pg-gwm-worktree: warning: "+format+"\n", a...)
}

// target is one worktree-creating call found in the input.
type target struct {
	tool string
	// dir is where the call runs / the repository is looked up from.
	dir string
	// path is the worktree path being created, "" when unknown.
	path string
	// marker reports the exception reason and whether the marker is present.
	reason  string
	marked  bool
	session bool // marker came from the session environment
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	var t *target
	switch input.ToolName {
	case "EnterWorktree":
		t = r.sessionTarget(input, toolEnterWorktree)
	case "Agent":
		var in struct {
			Isolation string `json:"isolation"`
		}
		if json.Unmarshal(input.ToolInput, &in) != nil || in.Isolation != "worktree" {
			return hookio.NotApplicable()
		}
		t = r.sessionTarget(input, toolAgent)
	case "Bash":
		leaves, err := hookio.LeavesOf(input)
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("pg-gwm-worktree: read bash command: %w", err)
		}
		for i, pc := range leaves {
			if c := r.bashTarget(input, leaves, i, pc); c != nil {
				if res, done := r.judge(input, c); done {
					return res, nil
				}
			}
		}
		return hookio.NotApplicable()
	default:
		return hookio.NotApplicable()
	}
	if res, done := r.judge(input, t); done {
		return res, nil
	}
	return hookio.NotApplicable()
}

func (r *Rule) sessionTarget(input *hookio.HookInput, tool string) *target {
	reason := r.getenv(ExceptionEnv)
	return &target{tool: tool, dir: input.CWD, reason: reason, marked: reason != "", session: true}
}

// bashTarget classifies one leaf; nil when it creates no worktree.
func (r *Rule) bashTarget(input *hookio.HookInput, leaves []cmdparse.ParsedCommand, i int, pc cmdparse.ParsedCommand) *target {
	base := filepath.Base(pc.Executable)
	var t *target
	switch {
	case base == "git":
		chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
		if subcmd != "worktree" || len(rest) == 0 || rest[0] != "add" {
			return nil
		}
		res := primarycommit.ResolveDir(input.CWD, chdirs, primarycommit.LeafVars(input.InCommandVars, leaves, i))
		t = &target{tool: toolGitAdd, dir: res.Dir}
		if p := worktreeAddPath(rest[1:]); p != "" {
			if !filepath.IsAbs(p) {
				p = filepath.Join(res.Dir, p)
			}
			t.path = filepath.Clean(p)
		}
	case base == "worktree" && len(pc.Args) > 0 && pc.Args[0] == "create" && isBinPath(pc.Executable):
		exe := pc.Executable
		if !filepath.IsAbs(exe) {
			exe = filepath.Join(input.CWD, exe)
		}
		t = &target{tool: toolBinWorktree, dir: filepath.Dir(filepath.Clean(exe))}
	default:
		return nil
	}
	for _, ev := range pc.EnvVars {
		if ev.Name == ExceptionEnv {
			t.marked, t.reason = true, unquoteShell(ev.Value)
		}
	}
	return t
}

// unquoteShell strips one pair of surrounding shell quotes: cmdparse keeps an
// assignment's value as written (PG_GWM_EXCEPTION='a b' gives "'a b'").
func unquoteShell(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// isBinPath reports whether the executable is spelled as <something>/bin/worktree
// or bin/worktree (repo-relative), the repository's own script.
func isBinPath(exe string) bool {
	return exe == "bin/worktree" || strings.HasSuffix(exe, "/bin/worktree")
}

// worktreeAddPath returns the <path> operand of `git worktree add` (args after
// "add"): the first positional, skipping options and the values of -b/-B/--reason.
func worktreeAddPath(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case a == "-b" || a == "-B" || a == "--reason":
			i++
		case strings.HasPrefix(a, "-"):
			// flag without a separate value (-f, --detach, --lock, --orphan, --reason=..)
		default:
			return a
		}
	}
	return ""
}

// judge returns (result, true) when the target decides the verdict (a deny, or an
// honored exception stepping aside is NOT decisive: it returns done=false).
func (r *Rule) judge(input *hookio.HookInput, t *target) (hookio.RuleResult, bool) {
	main, ok := mainCheckout(t.dir)
	if !ok {
		return hookio.RuleResult{}, false
	}
	cfg, err := loadConfig(r.configPath)
	if err != nil {
		r.warn("cannot read pg-gwm config %s, failing open: %v", r.configPath, err)
		return hookio.RuleResult{}, false
	}
	excs, listed := cfg.repos[main]
	if !listed {
		return hookio.RuleResult{}, false
	}
	if t.path != "" {
		for _, e := range excs {
			if e.matches(t.path) {
				return hookio.RuleResult{}, false
			}
		}
	}
	repo := filepath.Base(main)
	if t.marked && strings.TrimSpace(t.reason) != "" {
		r.recordException(exceptionRecord{
			TS: r.now(), SessionID: input.SessionID, Tool: t.tool, Repo: main, Reason: strings.TrimSpace(t.reason), CWD: input.CWD,
		})
		return hookio.RuleResult{}, false
	}
	r.recordDeny()
	return hookio.RuleResult{Decision: hookio.Reject, Reason: denyMessage(repo, t), Module: r.Name()}, true
}

func denyMessage(repo string, t *target) string {
	msg := fmt.Sprintf("Denied: %s is managed by pg-gwm. Run p=$(pg-gwm lease --name <name> -b <branch> --print=path) "+
		"and work in that path (skill pg-gwm:pg-gwm). ", repo)
	how := "prefix the command with " + ExceptionEnv + "=<reason>."
	if t.session {
		how = "set " + ExceptionEnv + "=<reason> in the session environment."
	}
	msg += "If a raw worktree is truly required, " + how
	if t.marked && strings.TrimSpace(t.reason) == "" {
		msg += " (" + ExceptionEnv + " is set but its reason is empty; an empty reason is a deny.)"
	}
	return msg
}
