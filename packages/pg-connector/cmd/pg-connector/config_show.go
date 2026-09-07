// config_show.go: the "pg-connector config show" Tier-1-only CLI verb
// [finding A30, bead pg2-gprc3]. Unlike "config validate" (config_validate.go),
// which fans auth_status/capabilities out across every registered backend to
// answer "is what's registered healthy," "config show" never invokes a
// backend at all — it answers the prior question this build previously had
// no verb for: which config file did pg-connector actually resolve
// (registry.go's $PG_PR_CONFIG -> XDG -> ~/.config order), and what does
// connector.<type> in it actually register per entity type.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// ConfigShowResult is "config show"'s own result payload: the resolved
// config file path plus each entity type's registered backend(s), in
// registry.go's own list-valued (pr/issue/ci) vs single-valued (scm) shape
// (INV-REG-1) — rather than force-fitting scm's single backend into a
// one-element list just to give every entity type the same Go type.
type ConfigShowResult struct {
	ConfigPath string   `json:"config_path"`
	PR         []string `json:"pr,omitempty"`
	Issue      []string `json:"issue,omitempty"`
	CI         []string `json:"ci,omitempty"`
	Scm        string   `json:"scm,omitempty"`
}

// buildConfigShowResult resolves and parses the registry exactly as
// LoadRegistry does (registry.go), then reads every entityTypes entry via
// Registry's own List/Single — reusing that existing per-type
// list-vs-scalar validation rather than a second, parallel decode.
func buildConfigShowResult() (*ConfigShowResult, error) {
	path, err := ResolveConfigPath()
	if err != nil {
		return nil, err
	}
	reg, err := loadRegistryFile(path)
	if err != nil {
		return nil, err
	}
	pr, err := reg.List("pr")
	if err != nil {
		return nil, err
	}
	issue, err := reg.List("issue")
	if err != nil {
		return nil, err
	}
	ci, err := reg.List("ci")
	if err != nil {
		return nil, err
	}
	scm, err := reg.Single("scm")
	if err != nil {
		return nil, err
	}
	return &ConfigShowResult{ConfigPath: path, PR: pr, Issue: issue, CI: ci, Scm: scm}, nil
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config file path and its registered connector.<type> backends",
		Long: "Print the resolved config file path (registry.go's $PG_PR_CONFIG -> $XDG_CONFIG_HOME -> ~/.config\n" +
			"resolution order) and, for each connector.<type> entity type, the backend(s) it registers there —\n" +
			"without invoking any backend. Use \"config validate\" to check whether what's registered is healthy.",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := buildConfigShowResult()
			if err != nil {
				return err
			}
			return writeConfigShowResult(cmd, result)
		},
	}
}

// writeConfigShowResult renders result per the persistent --output flag
// (output.go), mirroring writeFanOutResult/writeTargetedResult's own
// json-default/human-opt-in split — but "config show" never has a wire
// envelope or a per-op exit code of its own to compute: a resolved,
// parseable config is unconditionally exit 0 here (an unresolved/unparsable
// one is reported through RunE's plain err return above, before this is
// ever reached).
func writeConfigShowResult(cmd *cobra.Command, result *ConfigShowResult) error {
	mode, err := outputModeFor(cmd)
	if err != nil {
		return err
	}
	if mode == OutputHuman {
		fmt.Fprintln(cmd.OutOrStdout(), formatConfigShow(result))
		return nil
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	return enc.Encode(result)
}

// formatConfigShow renders result's human-readable form: the resolved path,
// then one line per entity type naming its registered backend(s) — "(none
// registered)" for a type with no connector.<type> entry at all, matching
// registry.go's own List/Single "no entry -> empty, not an error" contract.
func formatConfigShow(r *ConfigShowResult) string {
	line := func(label string, backends []string) string {
		if len(backends) == 0 {
			return fmt.Sprintf("  %s: (none registered)", label)
		}
		out := fmt.Sprintf("  %s:", label)
		for _, b := range backends {
			out += " " + b
		}
		return out
	}
	scmLine := "  scm: (none registered)"
	if r.Scm != "" {
		scmLine = "  scm: " + r.Scm
	}
	return fmt.Sprintf("config show:\n  config_path: %s\n%s\n%s\n%s\n%s",
		r.ConfigPath, line("pr", r.PR), line("issue", r.Issue), line("ci", r.CI), scmLine)
}
