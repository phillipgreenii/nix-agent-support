package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

// runConfig implements `pr-pool config --print-defaults | --show`.
//   - print-defaults writes the canonical built-in config.toml to stdout (the
//     copy-paste starting point; equal to the no-config default role set).
//   - show loads the effective config and prints the resolved path + role set,
//     flagging any stub query types.
func runConfig(mode string) int {
	switch mode {
	case "print-defaults":
		fmt.Print(config.ExampleTOML())
		return exitOK
	case "show":
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return exitGeneric
		}
		fmt.Printf("config path: %s\n", cfg.ConfigPath)
		fmt.Printf("roles (%d):\n", len(cfg.Roles))
		for _, r := range cfg.Roles {
			stub := ""
			if r.Query != nil && query.IsStub(r.Query) {
				stub = "  (query type is a stub: not yet implemented)"
			}
			fmt.Printf("  - %-12s type=%-8s cap=%d enabled=%t%s\n", r.Name, r.Type, r.Cap, r.Enabled, stub)
		}
		return exitOK
	}
	printUsageErr("config: unknown mode " + mode)
	return exitUsage
}
