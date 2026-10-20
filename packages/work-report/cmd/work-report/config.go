package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
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
				return err
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
				return err
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

type validateJSON struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	ConnectorAt string `json:"pg_connector_path,omitempty"`
	ExitCode    int    `json:"pg_connector_exit_code"`
	Output      string `json:"pg_connector_output,omitempty"`
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the config file and that pg-connector is usable",
		Long: "Loads and checks the work-report config, confirms pg-connector is on PATH, " +
			"and runs `pg-connector config validate` for the activity sources.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, _ := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return err
			}
			res, runErr := validate(cmd, cfgPath)
			if format == "json" {
				if err := writeJSON(cmd.OutOrStdout(), res); err != nil {
					return err
				}
			} else if runErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "config ok; %s config validate:\n%s", connectorBinary, res.Output)
				if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
					fmt.Fprintln(cmd.OutOrStdout())
				}
			} else if res.Output != "" {
				fmt.Fprint(cmd.OutOrStdout(), res.Output)
			}
			return runErr
		},
	}
}

// validate loads the config, finds pg-connector on PATH and runs its
// `config validate`. A non-nil error means validation failed; the returned
// result is populated as far as the run got.
func validate(cmd *cobra.Command, cfgPath string) (validateJSON, error) {
	var res validateJSON
	fail := func(err error) (validateJSON, error) {
		res.Error = err.Error()
		return res, err
	}

	if _, err := config.Load(cfgPath); err != nil {
		return fail(err)
	}

	path, err := exec.LookPath(connectorBinary)
	if err != nil {
		return fail(fmt.Errorf("%s not found on PATH: %w", connectorBinary, err))
	}
	res.ConnectorAt = path

	var out bytes.Buffer
	c := exec.CommandContext(cmd.Context(), path, "config", "validate")
	c.Stdout = &out
	c.Stderr = &out
	runErr := c.Run()
	res.Output = out.String()
	if runErr == nil {
		res.OK = true
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return fail(fmt.Errorf("%s config validate failed (exit %d)", connectorBinary, res.ExitCode))
	}
	return fail(fmt.Errorf("run %s config validate: %w", connectorBinary, runErr))
}
