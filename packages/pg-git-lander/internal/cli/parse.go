// Package cli is the command-line surface of pg-git-lander: argument parsing
// (design section 5.1), the PGL_* input variables (section 11.1), dispatch to
// the pipeline and the printing of exactly one outcome (section 6.1).
package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Kind is the subcommand.
type Kind string

// The subcommands.
const (
	KindRebase      Kind = "rebase"
	KindLand        Kind = "land"
	KindStatus      Kind = "status"
	KindLog         Kind = "log"
	KindConfigCheck Kind = "config-check"
	KindVersion     Kind = "version"
	KindHelp        Kind = "help"
)

// Synopsis is the usage text of section 5.1.
const Synopsis = `usage:
  pg-git-lander rebase BRANCH --onto REF [--source-worktree PATH] [--work-worktree PATH]
                       [--wait DURATION] [--context KEY=VALUE]... [--json]
  pg-git-lander land   BRANCH [--into TARGET] [--target-worktree PATH] [--source-worktree PATH] [--work-worktree PATH]
                       [--keep-source-worktree] [--keep-source-branch]
                       [--wait DURATION] [--context KEY=VALUE]... [--json]
  pg-git-lander rebase [BRANCH] --continue | --skip | --abort | --quit [--wait DURATION] [--json]
  pg-git-lander land   [BRANCH] --continue | --skip | --abort | --quit [--wait DURATION] [--json]
  pg-git-lander status [BRANCH] [--json]
  pg-git-lander log    OP_ID | BRANCH [--tail N]
  pg-git-lander config check [--file PATH] [--json]
every form accepts --repo PATH`

// Command is one parsed invocation.
type Command struct {
	Kind Kind
	// Repo is the --repo value; empty when the flag was not used.
	Repo string
	JSON bool

	// Branch is BRANCH (rebase, land, status); for log it is the OP_ID or
	// BRANCH argument.
	Branch string
	Onto   string
	// Into is --into; empty means the repo's primary branch.
	Into string
	// Resume is the resume flag (outcome.FlagContinue and so on); empty for a
	// fresh operation.
	Resume string

	TargetWorktree, SourceWorktree, WorkWorktree string
	KeepSourceWorktree, KeepSourceBranch         bool
	Wait                                         *time.Duration
	Context                                      []hooks.ContextPair

	// File is config check's --file; Tail is log's --tail (-1 when absent).
	File string
	Tail int
}

// UsageError is a command line that does not parse (exit 2).
type UsageError struct {
	Msg string
	// JSON is true when --json was among the arguments.
	JSON bool
}

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) *UsageError { return &UsageError{Msg: fmt.Sprintf(format, a...)} }

type flagSpec struct {
	value bool
	// fresh marks the flags a resume does not accept.
	fresh bool
	kinds map[Kind]bool
}

func kinds(ks ...Kind) map[Kind]bool {
	m := map[Kind]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

var flagSpecs = map[string]flagSpec{
	"--repo":                 {value: true, kinds: kinds(KindRebase, KindLand, KindStatus, KindLog, KindConfigCheck)},
	"--json":                 {kinds: kinds(KindRebase, KindLand, KindStatus, KindConfigCheck)},
	"--wait":                 {value: true, kinds: kinds(KindRebase, KindLand)},
	"--onto":                 {value: true, fresh: true, kinds: kinds(KindRebase)},
	"--into":                 {value: true, fresh: true, kinds: kinds(KindLand)},
	"--target-worktree":      {value: true, fresh: true, kinds: kinds(KindLand)},
	"--source-worktree":      {value: true, fresh: true, kinds: kinds(KindRebase, KindLand)},
	"--work-worktree":        {value: true, fresh: true, kinds: kinds(KindRebase, KindLand)},
	"--keep-source-worktree": {fresh: true, kinds: kinds(KindLand)},
	"--keep-source-branch":   {fresh: true, kinds: kinds(KindLand)},
	"--context":              {value: true, fresh: true, kinds: kinds(KindRebase, KindLand)},
	"--continue":             {kinds: kinds(KindRebase, KindLand)},
	"--skip":                 {kinds: kinds(KindRebase, KindLand)},
	"--abort":                {kinds: kinds(KindRebase, KindLand)},
	"--quit":                 {kinds: kinds(KindRebase, KindLand)},
	"--file":                 {value: true, kinds: kinds(KindConfigCheck)},
	"--tail":                 {value: true, kinds: kinds(KindLog)},
}

// Parse parses the arguments after the program name.
func Parse(args []string) (*Command, *UsageError) {
	hasJSON := false
	for _, a := range args {
		if a == "--json" {
			hasJSON = true
		}
	}
	c, err := parse(args)
	if err != nil {
		err.JSON = hasJSON
	}
	return c, err
}

func parse(args []string) (*Command, *UsageError) {
	if len(args) == 0 {
		return nil, usagef("a subcommand is required")
	}
	c := &Command{Tail: -1}
	rest := args[1:]
	switch args[0] {
	case "--version", "version":
		if len(rest) != 0 {
			return nil, usagef("%s takes no arguments", args[0])
		}
		c.Kind = KindVersion
		return c, nil
	case "--help", "-h", "help":
		c.Kind = KindHelp
		return c, nil
	case "rebase":
		c.Kind = KindRebase
	case "land":
		c.Kind = KindLand
	case "status":
		c.Kind = KindStatus
	case "log":
		c.Kind = KindLog
	case "config":
		if len(rest) == 0 || rest[0] != "check" {
			return nil, usagef("config takes the subcommand check")
		}
		c.Kind = KindConfigCheck
		rest = rest[1:]
	default:
		return nil, usagef("unknown subcommand %q", args[0])
	}

	var positional []string
	var resumeFlags []string
	var freshFlags []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--help" || a == "-h" {
			return &Command{Kind: KindHelp}, nil
		}
		if !strings.HasPrefix(a, "--") {
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				return nil, usagef("unknown flag %s", a)
			}
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		spec, ok := flagSpecs[name]
		if !ok || !spec.kinds[c.Kind] {
			return nil, usagef("%s does not take the flag %s", displayKind(c.Kind), name)
		}
		if spec.value {
			if !hasVal {
				if i+1 >= len(rest) {
					return nil, usagef("%s needs a value", name)
				}
				i++
				val = rest[i]
			}
		} else if hasVal {
			return nil, usagef("%s takes no value", name)
		}
		if spec.fresh {
			freshFlags = append(freshFlags, name)
		}
		if err := c.set(name, val); err != nil {
			return nil, err
		}
		switch name {
		case outcome.FlagContinue, outcome.FlagSkip, outcome.FlagAbort, outcome.FlagQuit:
			resumeFlags = append(resumeFlags, name)
		}
	}

	if len(resumeFlags) > 1 {
		return nil, usagef("%s and %s cannot be combined", resumeFlags[0], resumeFlags[1])
	}
	if len(resumeFlags) == 1 {
		c.Resume = resumeFlags[0]
		if len(freshFlags) > 0 {
			return nil, usagef("%s is not accepted with %s: a resumed operation keeps its original context, roles and flags", freshFlags[0], c.Resume)
		}
	}

	switch c.Kind {
	case KindRebase, KindLand:
		if len(positional) > 1 {
			return nil, usagef("unexpected argument %q", positional[1])
		}
		if len(positional) == 1 {
			c.Branch = positional[0]
		}
		if c.Resume != "" {
			return c, nil
		}
		if c.Branch == "" {
			return nil, usagef("%s needs BRANCH", c.Kind)
		}
		if c.Kind == KindRebase && c.Onto == "" {
			return nil, usagef("rebase needs --onto REF")
		}
		if c.Kind == KindLand && c.Into != "" && c.Into == c.Branch {
			return nil, usagef("BRANCH and TARGET are both %s", c.Branch)
		}
		if c.Kind == KindRebase && (c.KeepSourceWorktree || c.KeepSourceBranch) {
			return nil, usagef("--keep-source-worktree and --keep-source-branch are not accepted with rebase")
		}
	case KindStatus:
		if len(positional) > 1 {
			return nil, usagef("unexpected argument %q", positional[1])
		}
		if len(positional) == 1 {
			c.Branch = positional[0]
		}
	case KindLog:
		if len(positional) != 1 {
			return nil, usagef("log needs OP_ID or BRANCH")
		}
		c.Branch = positional[0]
	case KindConfigCheck:
		if len(positional) != 0 {
			return nil, usagef("unexpected argument %q", positional[0])
		}
	}
	return c, nil
}

func displayKind(k Kind) string {
	if k == KindConfigCheck {
		return "config check"
	}
	return string(k)
}

func (c *Command) set(name, val string) *UsageError {
	switch name {
	case "--repo":
		c.Repo = val
	case "--json":
		c.JSON = true
	case "--wait":
		d, err := time.ParseDuration(val)
		if err != nil || d < 0 {
			return usagef("--wait needs a duration such as 30s or 2m, not %q", val)
		}
		c.Wait = &d
	case "--onto":
		c.Onto = val
	case "--into":
		c.Into = val
	case "--target-worktree":
		c.TargetWorktree = val
	case "--source-worktree":
		c.SourceWorktree = val
	case "--work-worktree":
		c.WorkWorktree = val
	case "--keep-source-worktree":
		c.KeepSourceWorktree = true
	case "--keep-source-branch":
		c.KeepSourceBranch = true
	case "--context":
		k, v, ok := strings.Cut(val, "=")
		if !ok || k == "" {
			return usagef("--context needs KEY=VALUE, not %q", val)
		}
		c.Context = append(c.Context, hooks.ContextPair{Key: k, Value: v})
	case "--file":
		c.File = val
	case "--tail":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return usagef("--tail needs a non-negative number, not %q", val)
		}
		c.Tail = n
	}
	return nil
}

// Environment variables used when the corresponding flag is absent (design
// section 11.1). The names deliberately do not start with PG_GIT_LANDER_,
// which the tool reserves for the environment it hands to hooks.
const (
	EnvTargetWorktree = "PGL_TARGET_WORKTREE"
	EnvSourceWorktree = "PGL_SOURCE_WORKTREE"
	EnvWorkWorktree   = "PGL_WORK_WORKTREE"
)

// ApplyEnv fills the worktree flags that are absent from the PGL_* variables.
// It does nothing for a resume: a resumed operation keeps the roles of its
// record, and the variables are not read.
func (c *Command) ApplyEnv(getenv func(string) string) {
	if c.Resume != "" || (c.Kind != KindRebase && c.Kind != KindLand) {
		return
	}
	fill := func(dst *string, name string) {
		if *dst == "" {
			*dst = getenv(name)
		}
	}
	if c.Kind == KindLand {
		fill(&c.TargetWorktree, EnvTargetWorktree)
	}
	fill(&c.SourceWorktree, EnvSourceWorktree)
	fill(&c.WorkWorktree, EnvWorkWorktree)
}
