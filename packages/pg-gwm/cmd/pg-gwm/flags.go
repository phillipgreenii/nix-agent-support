package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// parseInterleaved parses args with fs, accepting flags before, between and
// after the positional arguments (the standard flag package stops at the
// first positional). Everything after a "--" is positional. It returns the
// positionals in order.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// commonFlags are the flags every subcommand takes.
type commonFlags struct {
	repo   string
	format string
	json   bool
	print  string
}

// register adds the common flags to fs; withPrint adds --print.
func (c *commonFlags) register(fs *flag.FlagSet, withPrint bool) {
	fs.StringVar(&c.repo, "repo", "", "repository path (default: the current directory)")
	fs.StringVar(&c.format, "format", "", "text or json (default: text on a terminal, else json)")
	fs.BoolVar(&c.json, "json", false, "alias for --format=json")
	if withPrint {
		fs.StringVar(&c.print, "print", "", "print only one field; the only value is path")
	}
}

// resolveFormat is the output format: --json and --format override the
// terminal default.
func (c *commonFlags) resolveFormat(tty bool) (string, error) {
	f := c.format
	switch f {
	case "", "text", "json":
	default:
		return "", fmt.Errorf("--format %q: want text or json", f)
	}
	if c.json {
		if f == "text" {
			return "", fmt.Errorf("--json and --format=text contradict each other")
		}
		return "json", nil
	}
	if f != "" {
		return f, nil
	}
	if tty {
		return "text", nil
	}
	return "json", nil
}

func (c *commonFlags) printPath() (bool, error) {
	switch c.print {
	case "":
		return false, nil
	case "path":
		return true, nil
	}
	return false, fmt.Errorf("--print %q: the only value is path", c.print)
}

// kvFlag collects repeated key=value flags.
type kvFlag map[string]string

func (k kvFlag) String() string { return fmt.Sprint(map[string]string(k)) }

func (k kvFlag) Set(v string) error {
	key, val, ok := strings.Cut(v, "=")
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", v)
	}
	k[key] = val
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
