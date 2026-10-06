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
	_, err := io.WriteString(w, b.String())
	return err
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
