package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
)

// linksCmdFlags is a named type so tests can reset it between runs.
type linksCmdFlags struct {
	jsonOut bool
	stdin   bool
}

var linksFlags linksCmdFlags

// linksCmd implements `pg-desk links --json <type>:<id>... | --stdin`
// [docs/behavior/pg-desk/links.md]: a read-only batch lookup of the
// cross-reference links (PR, build, issue, thread) the local store knows for each
// attention-style ref. It reads the store only (INV-LINKS-1): no network, no
// hydration, no write. An unknown ref is data (known:false), not an error
// (INV-LINKS-2). Exit 0 on any answer; exit 1 when the store cannot be read,
// the config cannot be loaded, or no refs were given.
var linksCmd = &cobra.Command{
	Use:   "links [--json] (<type>:<id>... | --stdin)",
	Short: "Print the cross-reference links known for a batch of refs (read-only, local store only)",
	Long: `Print, as JSON, the cross-reference links pg-desk's local store knows for each
<type>:<id> ref (an attention item's type and id unchanged, e.g. pr:owner/repo#123).
Reads the store only: no network, no hydration, no write. An unknown ref yields
known:false with exit 0; exit 1 means the store is unreadable. Output is always JSON.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return execLinks(cmd, args)
	},
}

func init() {
	linksCmd.Flags().BoolVar(&linksFlags.jsonOut, "json", false,
		"Accepted for symmetry with the other verbs; the output is always JSON")
	linksCmd.Flags().BoolVar(&linksFlags.stdin, "stdin", false,
		"Read one <type>:<id> ref per line from standard input")
	rootCmd.AddCommand(linksCmd)
}

func execLinks(cmd *cobra.Command, args []string) error {
	refs := append([]string(nil), args...)
	if linksFlags.stdin {
		fromStdin, err := readRefLines(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("links: read stdin: %w", err)
		}
		refs = append(refs, fromStdin...)
	} else if len(refs) == 0 {
		return fmt.Errorf("links: give at least one <type>:<id> ref, or --stdin")
	}

	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("links: load config: %w", err)
	}
	if len(cfg.Repos) == 0 {
		return fmt.Errorf("links: no repository configured")
	}
	st, err := deskStoreOpenReadOnly()
	if err != nil {
		return fmt.Errorf("links: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	res, err := links.Resolve(links.Deps{
		Store:             st,
		Repo:              cfg.Repos[0].Remote,
		Patterns:          cfg.TicketPatterns,
		IssueURLTemplate:  cfg.Links.IssueURLTemplate,
		CheckInterpreters: cfg.CheckInterpreters,
	}, refs)
	if err != nil {
		return fmt.Errorf("links: %w", err)
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("links: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return err
}

// readRefLines returns the non-blank lines of r, trimmed.
func readRefLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}
