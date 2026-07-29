package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/trust"
)

// runTrust pre-trusts a cwd in ~/.claude.json so an automated `claude` launch
// there doesn't stall on the interactive folder-trust prompt. Exposed so the
// home-manager activation can pre-trust default_cwd — the primary, non-racy
// trust path — vs. the runtime `ensure` fallback (see internal/trust for why the
// runtime write is racy and must stay rare).
func runTrust(args []string) int {
	fs := flag.NewFlagSet("trust", flag.ExitOnError)
	_ = fs.Parse(args)

	cwd, _ := os.Getwd()
	if fs.NArg() >= 1 {
		cwd = fs.Arg(0)
	}
	// Canonicalize so the key matches what Claude records (it resolves symlinks,
	// e.g. /tmp -> /private/tmp; verified against Claude Code 2.1.170).
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
