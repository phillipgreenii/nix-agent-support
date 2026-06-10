package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/trust"
)

// runTrust pre-trusts a cwd in ~/.claude.json so an automated `claude` launch
// there doesn't stall on the interactive folder-trust prompt (spec §8.1.1).
// Exposed so the home-manager activation can pre-trust default_cwd — the
// primary, non-racy trust path (§14 step 6) vs. the runtime `ensure` fallback.
func runTrust(args []string) int {
	fs := flag.NewFlagSet("trust", flag.ExitOnError)
	_ = fs.Parse(args)

	cwd, _ := os.Getwd()
	if fs.NArg() >= 1 {
		cwd = fs.Arg(0)
	}
	// Canonicalize so the key matches what Claude records (it resolves symlinks,
	// e.g. /tmp -> /private/tmp; the §4 spike confirmed this).
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	home, _ := os.UserHomeDir()
	if err := trust.EnsureTrusted(filepath.Join(home, ".claude.json"), cwd); err != nil {
		fmt.Fprintln(os.Stderr, "trust:", err)
		return 1
	}
	return 0
}
