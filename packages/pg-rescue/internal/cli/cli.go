// Package cli parses the pg-rescue command line. It only parses and checks
// the shape of the arguments; what the options do belongs to the wrapper.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

// Verbosity is the display level.
type Verbosity int

const (
	Quiet Verbosity = iota - 1
	Normal
	Verbose
	VeryVerbose
)

// Options are the parsed wrapper options.
type Options struct {
	// Handlers is the --handlers list; nil when --chain was used.
	Handlers []string
	// Chain is the --chain name; "" when --handlers was used.
	Chain string
	// Dir is -C; "" means the wrapper's own cwd.
	Dir        string
	Context    string
	Verify     string
	HasVerify  bool
	Stdin      bool
	ResultFile string
	Verbosity  Verbosity
	ConfigPath string
	// Argv is the command after "--"; nil in --stdin mode.
	Argv []string
}

// ErrHelp is returned when -h/--help was requested.
var ErrHelp = pflag.ErrHelp

// Usage is the synopsis printed for --help.
const Usage = `usage:
  pg-rescue (--handlers a,b,c | --chain NAME) [-C DIR] [--context TEXT] [--verify CMD]
            [--result-file F] [-q | -v | -vv] [--config PATH] -- cmd args...
  pg-rescue --stdin (--handlers ... | --chain ...) --verify CMD [-C DIR] [--context TEXT]
            [--result-file F] [-q | -v | -vv] [--config PATH]
  pg-rescue result <resolved|deferred|declined> [SUMMARY] [--details-file F] [--meta-file F]
  pg-rescue check [--chain NAME] [--config PATH]
`

// ParseWrapper parses the wrapper form (everything but the result and check
// subcommands). Every returned error other than ErrHelp is a wrapper error.
func ParseWrapper(args []string) (*Options, error) {
	var (
		handlers, chain, dir, ctxText, verify, resultFile, cfg string
		stdin, quiet                                           bool
		verbose                                                int
	)
	fs := pflag.NewFlagSet("pg-rescue", pflag.ContinueOnError)
	fs.SetOutput(discard{})
	fs.SetInterspersed(false)
	fs.StringVar(&handlers, "handlers", "", "comma-separated handler names, in order")
	fs.StringVar(&chain, "chain", "", "name of a configured chain")
	fs.StringVarP(&dir, "directory", "C", "", "run the command, handlers and verify in DIR")
	fs.StringVar(&ctxText, "context", "", "free text about the intent of this step")
	fs.StringVar(&verify, "verify", "", "command that checks a handler's resolved claim")
	fs.BoolVar(&stdin, "stdin", false, "treat stdin as the failure output instead of running a command")
	fs.StringVar(&resultFile, "result-file", "", "write the run's run-log line to this file")
	fs.BoolVarP(&quiet, "quiet", "q", false, "print no pg-rescue text")
	fs.CountVarP(&verbose, "verbose", "v", "more detail (-vv for the most)")
	fs.StringVar(&cfg, "config", "", "path to the config file")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil, ErrHelp
		}
		return nil, err
	}

	o := &Options{
		Chain:      chain,
		Dir:        dir,
		Context:    ctxText,
		Verify:     verify,
		HasVerify:  fs.Changed("verify"),
		Stdin:      stdin,
		ResultFile: resultFile,
		ConfigPath: cfg,
	}

	hasHandlers, hasChain := fs.Changed("handlers"), fs.Changed("chain")
	switch {
	case hasHandlers && hasChain:
		return nil, errors.New("--handlers and --chain are mutually exclusive; give exactly one")
	case !hasHandlers && !hasChain:
		return nil, errors.New("no handlers selected; give exactly one of --handlers or --chain (there is no default chain)")
	case hasChain && chain == "":
		return nil, errors.New("--chain: the chain name is empty")
	case hasHandlers:
		if handlers == "" {
			return nil, errors.New("--handlers: the list is empty")
		}
		o.Handlers = strings.Split(handlers, ",")
		for i, h := range o.Handlers {
			if h == "" {
				return nil, fmt.Errorf("--handlers: empty handler name at position %d in %q", i+1, handlers)
			}
		}
	}

	switch {
	case quiet && verbose > 0:
		return nil, errors.New("-q cannot be combined with -v")
	case verbose > 2:
		return nil, errors.New("-v can be given at most twice (-vv)")
	case quiet:
		o.Verbosity = Quiet
	default:
		o.Verbosity = Verbosity(verbose)
	}

	rest := fs.Args()
	dash := fs.ArgsLenAtDash()
	if dash > 0 {
		return nil, fmt.Errorf("unexpected argument %q before --", rest[0])
	}
	if dash < 0 && len(rest) > 0 {
		return nil, fmt.Errorf("unexpected argument %q; the command must follow --", rest[0])
	}

	if stdin {
		if dash >= 0 {
			return nil, errors.New("--stdin cannot be combined with -- cmd")
		}
		if !o.HasVerify {
			return nil, errors.New("--stdin requires --verify CMD (use --verify true to trust handlers)")
		}
		return o, nil
	}
	if dash < 0 || len(rest) == 0 {
		return nil, errors.New("no command; give it after --, or use --stdin")
	}
	o.Argv = append([]string(nil), rest...)
	return o, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// ResultOptions are the parsed `pg-rescue result` arguments.
type ResultOptions struct {
	Outcome     string
	Summary     string
	DetailsFile string
	MetaFile    string
}

// ParseResult parses the arguments after `result`.
func ParseResult(args []string) (*ResultOptions, error) {
	var details, meta string
	fs := pflag.NewFlagSet("pg-rescue result", pflag.ContinueOnError)
	fs.SetOutput(discard{})
	fs.StringVar(&details, "details-file", "", "file whose contents become details")
	fs.StringVar(&meta, "meta-file", "", "file holding one JSON object that becomes meta")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil, ErrHelp
		}
		return nil, err
	}
	pos := fs.Args()
	switch {
	case len(pos) == 0:
		return nil, errors.New("result: missing outcome; expected one of resolved, deferred, declined")
	case len(pos) > 2:
		return nil, fmt.Errorf("result: too many arguments (%d); expected <outcome> [SUMMARY]", len(pos))
	}
	r := &ResultOptions{Outcome: pos[0], DetailsFile: details, MetaFile: meta}
	if len(pos) == 2 {
		r.Summary = pos[1]
	}
	return r, nil
}

// CheckOptions are the parsed `pg-rescue check` arguments.
type CheckOptions struct {
	Chain      string
	ConfigPath string
}

// ParseCheck parses the arguments after `check`.
func ParseCheck(args []string) (*CheckOptions, error) {
	var chain, cfg string
	fs := pflag.NewFlagSet("pg-rescue check", pflag.ContinueOnError)
	fs.SetOutput(discard{})
	fs.StringVar(&chain, "chain", "", "list only this chain")
	fs.StringVar(&cfg, "config", "", "path to the config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil, ErrHelp
		}
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("check: unexpected argument %q", fs.Arg(0))
	}
	if fs.Changed("chain") && chain == "" {
		return nil, errors.New("--chain: the chain name is empty")
	}
	return &CheckOptions{Chain: chain, ConfigPath: cfg}, nil
}
