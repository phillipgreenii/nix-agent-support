package main

import (
	"flag"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
)

// common holds the flags every client subcommand takes.
type common struct {
	clientName string
	socket     string
	configPath string
	jsonOut    bool
	partialOK  bool
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.clientName, "client", "cli", "name the caller for the recorder's per-client metrics")
	fs.StringVar(&c.socket, "socket", "", "the daemon's socket")
	fs.StringVar(&c.configPath, "config", "", "the TOML config file")
	fs.BoolVar(&c.jsonOut, "json", false, "print the stable JSON shape")
	fs.BoolVar(&c.partialOK, "allow-partial", false, "exit 0 instead of 5 while the recorder is behind (first backfill)")
}

func newFlagSet(name string, _ io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {} // errors print as "error: usage" plus one line; --help has the details
	return fs
}

// resolve loads the config and picks the socket: --socket, else the
// environment, else the config's socket (which the environment also overrides).
func (a *app) resolve(c *common) (config.Config, *client.Client, error) {
	path, must := c.configPath, true
	if path == "" {
		path, must = config.DefaultPath(a.getenv), false
	}
	cfg, err := config.Load(path, must, a.getenv)
	if err != nil {
		return cfg, nil, err
	}
	if c.socket != "" {
		cfg.Socket = c.socket
	}
	return cfg, client.New(cfg.Socket, c.clientName), nil
}
