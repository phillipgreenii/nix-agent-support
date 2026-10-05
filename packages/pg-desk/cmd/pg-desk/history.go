package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
)

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newHistoryCmd(t))
	}
}

// newHistoryCmd builds `pg-desk <type> history <id> [--limit N]`: the change
// records of one entity, newest first [design 9.2]. It is a typed verb with
// no old-schema counterpart, so it refuses an old-schema store.
func newHistoryCmd(entityType string) *cobra.Command {
	var limit int
	var jsonOut bool
	c := &cobra.Command{
		Use:   "history <id>",
		Short: fmt.Sprintf("List a %s's change records, newest first", entityType),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistory(cmd, entityType, args[0], limit, resolveJSONOutput(jsonOut))
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "Return at most N records (0 = all)")
	c.Flags().BoolVar(&jsonOut, "json", false, `Print {"records": [...]} instead of one line per record`)
	return c
}

func runHistory(cmd *cobra.Command, entityType, ref string, limit int, jsonOut bool) error {
	if limit < 0 {
		return fmt.Errorf("history: --limit must be >= 0, got %d", limit)
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("history: load config: %w", err)
	}
	repo, id, err := resolveTypedRef(cfg, entityType, ref)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("history: open store: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := requireNewSchemaForTypedVerb(st); err != nil {
		return err
	}

	rows, err := st.ListEntityHistory(repo, entityType, id, limit)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	title := ""
	if ent, found, gerr := st.GetEntity(repo, entityType, id); gerr != nil {
		return fmt.Errorf("history: read entity: %w", gerr)
	} else if found {
		title = changes.TitleFromFacts(entityType, ent.Facts)
	}

	records := make([]changes.Record, 0, len(rows))
	for _, r := range rows {
		kinds := r.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		records = append(records, changes.Record{
			Seq: r.Seq, Type: r.EntityType, ID: r.EntityID, Title: title,
			Version: r.Version, Kinds: kinds, Origin: r.Origin, At: r.At,
		})
	}

	if jsonOut {
		return writeHistoryJSON(cmd.OutOrStdout(), records)
	}
	return renderHistory(cmd.OutOrStdout(), records)
}

type historyPayload struct {
	Records []changes.Record `json:"records"`
}

func writeHistoryJSON(w io.Writer, records []changes.Record) error {
	b, err := json.MarshalIndent(historyPayload{Records: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("history: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// renderHistory prints one line per record:
// <seq>  <at>  <kinds joined by ", ">  origin=<origin>.
func renderHistory(w io.Writer, records []changes.Record) error {
	for _, r := range records {
		if _, err := fmt.Fprintf(w, "%d  %s  %s  origin=%s\n", r.Seq, r.At, strings.Join(r.Kinds, ", "), r.Origin); err != nil {
			return err
		}
	}
	return nil
}
