package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Env is everything Run needs from the process, so tests can supply their
// own seams.
type Env struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Cwd is the directory used when --repo is absent.
	Cwd     string
	Version string

	// Deps carries the seams (Git, Clock, Liveness, Host, Paths and the test
	// seams of the lock layer). Run fills Owner, Out and Wait.
	Deps pipeline.Deps
	// Interrupter turns signals into cancellation; the zero value subscribes
	// to SIGINT, SIGTERM and SIGHUP.
	Interrupter pipeline.Interrupter
	// Token is this process's owner token; empty generates a random one.
	Token string
}

// Production builds the Env of the real process.
func Production(version string, stdout, stderr io.Writer) *Env {
	cwd, _ := os.Getwd()
	g := &gitexec.Git{Runner: pipeline.GroupRunner(pipeline.DefaultGrace), Env: seams.ProductionGitEnv()}
	return &Env{
		Stdout: stdout, Stderr: stderr, Getenv: os.Getenv, Cwd: cwd, Version: version,
		Deps: pipeline.Deps{
			Git: g, Clock: seams.SystemClock(), Liveness: seams.ProcLiveness(),
			Host: seams.OSHost(), Paths: seams.XDGConfigPaths(),
		},
	}
}

// Run executes one invocation and returns the process exit code.
func Run(args []string, env *Env) int {
	c, uerr := Parse(args)
	if uerr != nil {
		o := outcome.New(outcome.UsageError)
		o.Message = uerr.Msg
		if !uerr.JSON {
			fmt.Fprintln(env.Stderr, Synopsis)
		}
		return print(env, o, uerr.JSON)
	}
	switch c.Kind {
	case KindVersion:
		fmt.Fprintf(env.Stdout, "pg-git-lander %s\n", env.Version)
		return 0
	case KindHelp:
		fmt.Fprintln(env.Stdout, HelpText)
		return 0
	}
	c.ApplyEnv(env.Getenv)

	ctx, stop := env.Interrupter.Context(context.Background())
	defer stop()

	if c.Kind == KindStatus || c.Kind == KindLog {
		return readOnly(ctx, env, c)
	}
	o, err := dispatch(ctx, env, c)
	if err != nil {
		o = outcome.New(outcome.InternalError)
		o.Operation, o.Branch, o.Repo = string(c.Kind), c.Branch, c.Repo
		o.Message = err.Error()
	}
	if o == nil {
		o = outcome.New(outcome.InternalError)
		o.Message = "no outcome was produced"
	}
	return print(env, o, c.JSON)
}

// print writes the one outcome of the invocation and returns its exit code.
func print(env *Env, o *outcome.Outcome, asJSON bool) int {
	if asJSON {
		b, err := o.JSON()
		if err != nil {
			fmt.Fprintf(env.Stderr, "pg-git-lander: rendering the outcome: %v\n", err)
			return outcome.ExitInternal
		}
		fmt.Fprintf(env.Stdout, "%s\n", b)
	} else {
		fmt.Fprintln(env.Stdout, o.Human())
	}
	return outcome.ExitCode(o.Outcome)
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func dispatch(ctx context.Context, env *Env, c *Command) (*outcome.Outcome, error) {
	dir := c.Repo
	if dir == "" {
		dir = env.Cwd
	}
	deps := env.Deps
	deps.Out = env.Stderr
	deps.Wait = c.Wait
	token := env.Token
	if token == "" {
		token = newToken()
	}
	deps.Owner = pipeline.CurrentOwner(deps.Host, deps.Liveness, env.Version, token)

	pruneLogs(ctx, env, deps, dir)

	switch c.Kind {
	case KindConfigCheck:
		l := &config.Loader{Git: deps.Git, Paths: deps.Paths}
		return l.Check(ctx, config.CheckOptions{RepoDir: dir, File: c.File})
	case KindRebase, KindLand:
		if c.Resume != "" {
			return pipeline.Resume(ctx, deps, pipeline.ResumeRequest{
				Flag: c.Resume, Operation: worktrees.Operation(c.Kind), Branch: c.Branch,
				Dir: dir, RepoFlag: c.Repo,
			})
		}
		return start(ctx, deps, c, dir)
	}
	return nil, fmt.Errorf("unhandled subcommand %q", c.Kind)
}

// pruneLogs is the log pruning at the start of every invocation (design
// section 14.5). It is housekeeping: a failure is reported on stderr and
// never changes the outcome.
func pruneLogs(ctx context.Context, env *Env, deps pipeline.Deps, dir string) {
	common, err := deps.Git.CommonDir(ctx, dir)
	if err != nil {
		return // not a repository: the subcommand reports it
	}
	layer := pipeline.LayerFor(deps, dir)
	if _, err := oplog.PruneNamed(ctx, layer, deps.Clock.Now()); err != nil {
		fmt.Fprintf(env.Stderr, "pg-git-lander: pruning the logs of %s: %v\n", common, err)
	}
}

func start(ctx context.Context, deps pipeline.Deps, c *Command, dir string) (*outcome.Outcome, error) {
	req := pipeline.Request{
		Operation: worktrees.Operation(c.Kind), Branch: c.Branch, Target: c.Into, Onto: c.Onto,
		Dir: dir, RepoFlag: c.Repo,
		TargetWorktree: c.TargetWorktree, SourceWorktree: c.SourceWorktree, WorkWorktree: c.WorkWorktree,
		KeepSourceWorktree: c.KeepSourceWorktree, KeepSourceBranch: c.KeepSourceBranch,
		Context: append([]hooks.ContextPair(nil), c.Context...),
	}
	res, err := pipeline.Start(ctx, deps, req)
	if err != nil {
		return nil, err
	}
	if res.Prepared == nil {
		return res.Outcome, nil
	}
	return res.Prepared.Run(ctx)
}

// readOnly runs status and log. They print their own output instead of the
// outcome object; status writes nothing at all (not even the log pruning of
// the other subcommands).
func readOnly(ctx context.Context, env *Env, c *Command) int {
	dir := c.Repo
	if dir == "" {
		dir = env.Cwd
	}
	var err error
	if c.Kind == KindStatus {
		err = runStatus(ctx, env, c, dir)
	} else {
		err = runLog(ctx, env, c, dir)
	}
	if err == nil {
		return outcome.ExitOK
	}
	o := outcome.New(outcome.InternalError)
	if errors.Is(err, status.ErrNotARepo) || errors.Is(err, status.ErrNoLog) {
		o = outcome.New(outcome.UsageError)
	}
	o.Operation, o.Branch, o.Repo = string(c.Kind), c.Branch, c.Repo
	o.Message = err.Error()
	return print(env, o, c.JSON)
}

func runStatus(ctx context.Context, env *Env, c *Command, dir string) error {
	d := env.Deps
	rep, err := status.Collect(ctx, status.Input{
		Git: d.Git, Clock: d.Clock, Liveness: d.Liveness, Host: d.Host, Paths: d.Paths,
		Dir: dir, Branch: c.Branch,
	})
	if err != nil {
		return err
	}
	if c.JSON {
		b, err := rep.JSON()
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%s\n", b)
		return nil
	}
	fmt.Fprintln(env.Stdout, rep.Human())
	return nil
}

func runLog(ctx context.Context, env *Env, c *Command, dir string) error {
	d := env.Deps
	pruneLogs(ctx, env, d, dir)
	_, path, err := status.ResolveLog(ctx, d.Git, dir, c.Branch)
	if err != nil {
		return err
	}
	return status.PrintLog(env.Stdout, path, c.Tail)
}
