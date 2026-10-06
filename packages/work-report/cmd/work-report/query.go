package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

func init() { registerCommand(newQueryCmd) }

// queryEntryJSON is the wire shape of one entry in `query --output json`.
type queryEntryJSON struct {
	ID         string          `json:"id"`
	ExternalID string          `json:"external_id"`
	SourceID   string          `json:"source_id"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurred_at"`
	IngestedAt string          `json:"ingested_at"`
	Summary    string          `json:"summary"`
	URL        string          `json:"url"`
	Labels     []string        `json:"labels"`
	Fields     json.RawMessage `json:"fields"`
}

func toQueryJSON(entries []store.Entry) []queryEntryJSON {
	out := make([]queryEntryJSON, 0, len(entries))
	for _, e := range entries {
		labels := e.Labels
		if labels == nil {
			labels = []string{}
		}
		fields := e.Fields
		if len(fields) == 0 {
			fields = json.RawMessage(`{}`)
		}
		out = append(out, queryEntryJSON{
			ID:         e.ID,
			ExternalID: e.ExternalID,
			SourceID:   e.SourceID,
			Type:       e.Type,
			OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339Nano),
			IngestedAt: e.IngestedAt.UTC().Format(time.RFC3339Nano),
			Summary:    e.Summary,
			URL:        e.URL,
			Labels:     labels,
			Fields:     fields,
		})
	}
	return out
}

// queryFormat validates the raw --output value for query: JSON is the default.
func queryFormat(raw string) (string, error) {
	switch raw {
	case "", "json":
		return "json", nil
	case "human":
		return "human", nil
	}
	return "", fmt.Errorf("--output %q is not supported here: want json or human", raw)
}

func newQueryCmd() *cobra.Command {
	var (
		rangeSpec string
		id        string
		types     []string
		labels    []string
		sources   []string
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "List the stored entries for a range, optionally narrowed",
		Long: "Returns the resolved entries (one per id, latest observation wins) whose " +
			"occurrence falls in the range. Narrowing flags compose AND across dimensions " +
			"and OR within one. Range forms: " + rangespec.Forms + ".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := queryFormat(rawOut)
			if err != nil {
				return err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			loc, err := cfg.Location()
			if err != nil {
				return err
			}
			rng, err := rangespec.Resolve(rangeSpec, time.Now(), loc)
			if err != nil {
				return err
			}
			st, err := store.Open(config.StorePath(cfg, storeFlag))
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			entries, err := query.Run(cmd.Context(), st, store.Filter{
				Since: rng.Since, Before: rng.Before, OpenStart: rng.OpenStart,
				ID: id, Types: types, Labels: labels, Sources: sources,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), toQueryJSON(entries))
			}
			return writeQueryHuman(cmd.OutOrStdout(), entries, loc)
		},
	}
	f := cmd.Flags()
	f.StringVar(&rangeSpec, "range", "today", "range spec: "+rangespec.Forms)
	f.StringVar(&id, "id", "", "only the entry with this id")
	f.StringArrayVar(&types, "type", nil, "only entries of this type (repeatable; OR within)")
	f.StringArrayVar(&labels, "label", nil, "only entries carrying this label (repeatable; OR within)")
	f.StringArrayVar(&sources, "source", nil, "only entries from this source (repeatable; OR within)")
	return cmd
}

func writeQueryHuman(w io.Writer, entries []store.Entry, loc *time.Location) error {
	if len(entries) == 0 {
		_, err := io.WriteString(w, "no entries\n")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "OCCURRED\tSOURCE\tTYPE\tID\tLABELS\tSUMMARY")
	for _, e := range entries {
		lbl := "-"
		if len(e.Labels) > 0 {
			lbl = strings.Join(e.Labels, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.OccurredAt.In(loc).Format("2006-01-02 15:04"), e.SourceID, e.Type, e.ID, lbl,
			strings.ReplaceAll(e.Summary, "\n", " "))
	}
	return tw.Flush()
}
