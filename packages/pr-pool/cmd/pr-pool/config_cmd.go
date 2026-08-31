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

// runConfig implements `pr-pool config --print-defaults | --show [--json]`.
//   - print-defaults writes the canonical built-in config.toml to stdout (the
//     copy-paste starting point; equal to the no-config default role set).
//     --json is never valid with print-defaults (args.go's parseConfigArgs
//     rejects it before this is reached), so asJSON is ignored for that mode.
//   - show loads the effective config and prints the resolved path + role set,
//     flagging any stub query types; --json prints the same information as one
//     JSON object (renderConfigShowJSON) instead of the text form
//     (renderConfigShow).
func runConfig(mode string, asJSON bool) int {
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
		if asJSON {
			renderConfigShowJSON(os.Stdout, cfg)
		} else {
			renderConfigShow(os.Stdout, cfg)
		}
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

// gateState reports whether path's gate is currently set (its file exists) and,
// when set, the RFC3339 instant it was set at (the file's mtime) — read
// straight off the file, never from a separately-tracked flag, since file
// existence is the single source of truth (interfaces.md's "Operator
// pause/resume"). Both gateShowLine's text rendering and
// renderConfigShowJSON's JSON rendering consult this ONE helper, so the two
// output forms can never disagree about gate state.
func gateState(path string) (paused bool, since string) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, ""
	}
	return true, fi.ModTime().Format(time.RFC3339)
}

// gateShowLine renders one gate's `config --show` text row: its path, and
// whether it is set and, if so, since when ("paused since").
func gateShowLine(path string) string {
	paused, since := gateState(path)
	if !paused {
		return fmt.Sprintf("%s (not paused)", path)
	}
	return fmt.Sprintf("%s (paused since %s)", path, since)
}

// gateShowJSON renders one gate's `config --show --json` row.
func gateShowJSON(path string) configShowGate {
	paused, since := gateState(path)
	return configShowGate{Path: path, Paused: paused, Since: since}
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

// renderConfigShowJSON is renderConfigShow's `--json` counterpart (Task 1.5b):
// the SAME resolved-config information config --show prints as text, encoded as
// one JSON object instead. Per Task 0.4's wire decision (recorded in
// docs/decisions/cli.md's DEC-CLI-1 "--json's versioning" note), a subcommand's
// --json output is UNVERSIONED unless docs/decisions/wire.md says otherwise for
// it — this shape deliberately carries no `schemaVersion` field: it is not one
// of the schemas/-registered, conformance-gated wire messages DEC-WIRE-1
// governs, just an operator-facing CLI report that may change shape freely.
func renderConfigShowJSON(w io.Writer, cfg config.Config) {
	out := configShowReport{
		ConfigPath: cfg.ConfigPath,
		Roles:      make([]configShowRole, 0, len(cfg.Roles)),
		Queries:    make([]configShowQuery, 0, len(cfg.Queries)),
		Gates: configShowGates{
			QuotaPaused: gateShowJSON(cfg.QuotaPaused),
			CICDDown:    gateShowJSON(cfg.CICDDown),
		},
		Dispatch: configShowDispatch{
			PermissionMode: cfg.PermissionMode,
			AllowedTools:   cfg.AllowedTools,
			Autonomous:     cfg.Autonomous,
			ConfirmIngest:  cfg.ConfirmIngest.String(),
			Effort:         cfg.Effort,
			Model:          cfg.Model,
			Budget: configShowBudget{
				// <=0 means unlimited, mirroring limitStr/centsStr/durStr's own
				// text-mode convention (above), rather than a separate
				// "unlimited" boolean a machine consumer would have to check
				// first.
				TokensLimit:      cfg.BudgetTokens,
				CostCentsLimit:   cfg.BudgetCost,
				TimeLimitSeconds: int64(cfg.BudgetTime.Seconds()),
			},
		},
	}
	for _, r := range cfg.Roles {
		out.Roles = append(out.Roles, configShowRole{Name: r.Name, Type: r.Type, Enabled: r.Enabled, Binds: r.Binds})
	}
	for _, s := range cfg.Queries {
		var emits []string
		stub := false
		if s.Query != nil {
			emits = s.Query.Emits()
			stub = query.IsStub(s.Query)
		}
		out.Queries = append(out.Queries, configShowQuery{Name: s.Name, Emits: emits, Stub: stub})
	}
	writeJSON(w, out)
}

// configShowReport is the `--json` output shape of `config --show`. Like
// pushInjectReport, it is NOT one of the INTF message schemas — DEC-CLI-1
// requires only that every operator subcommand "emit JSON with --json"; the
// concrete shape is this CLI's own report.
type configShowReport struct {
	ConfigPath string             `json:"configPath"`
	Roles      []configShowRole   `json:"roles"`
	Queries    []configShowQuery  `json:"queries"`
	Gates      configShowGates    `json:"gates"`
	Dispatch   configShowDispatch `json:"dispatch"`
}

type configShowRole struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Enabled bool     `json:"enabled"`
	Binds   []string `json:"binds"`
}

// configShowQuery echoes one configured producer's emits, flagging a stub query
// type the same way the text form does (its "  (query type is a stub...)"
// suffix) — Stub, not a suffixed string, so a machine consumer branches on a
// bool rather than parsing prose.
type configShowQuery struct {
	Name  string   `json:"name"`
	Emits []string `json:"emits"`
	Stub  bool     `json:"stub"`
}

type configShowGates struct {
	QuotaPaused configShowGate `json:"quotaPaused"`
	CICDDown    configShowGate `json:"cicdDown"`
}

// configShowGate is one gate's `--json` row (INV-LIFE-2): Since is the RFC3339
// "paused since" instant, omitted when Paused is false.
type configShowGate struct {
	Path   string `json:"path"`
	Paused bool   `json:"paused"`
	Since  string `json:"since,omitempty"`
}

// configShowDispatch mirrors renderConfigShow's "dispatch (workers):" block —
// the pg2-ju3r security-audit surface — as JSON fields instead of text lines.
type configShowDispatch struct {
	PermissionMode string           `json:"permissionMode"`
	AllowedTools   string           `json:"allowedTools"`
	Autonomous     bool             `json:"autonomous"`
	ConfirmIngest  string           `json:"confirmIngest"`
	Effort         string           `json:"effort,omitempty"`
	Model          string           `json:"model,omitempty"`
	Budget         configShowBudget `json:"budget"`
}

type configShowBudget struct {
	TokensLimit      int64 `json:"tokensLimit"`
	CostCentsLimit   int64 `json:"costCentsLimit"`
	TimeLimitSeconds int64 `json:"timeLimitSeconds"`
}
