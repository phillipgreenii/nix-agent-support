package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// runConfig implements `pg-router config --print-defaults | --show [--json] | check`.
//   - print-defaults writes the canonical built-in config.toml to stdout (the
//     copy-paste starting point; equal to the no-config default role set).
//     --json is never valid with print-defaults (args.go's parseConfigArgs
//     rejects it before this is reached), so asJSON is ignored for that mode.
//   - show loads the effective config and prints the resolved path + role set,
//     flagging any stub query types; --json prints the same information as one
//     JSON object (renderConfigShowJSON) instead of the text form
//     (renderConfigShow).
//   - check runs the startup config checks on demand (configCheck).
func runConfig(mode string, asJSON bool) int {
	switch mode {
	case "print-defaults":
		fmt.Print(config.ExampleTOML())
		return exitOK
	case "check":
		return configCheck(os.Stdout, os.Stderr)
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

// configCheck implements `pg-router config check`: the checks the daemon runs at
// startup, on demand. It adds no check of its own: every problem config.Load
// reports is printed unchanged, one per line, on stderr, so a missing query
// command, a missing handler command and a bad role config show in one run.
// Exit 1 on any problem; with none it prints one line, "config ok", and exits 0.
func configCheck(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		for _, line := range errorLines(err) {
			_, _ = fmt.Fprintln(stderr, line)
		}
		return exitGeneric
	}
	_, _ = fmt.Fprintln(stdout, "config ok")
	now := time.Now()
	for _, s := range cfg.Queries {
		if ct, ok := query.Calendar(s.Query.Trigger()); ok {
			_, _ = fmt.Fprintf(stdout, "%s: %s on_missed=%s next fire %s\n", s.Name, ct.Describe(), ct.OnMissed, ct.FormatSlot(ct.Next(now)))
		}
	}
	return exitOK
}

// errorLines splits err's text into its non-empty lines, so an errors.Join of
// several problems yields one entry per problem.
func errorLines(err error) []string {
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
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
		_, _ = fmt.Fprintf(w, "  - %-12s enabled=%t binds=%v\n", r.Name, r.Enabled, r.Binds)
		show := roleShowFor(cfg, r)
		if show.Command != "" {
			_, _ = fmt.Fprintf(w, "      command: %s\n", show.Command)
		} else {
			_, _ = fmt.Fprintf(w, "      command: (none: %s)\n", show.CommandError)
		}
		_, _ = fmt.Fprintf(w, "      survives_shutdown: %t\n", show.SurvivesShutdown)
		for _, cl := range cfg.Requirements[r.Name].Requirement.Clauses {
			_, _ = fmt.Fprintf(w, "      requires[%d]: %s\n", cl.Index, config.ExpandClause(cl))
		}
		if show.Role != nil {
			_, _ = fmt.Fprintf(w, "      role: %s\n", show.Role)
		}
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
		// A command query shows its command and its strict switch. A role's
		// command is shown with the role, in the roles block above.
		if s.Query != nil {
			if cmdline, strict, ok := queryCommand(s.Query); ok {
				_, _ = fmt.Fprintf(w, "      command: %s\n", cmdline)
				_, _ = fmt.Fprintf(w, "      strict_base_env: %t\n", strict)
			}
		}
	}
	renderLevelsShow(w, cfg)
	_, _ = fmt.Fprintln(w, "gates: live state, not configuration — see 'pg-router gate list' (Gate Registry)")
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

// renderLevelsShow prints the [levels] settings, the resource registry and the
// requires warnings (a clause naming a resource with no sensor on this machine).
func renderLevelsShow(w io.Writer, cfg config.Config) {
	lv := cfg.Levels
	_, _ = fmt.Fprintf(w, "levels: observed_at_skew=%s grace_factor=%v start_wait=%s start_stagger=%s wake_threshold=%s\n",
		lv.ObservedAtSkew, lv.GraceFactor, lv.StartWait, lv.StartStagger, lv.WakeThreshold)
	_, _ = fmt.Fprintf(w, "resources (%d):\n", len(cfg.Resources))
	for _, r := range cfg.Resources {
		_, _ = fmt.Fprintf(w, "  - %-14s scale=%s ignored=%s sensor=%s grace_factor=%v unknown=%s\n",
			r.Name, strings.Join(r.Scale, " > "), r.Ignored, orNone(r.Sensor), r.GraceFactor, r.Unknown)
	}
	for _, warn := range cfg.RequiresWarnings() {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warn)
	}
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
		show := roleShowFor(cfg, r)
		var clauses []configShowClause
		for _, cl := range cfg.Requirements[r.Name].Requirement.Clauses {
			clauses = append(clauses, configShowClause{Index: cl.Index, Text: cl.Text, Resource: cl.Resource, Allowed: cl.Allowed, Implicit: cl.Index == config.ImplicitClauseIndex})
		}
		out.Roles = append(out.Roles, configShowRole{
			Requires: clauses,
			Name:     r.Name, Enabled: r.Enabled, Binds: r.Binds, MaxDispatchRetries: r.MaxDispatchRetries,
			Command: show.Command, CommandError: show.CommandError, SurvivesShutdown: show.SurvivesShutdown, Role: show.Role,
		})
	}
	lv := cfg.Levels
	out.Levels = configShowLevels{
		ObservedAtSkew: lv.ObservedAtSkew.String(), GraceFactor: lv.GraceFactor, StartWait: lv.StartWait.String(),
		StartStagger: lv.StartStagger.String(), WakeThreshold: lv.WakeThreshold.String(),
	}
	out.Resources = make([]configShowResource, 0, len(cfg.Resources))
	for _, r := range cfg.Resources {
		out.Resources = append(out.Resources, configShowResource{Name: r.Name, Scale: r.Scale, Ignored: r.Ignored, Sensor: r.Sensor, GraceFactor: r.GraceFactor, Unknown: r.Unknown})
	}
	out.Warnings = cfg.RequiresWarnings()
	for _, s := range cfg.Queries {
		var emits []string
		stub := false
		if s.Query != nil {
			emits = s.Query.Emits()
			stub = query.IsStub(s.Query)
		}
		cq := configShowQuery{Name: s.Name, Emits: emits, Stub: stub}
		if s.Query != nil {
			if cmdline, strict, ok := queryCommand(s.Query); ok {
				cq.Command, cq.StrictBaseEnv = cmdline, &strict
			}
		}
		out.Queries = append(out.Queries, cq)
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
	Dispatch   configShowDispatch `json:"dispatch"`
	// Resources, Levels and Warnings carry the resource registry, the [levels]
	// settings and the requires warnings.
	Resources []configShowResource `json:"resources"`
	Levels    configShowLevels     `json:"levels"`
	Warnings  []string             `json:"warnings,omitempty"`
}

type configShowResource struct {
	Name        string   `json:"name"`
	Scale       []string `json:"scale"`
	Ignored     string   `json:"ignored"`
	Sensor      string   `json:"sensor,omitempty"`
	GraceFactor float64  `json:"graceFactor"`
	Unknown     string   `json:"unknown"`
}

type configShowLevels struct {
	ObservedAtSkew string  `json:"observedAtSkew"`
	GraceFactor    float64 `json:"graceFactor"`
	StartWait      string  `json:"startWait"`
	StartStagger   string  `json:"startStagger"`
	WakeThreshold  string  `json:"wakeThreshold"`
}

// configShowClause is one requires clause with its expansion.
type configShowClause struct {
	Index    int      `json:"index"`
	Text     string   `json:"text"`
	Resource string   `json:"resource"`
	Allowed  []string `json:"allowed"`
	Implicit bool     `json:"implicit,omitempty"`
}

type configShowRole struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Binds   []string `json:"binds"`
	// MaxDispatchRetries is the role's opt-in bounded re-run count for a
	// transiently failed dispatch (bead pg2-yu5y2); omitted when 0 (off).
	MaxDispatchRetries int `json:"maxDispatchRetries,omitempty"`
	// Requires lists the role's requires clauses with their expansions.
	Requires []configShowClause `json:"requires,omitempty"`
	// Command is what core runs for the role's dispatch, resolved by the ONE
	// role-adapter resolver (config.Config.RoleCommand): [handler, dispatch].
	// Empty with CommandError set for a role built in code without handler.
	Command      string `json:"command,omitempty"`
	CommandError string `json:"commandError,omitempty"`
	// SurvivesShutdown is the value core acts on at shutdown: the role's TOML
	// survives_shutdown key.
	SurvivesShutdown bool `json:"survivesShutdown"`
	// Role is the role object core sends in every handler request
	// ({"name", "config"}, including sessionEnv when the config has one), byte
	// for byte what wireclient puts on stdin.
	Role json.RawMessage `json:"role,omitempty"`
}

// roleShow is what config --show prints about one role's adapter: the resolved
// command, the survives-shutdown value and the role object as sent.
type roleShow struct {
	Command          string
	CommandError     string
	SurvivesShutdown bool
	Role             json.RawMessage
}

// roleShowFor resolves a role's adapter surfaces through the same resolver,
// survival decision and role-object builder the daemon uses, so --show cannot
// drift from what core runs and sends.
func roleShowFor(cfg config.Config, r roles.Role) roleShow {
	var out roleShow
	if argv, err := cfg.RoleCommand(r, "dispatch"); err != nil {
		out.CommandError = err.Error()
	} else {
		out.Command = displayArgv(argv)
	}
	out.SurvivesShutdown = roleSurvives(r)
	out.Role, _ = wireclient.SentRole(r)
	return out
}

// configShowQuery echoes one configured producer's emits, flagging a stub query
// type the same way the text form does (its "  (query type is a stub...)"
// suffix) — Stub, not a suffixed string, so a machine consumer branches on a
// bool rather than parsing prose.
type configShowQuery struct {
	Name  string   `json:"name"`
	Emits []string `json:"emits"`
	Stub  bool     `json:"stub"`
	// Command is a command query's command line (its argv, shell-quoted, as
	// config --show prints it); omitted for a query with no command (the timer).
	Command string `json:"command,omitempty"`
	// StrictBaseEnv is a command query's strict_base_env switch (present, true
	// or false, for every command query); omitted for a query with no command.
	StrictBaseEnv *bool `json:"strictBaseEnv,omitempty"`
}

// queryCommand renders a command query's command line, shell-quoted for
// display. ok is false for a query with no command.
func queryCommand(q query.Query) (line string, strict bool, ok bool) {
	argv, strict, ok := query.CommandOf(q)
	if !ok {
		return "", false, false
	}
	return displayArgv(argv), strict, true
}

// displayArgv renders argv as one shell-quoted, human-readable line. Words made
// only of safe characters are left bare.
func displayArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
