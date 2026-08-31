package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

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
		renderConfigShow(os.Stdout, cfg)
		return exitOK
	}
	printUsageErr("config: unknown mode " + mode)
	return exitUsage
}

// renderConfigShow prints the resolved config: path, role set (flagging stub query
// types), and the pool DISPATCH SCALARS every worker is launched with. The scalars
// are the security-critical audit surface (pg2-ju3r): an operator can confirm
// PermissionMode=dontAsk (deny-by-default) and read the AllowedTools allowlist
// VERBATIM (so "git push absent" is auditable) without launching a worker or
// bypassing the nix wrapper to intercept ccpool.
func renderConfigShow(w io.Writer, cfg config.Config) {
	_, _ = fmt.Fprintf(w, "config path: %s\n", cfg.ConfigPath)
	_, _ = fmt.Fprintf(w, "roles (%d):\n", len(cfg.Roles))
	for _, r := range cfg.Roles {
		_, _ = fmt.Fprintf(w, "  - %-12s type=%-8s enabled=%t binds=%v\n", r.Name, r.Type, r.Enabled, r.Binds)
	}
	// Queries are the producers (event model): show each one's emits, flagging any
	// stub query type (not yet implemented; it errors when run).
	_, _ = fmt.Fprintf(w, "queries (%d):\n", len(cfg.Queries))
	for _, s := range cfg.Queries {
		stub := ""
		if s.Query != nil && query.IsStub(s.Query) {
			stub = "  (query type is a stub: not yet implemented)"
		}
		emits := []string(nil)
		if s.Query != nil {
			emits = s.Query.Emits()
		}
		_, _ = fmt.Fprintf(w, "  - %-14s emits=%v%s\n", s.Name, emits, stub)
	}
	_, _ = fmt.Fprintln(w, "gates (INV-LIFE-2):")
	_, _ = fmt.Fprintf(w, "  quota-paused: %s\n", gateShowLine(cfg.QuotaPaused))
	_, _ = fmt.Fprintf(w, "  cicd-down:    %s\n", gateShowLine(cfg.CICDDown))
	_, _ = fmt.Fprintln(w, "dispatch (workers):")
	_, _ = fmt.Fprintf(w, "  permission-mode: %s\n", cfg.PermissionMode)
	_, _ = fmt.Fprintf(w, "  allowed-tools:   %s\n", cfg.AllowedTools)
	_, _ = fmt.Fprintf(w, "  autonomous:      %t\n", cfg.Autonomous)
	_, _ = fmt.Fprintf(w, "  confirm-ingest:  %s\n", cfg.ConfirmIngest)
	_, _ = fmt.Fprintf(w, "  effort:          %s\n", orNone(cfg.Effort))
	_, _ = fmt.Fprintf(w, "  model:           %s\n", orDefault(cfg.Model))
	_, _ = fmt.Fprintf(w, "  budget:          tokens=%s cost=%s time=%s\n",
		limitStr(cfg.BudgetTokens), centsStr(cfg.BudgetCost), durStr(cfg.BudgetTime))
}

// gateShowLine renders one gate's `config --show` row: its path, and — read
// straight off the file, never from a separately-tracked flag, since file
// existence is the single source of truth (interfaces.md's "Operator
// pause/resume") — whether it is set and, if so, since when ("paused since").
func gateShowLine(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("%s (not paused)", path)
	}
	return fmt.Sprintf("%s (paused since %s)", path, fi.ModTime().Format(time.RFC3339))
}

// limitStr renders a token/count budget: <=0 means unlimited.
func limitStr(n int64) string {
	if n <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(n, 10)
}

// centsStr renders a cost budget in cents: <=0 means unlimited.
func centsStr(cents int64) string {
	if cents <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(cents, 10) + "c"
}

// durStr renders a time budget: <=0 means unlimited.
func durStr(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func orDefault(s string) string {
	if s == "" {
		return "(ccpool default)"
	}
	return s
}
