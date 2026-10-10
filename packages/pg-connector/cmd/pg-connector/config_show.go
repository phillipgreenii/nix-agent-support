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
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// ConfigShowResult is "config show"'s own result payload: the resolved
// config file path plus each entity type's registered backend(s), in
// registry.go's own list-valued (pr/issue/ci) vs single-valued (scm) shape
// (INV-REG-1) — rather than force-fitting scm's single backend into a
// one-element list just to give every entity type the same Go type.
//
// Queries is populated only when --queries is passed (bead
// pg2-2j5ac.28.1) — backend binary name -> its own sorted registered
// query names (backends.<name>.queries), covering every backend
// registered under a list-capable type (pr/issue) — printed "on demand"
// (this packet's own Files-section note) rather than unconditionally, so
// an operator not using named queries at all sees no new noise in the
// default "config show" output. Never invokes a backend, matching this
// command's own existing "never invokes a backend" convention.
type ConfigShowResult struct {
	ConfigPath string   `json:"config_path"`
	PR         []string `json:"pr,omitempty"`
	Issue      []string `json:"issue,omitempty"`
	CI         []string `json:"ci,omitempty"`
	Scm        string   `json:"scm,omitempty"`
	// Attention/Search/Activity are the registered names under the
	// top-level attention.sources/search.sources/activity.sources keys, and
	// Commands maps each {name, command} instance's name to its argv
	// (bead pg2-91y12, INV-REG-4). All four are omitempty, so a config with
	// only plain strings and no sources renders exactly as it did before.
	Attention []string            `json:"attention,omitempty"`
	Search    []string            `json:"search,omitempty"`
	Activity  []string            `json:"activity,omitempty"`
	Commands  map[string][]string `json:"commands,omitempty"`
	Queries   map[string][]string `json:"queries,omitempty"`
}

// buildConfigShowResult resolves and parses the registry exactly as
// LoadRegistry does (registry.go), then reads every entityTypes entry via
// Registry's own List/Single — reusing that existing per-type
// list-vs-scalar validation rather than a second, parallel decode.
// withQueries populates ConfigShowResult.Queries when true.
func buildConfigShowResult(withQueries bool) (*ConfigShowResult, error) {
	path, err := ResolveConfigPath()
	if err != nil {
		return nil, err
	}
	reg, err := loadRegistryFile(path)
	if err != nil {
		return nil, err
	}
	// Surface a malformed entry anywhere (including attention/search/
	// activity sources, which no connector fan-out exercises) instead of
	// letting it pass silently.
	if err := reg.Validate(); err != nil {
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
	result := &ConfigShowResult{ConfigPath: path, PR: pr, Issue: issue, CI: ci, Scm: scm}
	if result.Attention, err = reg.AttentionSources(); err != nil {
		return nil, err
	}
	if result.Search, err = reg.SearchSources(); err != nil {
		return nil, err
	}
	if result.Activity, err = reg.ActivitySources(); err != nil {
		return nil, err
	}
	if commands := reg.Explicit(); len(commands) > 0 {
		result.Commands = commands
	}
	if withQueries {
		result.Queries = map[string][]string{}
		for _, entityType := range entityTypesWithList {
			for _, b := range result.backendsForType(entityType) {
				if _, ok := result.Queries[b]; ok {
					continue
				}
				names, namesErr := reg.BackendQueryNames(b)
				if namesErr != nil {
					return nil, namesErr
				}
				result.Queries[b] = names
			}
		}
	}
	return result, nil
}

// backendsForType returns r's own already-loaded backend list for
// entityType ("pr" or "issue" — the two entityTypesWithList members) —
// a plain field lookup, since buildConfigShowResult has already resolved
// both into r.PR/r.Issue.
func (r *ConfigShowResult) backendsForType(entityType string) []string {
	switch entityType {
	case "pr":
		return r.PR
	case "issue":
		return r.Issue
	default:
		return nil
	}
}

const configShowQueriesFlagName = "queries"

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config file path and its registered connector.<type> backends",
		Long: "Print the resolved config file path (registry.go's $PG_PR_CONFIG -> $XDG_CONFIG_HOME -> ~/.config\n" +
			"resolution order) and, for each connector.<type> entity type, the backend(s) it registers there —\n" +
			"without invoking any backend. It validates the whole registry first, so a malformed entry under\n" +
			"attention.sources, search.sources or activity.sources fails this verb too.\n" +
			"Use \"config validate\" to check whether what's registered is healthy.\n" +
			"--queries additionally prints, per pr/issue backend, the query names its own backends.<name>.queries\n" +
			"config block defines, without resolving or invoking any of them.",
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		withQueries, err := cmd.Flags().GetBool(configShowQueriesFlagName)
		if err != nil {
			return err
		}
		result, err := buildConfigShowResult(withQueries)
		if err != nil {
			return err
		}
		return writeConfigShowResult(cmd, result)
	}
	cmd.Flags().Bool(configShowQueriesFlagName, false, "additionally print each pr/issue backend's own registered query names, without invoking any backend")
	return cmd
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
	out := fmt.Sprintf("config show:\n  config_path: %s\n%s\n%s\n%s\n%s",
		r.ConfigPath, line("pr", r.PR), line("issue", r.Issue), line("ci", r.CI), scmLine)
	for _, src := range []struct {
		label string
		names []string
	}{{"attention", r.Attention}, {"search", r.Search}, {"activity", r.Activity}} {
		if len(src.names) > 0 {
			out += "\n" + line(src.label, src.names)
		}
	}
	if len(r.Commands) > 0 {
		out += "\n  commands:"
		for _, name := range sortedKeys(r.Commands) {
			out += fmt.Sprintf("\n    %s: %s", name, strings.Join(r.Commands[name], " "))
		}
	}
	if r.Queries != nil {
		out += "\n  queries:"
		if len(r.Queries) == 0 {
			out += " (none registered)"
		} else {
			names := make([]string, 0, len(r.Queries))
			for b := range r.Queries {
				names = append(names, b)
			}
			sort.Strings(names)
			for _, b := range names {
				if len(r.Queries[b]) == 0 {
					out += fmt.Sprintf("\n    %s: (none defined)", b)
				} else {
					out += fmt.Sprintf("\n    %s: %s", b, strings.Join(r.Queries[b], ", "))
				}
			}
		}
	}
	return out
}
