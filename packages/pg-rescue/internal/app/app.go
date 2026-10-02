// Package app wires the pg-rescue entry points together: it parses the
// command line, loads and validates the config, resolves the handler list,
// creates the run directory and hands a Plan to an Executor. Everything that
// can go wrong before the command runs is a wrapper error and exits 70.
package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/rundir"
)

// Exit codes owned by the wrapper.
const (
	// ExitWrapperError is EX_SOFTWARE: a CLI, config, run-directory or spawn
	// problem, always reported before the command runs.
	ExitWrapperError = 70
	// ExitDeferred is EX_TEMPFAIL: a handler deferred the failure.
	ExitDeferred = 75
)

// DefaultKillGrace is the SIGTERM-to-SIGKILL grace period.
const DefaultKillGrace = 5 * time.Second

// Runtime holds every outside dependency of the wrapper, so tests can inject
// them: the clock, the random source, the hostname, the environment, the
// PATH lookup and the executor that would run the command. Its zero value is
// not usable; start from DefaultRuntime.
type Runtime struct {
	Version  string
	Now      func() time.Time
	Rand     io.Reader
	Hostname func() (string, error)
	// KillGrace is the SIGTERM-to-SIGKILL grace; tests set it to about 50ms.
	KillGrace time.Duration
	Getenv    func(string) string
	Environ   func() []string
	Getwd     func() (string, error)
	Home      string
	LookPath  func(string) (string, error)
	// Executor runs the command and the handler chain once the plan is made.
	Executor Executor
}

// Plan is everything the wrapper decided before running the command.
type Plan struct {
	Options   *cli.Options
	Config    *config.Config
	Chain     string   // chain name; "" under --handlers
	Handlers  []string // resolved instance names, in order
	Cwd       string   // absolute directory the command runs in
	RunID     string
	RunDir    string
	StartedAt time.Time
	Host      string
	Runtime   *Runtime
	Stdout    io.Writer
	Stderr    io.Writer
}

// Executor runs the command and the handler chain for a Plan and returns the
// wrapper's exit code. Main calls it only after every wrapper error check has
// passed, so a wrapper error never spawns the command.
type Executor interface {
	Execute(p *Plan) int
}

// DefaultRuntime returns a Runtime backed by the real process environment.
func DefaultRuntime(version string) *Runtime {
	home, _ := os.UserHomeDir()
	return &Runtime{
		Version:   version,
		Now:       time.Now,
		Rand:      cryptoRand{},
		Hostname:  os.Hostname,
		KillGrace: DefaultKillGrace,
		Getenv:    os.Getenv,
		Environ:   os.Environ,
		Getwd:     os.Getwd,
		Home:      home,
		LookPath:  exec.LookPath,
		Executor:  pendingExecutor{},
	}
}

// pendingExecutor stands in until command execution lands: it reports that
// plainly (a wrapper error, exit 70) and removes the run directory it was
// given, so no half-run is left behind.
type pendingExecutor struct{}

func (pendingExecutor) Execute(p *Plan) int {
	_ = os.RemoveAll(p.RunDir)
	return failf(p.Stderr, "running the command and the handler chain is not implemented in this build")
}

// Main runs pg-rescue with args (without the program name) and returns its
// exit code.
func Main(rt *Runtime, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, cli.Usage)
		return ExitWrapperError
	}
	switch args[0] {
	case "result":
		return runResult(args[1:], stdout, stderr)
	case "check":
		return runCheck(rt, args[1:], stdout, stderr)
	case "--version":
		fmt.Fprintln(stdout, "pg-rescue", rt.Version)
		return 0
	}
	return runWrapper(rt, args, stdout, stderr)
}

func failf(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "pg-rescue: "+format+"\n", a...)
	return ExitWrapperError
}

// loadConfig resolves the config path and loads the whole file.
func loadConfig(rt *Runtime, flagPath string) (*config.Config, error) {
	path, _ := config.ResolvePath(flagPath, rt.Getenv, rt.Home)
	return config.Load(path, rt.Home)
}

func runWrapper(rt *Runtime, args []string, stdout, stderr io.Writer) int {
	opts, err := cli.ParseWrapper(args)
	if errors.Is(err, cli.ErrHelp) {
		fmt.Fprint(stdout, cli.Usage)
		return 0
	}
	if err != nil {
		return failf(stderr, "%v", err)
	}

	cfg, err := loadConfig(rt, opts.ConfigPath)
	if err != nil {
		return failf(stderr, "%v", err)
	}

	chainName, handlers, err := selectHandlers(cfg, opts)
	if err != nil {
		return failf(stderr, "%v", err)
	}

	cwd := opts.Dir
	if cwd == "" {
		if cwd, err = rt.Getwd(); err != nil {
			return failf(stderr, "cannot determine the working directory: %v", err)
		}
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return failf(stderr, "cannot resolve -C %q: %v", opts.Dir, err)
	}

	host, err := rt.Hostname()
	if err != nil {
		host = "unknown"
	}

	stateRoot := rundir.StateRoot(rt.Getenv, rt.Home)
	id, dir, err := rundir.Create(stateRoot, rt.Now, rt.Rand)
	if err != nil {
		return failf(stderr, "%v", err)
	}

	return rt.Executor.Execute(&Plan{
		Options:   opts,
		Config:    cfg,
		Chain:     chainName,
		Handlers:  handlers,
		Cwd:       cwd,
		RunID:     id,
		RunDir:    dir,
		StartedAt: rt.Now().UTC(),
		Host:      host,
		Runtime:   rt,
		Stdout:    stdout,
		Stderr:    stderr,
	})
}

// selectHandlers resolves --handlers or --chain against the config.
func selectHandlers(cfg *config.Config, opts *cli.Options) (chain string, handlers []string, err error) {
	if opts.Chain != "" {
		c, ok := cfg.Chains[opts.Chain]
		if !ok {
			return "", nil, config.UnknownNameError("chain", opts.Chain, "--chain", cfg.ChainNames())
		}
		return c.Name, append([]string(nil), c.Handlers...), nil
	}
	for _, h := range opts.Handlers {
		if _, ok := cfg.Handlers[h]; !ok {
			return "", nil, config.UnknownNameError("handler", h, "--handlers", cfg.HandlerNames())
		}
	}
	return "", append([]string(nil), opts.Handlers...), nil
}
