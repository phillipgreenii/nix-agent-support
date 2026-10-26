package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
	"github.com/spf13/cobra"
)

// specListEntry is the --format=json wire shape for one command name that has
// a spec: the name plus every layer that supplies one.
type specListEntry struct {
	Name   string   `json:"name"`
	Layers []string `json:"layers"`
}

func newSpecsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "specs",
		Short: "Inspect which commands have a spec (read-only)",
	}
	cmd.AddCommand(newSpecsListCmd())
	return cmd
}

func newSpecsListCmd() *cobra.Command {
	var userDir, repoDir, format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every command name that has a command spec, with the layers that supply it",
		Long: `list answers "does a spec exist for command X?" without grepping Go source.
A command name is listed when any of these layers supplies a command spec:

  registry  cmddesc.DefaultRegistry(), the table the live engine consults
  embedded  the built-in internal/embeddedspecs/data/*.json files
  user      --user-dir (default ~/.config/claude-extended-tool-approver)
  repo      --repo-dir (default ./.ceta)

A missing user or repo directory is an empty layer. A spec file that fails
validation is not listed. The command only reads; it never writes or lints.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecsList(cmd, userDir, repoDir, format)
		},
	}
	defUser, _ := specfmt.DefaultUserDir()
	cmd.Flags().StringVar(&userDir, "user-dir", defUser, "User-level spec layer directory")
	cmd.Flags().StringVar(&repoDir, "repo-dir", specfmt.DefaultRepoDir("."), "Repo-level spec layer directory")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text|json")
	return cmd
}

func runSpecsList(cmd *cobra.Command, userDir, repoDir, format string) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("specs list: unknown --format %q (want text|json)", format)
	}
	entries, err := listSpecNames(userDir, repoDir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	for _, e := range entries {
		fmt.Fprintf(out, "%s\t%v\n", e.Name, e.Layers)
	}
	return nil
}

// listSpecNames merges the command names of every layer, sorted by name.
func listSpecNames(userDir, repoDir string) ([]specListEntry, error) {
	layers := map[string]map[string]bool{}
	add := func(layer, name string) {
		if layers[name] == nil {
			layers[name] = map[string]bool{}
		}
		layers[name][layer] = true
	}
	for _, n := range cmddesc.DefaultRegistry().Names() {
		add("registry", n)
	}
	load := func(layer string, embedded fs.FS, user, repo string) error {
		set, err := specfmt.NewRepository(embedded, user, repo).Load()
		if err != nil {
			return fmt.Errorf("specs list: loading %s layer: %w", layer, err)
		}
		for n := range set.Commands {
			add(layer, n)
		}
		return nil
	}
	if err := load("embedded", embeddedspecs.FS, "", ""); err != nil {
		return nil, err
	}
	if err := load("user", nil, userDir, ""); err != nil {
		return nil, err
	}
	if err := load("repo", nil, "", repoDir); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(layers))
	for n := range layers {
		names = append(names, n)
	}
	sort.Strings(names)
	entries := make([]specListEntry, 0, len(names))
	for _, n := range names {
		var ls []string
		for _, l := range []string{"registry", "embedded", "user", "repo"} {
			if layers[n][l] {
				ls = append(ls, l)
			}
		}
		entries = append(entries, specListEntry{Name: n, Layers: ls})
	}
	return entries, nil
}
