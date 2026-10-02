package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
)

// runCheck implements `pg-rescue check`: it validates the config exactly as a
// run would, then lists every handler (with command[0] resolved on PATH, or
// NOT FOUND) and every chain, or only --chain NAME. It exits 0 when the
// config is valid and every binary resolves, 1 when any binary is missing
// and 70 on a config error.
//
// Output, on stdout:
//
//	config: <path>
//	handler <name>: <resolved path | NOT FOUND (<command[0]>)>
//	  description: <text>      (only when set)
//	  tags: a, b               (only when set)
//	chain <name>: h1, h2, h3
func runCheck(rt *Runtime, args []string, stdout, stderr io.Writer) int {
	opts, err := cli.ParseCheck(args)
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
	chains := cfg.ChainNames()
	if opts.Chain != "" {
		if _, ok := cfg.Chains[opts.Chain]; !ok {
			return failf(stderr, "%v", config.UnknownNameError("chain", opts.Chain, "--chain", cfg.ChainNames()))
		}
		chains = []string{opts.Chain}
	}

	missing := 0
	fmt.Fprintf(stdout, "config: %s\n", cfg.Path)
	for _, name := range cfg.HandlerNames() {
		h := cfg.Handlers[name]
		resolved, err := rt.LookPath(h.Command[0])
		if err != nil {
			missing++
			fmt.Fprintf(stdout, "handler %s: NOT FOUND (%s)\n", name, h.Command[0])
		} else {
			fmt.Fprintf(stdout, "handler %s: %s\n", name, resolved)
		}
		if h.Description != "" {
			fmt.Fprintf(stdout, "  description: %s\n", h.Description)
		}
		if len(h.Tags) > 0 {
			fmt.Fprintf(stdout, "  tags: %s\n", strings.Join(h.Tags, ", "))
		}
	}
	for _, name := range chains {
		fmt.Fprintf(stdout, "chain %s: %s\n", name, strings.Join(cfg.Chains[name].Handlers, ", "))
	}
	if missing > 0 {
		return 1
	}
	return 0
}
