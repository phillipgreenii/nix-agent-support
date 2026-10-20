package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/runner"
)

// connectorBinary is the one external program work-report execs.
const connectorBinary = "pg-connector"

func init() { registerCommand(newConfigCmd) }

// newConfigCmd builds the `config` verb group: validate, show and
// pg-router-query.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate work-report's configuration",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newConfigValidateCmd(), newConfigShowCmd(), newConfigPGRouterQueryCmd())
	return cmd
}

// outputFormat validates the raw --output value for a config verb. Empty means
// human; the config verbs accept only human and json.
func outputFormat(raw string) (string, error) {
	switch raw {
	case "", "human":
		return "human", nil
	case "json":
		return "json", nil
	}
	return "", fmt.Errorf("--output %q is not supported here: want json or human", raw)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- config pg-router-query -------------------------------------------------

type pgRouterQueryJSON struct {
	Text string `json:"text"`
}

func newConfigPGRouterQueryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pg-router-query",
		Short: "Print the pg-router [[query]] stanza that schedules the pull",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, _ := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return asUsage(err)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			text := cfg.PGRouterQueryText()
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), pgRouterQueryJSON{Text: text})
			}
			_, err = io.WriteString(cmd.OutOrStdout(), text)
			return err
		},
	}
}

// ---- config show ------------------------------------------------------------

type sourceJSON struct {
	Enable bool     `json:"enable"`
	Labels []string `json:"labels"`
}

type showJSON struct {
	Timezone string                `json:"timezone"`
	Sources  map[string]sourceJSON `json:"sources"`
	Schedule struct {
		Interval string `json:"interval"`
		Window   string `json:"window"`
	} `json:"schedule"`
	Store struct {
		Path string `json:"path"`
	} `json:"store"`
	Generators map[string]generatorJSON `json:"generators"`
	Presets    map[string]presetJSON    `json:"presets"`
	// Problems lists what is wrong in the generators and reports sections;
	// config show still exits 0.
	Problems []problemJSON `json:"problems"`
}

type generatorJSON struct {
	Builtin bool     `json:"builtin"`
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
	Inputs  []string `json:"inputs"`
	Env     []string `json:"env"`
}

type narrowingJSON struct {
	Labels  []string `json:"labels"`
	Sources []string `json:"sources"`
	Types   []string `json:"types"`
}

type presetJSON struct {
	Builtin   bool           `json:"builtin"`
	Generator string         `json:"generator"`
	Config    map[string]any `json:"config"`
	Range     string         `json:"range"`
	Narrowing narrowingJSON  `json:"narrowing"`
	Output    struct {
		Dir string `json:"dir"`
	} `json:"output"`
}

type problemJSON struct {
	Kind      string  `json:"kind"`
	Generator *string `json:"generator"`
	Preset    *string `json:"preset"`
	Message   string  `json:"message"`
}

// durationText renders a duration the way an operator writes it: 5m, 1h30m.
func durationText(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// effectiveConfig renders cfg with the store path resolved against the --store
// flag, so the view shows what the other verbs will actually use.
func effectiveConfig(cfg config.Config, storeFlag string) showJSON {
	var v showJSON
	v.Timezone = cfg.Timezone
	v.Sources = make(map[string]sourceJSON, len(cfg.Sources))
	for name := range cfg.Sources {
		labels := cfg.SourceLabels(name)
		if labels == nil {
			labels = []string{}
		}
		v.Sources[name] = sourceJSON{Enable: cfg.SourceEnabled(name), Labels: labels}
	}
	v.Schedule.Interval = cfg.Schedule.Interval
	v.Schedule.Window = cfg.Schedule.Window
	v.Store.Path = config.StorePath(cfg, storeFlag)

	set := config.Presets(cfg)
	v.Generators = make(map[string]generatorJSON, len(set.Generators))
	for name, g := range set.Generators {
		gj := generatorJSON{Builtin: g.Builtin, Command: g.Command, Inputs: g.Inputs, Env: g.Env}
		if gj.Command == nil {
			gj.Command = []string{}
		}
		if !g.Builtin {
			gj.Timeout = durationText(g.Timeout)
		}
		v.Generators[name] = gj
	}
	v.Presets = make(map[string]presetJSON, len(set.Presets))
	for name, p := range set.Presets {
		pj := presetJSON{
			Builtin: p.Builtin, Generator: p.Generator, Range: p.Range,
			Config:    config.Redact(p.Config),
			Narrowing: narrowingJSON{Labels: p.Narrowing.Labels, Sources: p.Narrowing.Sources, Types: p.Narrowing.Types},
		}
		pj.Output.Dir = p.OutputDir
		v.Presets[name] = pj
	}
	v.Problems = make([]problemJSON, 0, len(set.Problems))
	for _, pr := range set.Problems {
		v.Problems = append(v.Problems, problemJSON{Kind: pr.Kind, Generator: nonEmpty(pr.Generator), Preset: nonEmpty(pr.Preset), Message: pr.Message})
	}
	return v
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return asUsage(err)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			v := effectiveConfig(cfg, storeFlag)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return writeShowHuman(cmd.OutOrStdout(), v)
		},
	}
}

func writeShowHuman(w io.Writer, v showJSON) error {
	tz := v.Timezone
	if tz == "" {
		tz = "(system)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "timezone: %s\n", tz)
	fmt.Fprintf(&b, "schedule.interval: %s\n", v.Schedule.Interval)
	fmt.Fprintf(&b, "schedule.window: %s\n", v.Schedule.Window)
	fmt.Fprintf(&b, "store.path: %s\n", v.Store.Path)
	if len(v.Sources) == 0 {
		b.WriteString("sources: (none configured; every backend is enabled with no extra labels)\n")
	} else {
		b.WriteString("sources:\n")
		names := make([]string, 0, len(v.Sources))
		for name := range v.Sources {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			s := v.Sources[name]
			labels := "-"
			if len(s.Labels) > 0 {
				labels = strings.Join(s.Labels, ", ")
			}
			fmt.Fprintf(&b, "  %s: enable=%t labels=%s\n", name, s.Enable, labels)
		}
	}
	writeShowPresetsHuman(&b, v)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeShowPresetsHuman appends the generators, presets and problems.
func writeShowPresetsHuman(b *strings.Builder, v showJSON) {
	b.WriteString("generators:\n")
	for _, name := range sortedKeys(v.Generators) {
		g := v.Generators[name]
		if g.Builtin {
			fmt.Fprintf(b, "  %s: built in\n", name)
			continue
		}
		fmt.Fprintf(b, "  %s: command=%s timeout=%s inputs=%s env=%s\n",
			name, listText(g.Command), g.Timeout, listText(g.Inputs), listText(g.Env))
	}
	b.WriteString("presets:\n")
	for _, name := range sortedKeys(v.Presets) {
		p := v.Presets[name]
		fmt.Fprintf(b, "  %s: generator=%s range=%s output.dir=%s\n", name, p.Generator, p.Range, p.Output.Dir)
		fmt.Fprintf(b, "    narrowing: labels=%s sources=%s types=%s\n",
			listText(p.Narrowing.Labels), listText(p.Narrowing.Sources), listText(p.Narrowing.Types))
		if len(p.Config) > 0 {
			cfg, _ := json.Marshal(p.Config)
			fmt.Fprintf(b, "    config: %s\n", cfg)
		}
	}
	if len(v.Problems) > 0 {
		b.WriteString("problems:\n")
		for _, pr := range v.Problems {
			fmt.Fprintf(b, "  %s: %s\n", pr.Kind, pr.Message)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func listText(l []string) string {
	if len(l) == 0 {
		return "-"
	}
	return strings.Join(l, ", ")
}

// ---- config validate --------------------------------------------------------

// The problem kinds config validate reports besides the ones config.Presets
// emits (bad_name, bad_command, reserved_name, unknown_generator, bad_range,
// bad_inputs, bad_output_dir, bad_field).
const (
	problemConfigParse    = "config_parse"
	problemKindsRetired   = "kinds_retired"
	problemNotFound       = "not_found"
	problemConfigInvalid  = "config_invalid"
	problemValidateFailed = "validate_failed"
	problemDirDirty       = "validate_dir_dirty"
)

// validateProblemJSON is one problem; Preset is null for a problem not tied to
// a preset (a config that does not load, a bad generator entry).
type validateProblemJSON struct {
	Preset *string `json:"preset"`
	Kind   string  `json:"kind"`
	Reason string  `json:"reason"`
}

type validateJSON struct {
	OK          bool                  `json:"ok"`
	Error       string                `json:"error,omitempty"`
	ConnectorAt string                `json:"pg_connector_path,omitempty"`
	ExitCode    int                   `json:"pg_connector_exit_code"`
	Output      string                `json:"pg_connector_output,omitempty"`
	Problems    []validateProblemJSON `json:"problems"`
	Warnings    []string              `json:"warnings"`
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the config file, the report generators and that pg-connector is usable",
		Long: "Loads and checks the work-report config, applies the semantic checks of the generators " +
			"and reports sections, runs every preset's generator in validate mode (one after another, " +
			"each in a fresh empty temporary directory, under the same environment allowlist as a " +
			"real run), confirms pg-connector is on PATH and runs `pg-connector config validate` for " +
			"the activity sources. Every problem is printed, not the first; any problem exits 3.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, _ := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return asUsage(err)
			}
			res, runErr := validate(cmd, cfgPath)
			if format == "json" {
				if err := writeJSON(cmd.OutOrStdout(), res); err != nil {
					return err
				}
				return runErr
			}
			out := cmd.OutOrStdout()
			for _, p := range res.Problems {
				name := p.Kind
				if p.Preset != nil {
					name = *p.Preset
				}
				fmt.Fprintf(out, "%s: %s\n", name, p.Reason)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(out, "warning: %s\n", w)
			}
			if runErr == nil {
				fmt.Fprintf(out, "config ok; %s config validate:\n%s", connectorBinary, res.Output)
				if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
					fmt.Fprintln(out)
				}
			} else if res.Output != "" {
				fmt.Fprint(out, res.Output)
			}
			return runErr
		},
	}
}

func presetPtr(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

// validate loads the config, applies the generators and reports checks, runs
// validate mode for every preset, finds pg-connector on PATH and runs its
// `config validate`. A non-nil error means validation failed (an exitError
// with status 3); the returned result is populated as far as the run got.
func validate(cmd *cobra.Command, cfgPath string) (validateJSON, error) {
	res := validateJSON{Problems: []validateProblemJSON{}, Warnings: []string{}}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		kind := problemConfigParse
		if errors.Is(err, config.ErrKindsRetired) {
			kind = problemKindsRetired
		}
		res.Problems = append(res.Problems, validateProblemJSON{Kind: kind, Reason: err.Error()})
		res.Error = err.Error()
		return res, &exitError{code: 3, msg: err.Error(), cause: err}
	}

	set := config.Presets(cfg)
	for _, p := range set.Problems {
		res.Problems = append(res.Problems, validateProblemJSON{Preset: presetPtr(p.Preset), Kind: p.Kind, Reason: p.Message})
	}
	validatePresets(cmd.Context(), set, &res)

	failure := ""
	path, err := exec.LookPath(connectorBinary)
	switch {
	case err != nil:
		failure = fmt.Sprintf("%s not found on PATH: %v", connectorBinary, err)
	default:
		res.ConnectorAt = path
		var out bytes.Buffer
		c := exec.CommandContext(cmd.Context(), path, "config", "validate")
		c.Stdout = &out
		c.Stderr = &out
		runErr := c.Run()
		res.Output = out.String()
		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
		case errors.As(runErr, &exitErr):
			res.ExitCode = exitErr.ExitCode()
			failure = fmt.Sprintf("%s config validate failed (exit %d)", connectorBinary, res.ExitCode)
		default:
			failure = fmt.Sprintf("run %s config validate: %v", connectorBinary, runErr)
		}
	}

	if failure == "" && len(res.Problems) == 0 {
		res.OK = true
		return res, nil
	}
	msg := failure
	if msg == "" {
		msg = fmt.Sprintf("config validate: %d %s found", len(res.Problems), plural(len(res.Problems), "problem", "problems"))
	}
	res.Error = msg
	return res, &exitError{code: 3, msg: msg}
}

// validatePresets runs validate mode for every preset that has an external
// generator, in name order (the order Presets itself uses), and adds a warning
// for each generator no preset references whose executable cannot be found.
func validatePresets(ctx context.Context, set config.PresetSet, res *validateJSON) {
	rn := runner.New(Version)
	referenced := map[string]bool{}
	for _, name := range sortedKeys(set.Presets) {
		p := set.Presets[name]
		referenced[p.Generator] = true
		g := set.Generators[p.Generator]
		if p.Builtin || g.Builtin {
			continue
		}
		res.Problems = append(res.Problems, validateOne(ctx, rn, p, g)...)
	}
	for _, name := range sortedKeys(set.Generators) {
		g := set.Generators[name]
		if g.Builtin || referenced[name] || len(g.Command) == 0 {
			continue
		}
		if !resolvable(g.Command[0]) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("generator %q: no preset references it and its executable %q cannot be found", name, g.Command[0]))
		}
	}
}

// resolvable reports whether argv0 names an executable: a bare name is looked
// up on PATH, anything with a slash is checked where it is.
func resolvable(argv0 string) bool {
	if !strings.Contains(argv0, "/") {
		_, err := exec.LookPath(argv0)
		return err == nil
	}
	fi, err := os.Stat(argv0)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// validateOne runs the generator of preset p in validate mode: stdin is
// { "contract_version": 1, "mode": "validate", "config": ... }, the working
// directory a fresh empty temporary directory that must still be empty
// afterwards, and the environment the same allowlist a real run gets.
func validateOne(ctx context.Context, rn *runner.Runner, p config.Preset, g config.Generator) []validateProblemJSON {
	problem := func(kind, format string, args ...any) validateProblemJSON {
		return validateProblemJSON{Preset: presetPtr(p.Name), Kind: kind, Reason: fmt.Sprintf(format, args...)}
	}
	dir, err := os.MkdirTemp("", "work-report-validate-")
	if err != nil {
		return []validateProblemJSON{problem(problemValidateFailed, "generator %q: cannot create the validate-mode directory: %v", g.Name, err)}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	stdin, err := json.Marshal(map[string]any{"contract_version": runner.ContractVersion, "mode": runner.ModeValidate, "config": p.Config})
	if err != nil {
		return []validateProblemJSON{problem(problemValidateFailed, "generator %q: its config cannot be encoded: %v", g.Name, err)}
	}
	r := rn.Exec(ctx, runner.ExecSpec{Argv: g.Command, Dir: dir, Env: rn.Environ(g, runner.ModeValidate), Stdin: stdin, Timeout: g.Timeout})

	var out []validateProblemJSON
	switch {
	case r.StartErr != nil && r.NotFound:
		return []validateProblemJSON{problem(problemNotFound, "generator %q: command %q not found", g.Name, g.Command[0])}
	case r.StartErr != nil:
		return []validateProblemJSON{problem(problemValidateFailed, "generator %q could not start: %v", g.Name, r.StartErr)}
	case r.TimedOut:
		out = append(out, problem(problemValidateFailed, "generator %q did not finish validate mode within its timeout of %s", g.Name, durationText(g.Timeout)))
	case r.Cancelled:
		out = append(out, problem(problemValidateFailed, "generator %q: validate mode was cancelled", g.Name))
	case r.ExitCode == nil:
		out = append(out, problem(problemValidateFailed, "generator %q was terminated by %s in validate mode", g.Name, r.Signal))
	case *r.ExitCode == 3:
		out = append(out, problem(problemConfigInvalid, "generator %q config invalid: %s", g.Name, reasons(r.Stderr)))
	case *r.ExitCode != 0:
		out = append(out, problem(problemValidateFailed, "generator %q exited with status %d in validate mode: %s", g.Name, *r.ExitCode, reasons(r.Stderr)))
	}
	if left, err := os.ReadDir(dir); err != nil {
		out = append(out, problem(problemDirDirty, "generator %q: the validate-mode directory cannot be read afterwards: %v", g.Name, err))
	} else if len(left) > 0 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		out = append(out, problem(problemDirDirty, "generator %q wrote %s in validate mode, which must not write files", g.Name, strings.Join(names, ", ")))
	}
	return out
}

// reasons turns a generator's stderr, one reason per line, into one line.
func reasons(stderr string) string {
	var parts []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	if len(parts) == 0 {
		return "(no reason given on stderr)"
	}
	return strings.Join(parts, "; ")
}
