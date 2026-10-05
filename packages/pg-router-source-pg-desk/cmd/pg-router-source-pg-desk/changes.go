// changes.go: the envelope-to-items mapping and the adapter's command.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// statusOK is the envelope source status that does not count as degraded.
const statusOK = "ok"

// itemsFromEnvelope maps a decoded envelope to pg-router items: one item
// per (record, kind), in record order then kinds order. It adds no records,
// drops none and decides nothing. The result is always non-nil.
func itemsFromEnvelope(env envelope) ([]rawItem, error) {
	degraded := make([]string, 0, len(env.Sources))
	for _, s := range env.Sources {
		if s.Status != statusOK {
			degraded = append(degraded, s.Query)
		}
	}
	items := make([]rawItem, 0, len(env.Records))
	for _, r := range env.Records {
		seq, err := numberOrZero(r.Seq)
		if err != nil {
			return nil, fmt.Errorf("record %q: seq: %w", r.ID, err)
		}
		version, err := numberOrZero(r.Version)
		if err != nil {
			return nil, fmt.Errorf("record %q: version: %w", r.ID, err)
		}
		for _, kind := range r.Kinds {
			items = append(items, rawItem{
				ID:    r.ID,
				Type:  r.Type + "." + kind,
				Title: r.Title,
				Metadata: map[string]any{
					"entity_type":      r.Type,
					"entity_id":        r.ID,
					"kind":             kind,
					"seq":              seq,
					"version":          version,
					"origin":           r.Origin,
					"degraded_sources": degraded,
				},
			})
		}
	}
	return items, nil
}

// numberOrZero validates a JSON number copied from the envelope and keeps it
// a number (json.Number marshals as its literal).
func numberOrZero(n json.Number) (json.Number, error) {
	if n == "" {
		return json.Number("0"), nil
	}
	if _, err := strconv.ParseFloat(string(n), 64); err != nil {
		return "", err
	}
	return n, nil
}

const rootLong = `Exposes pg-desk's change envelope as pg-router command-query items.

Invocation shape:

  pg-router-source-pg-desk <type> --consumer <name> [--query Q] [--limit N]

It execs "pg-desk <type> changes --consumer <name> [--query Q] [--limit N] --json"
(pg-desk on $PATH), decodes the pg-desk.changes/v1 envelope and prints one JSON
array of items on stdout: one per (record, kind), {id, type "<type>.<kind>",
title, metadata{entity_type, entity_id, kind, seq, version, origin,
degraded_sources}}. --cached is never passed (it does not advance the cursor).

Exit codes (translated, not mirrored): pg-desk 0 or 2 -> 0 with every record
emitted (degraded detail in metadata.degraded_sources); pg-desk 3, any other
pg-desk exit code, an unparseable envelope, or pg-desk not startable -> 1 with
nothing on stdout and the diagnostic on stderr.`

func newRootCmd() *cobra.Command {
	var consumer, query string
	var limit int
	root := &cobra.Command{
		Use:     "pg-router-source-pg-desk <type> --consumer <name>",
		Short:   "Exposes pg-desk's change envelope as pg-router command-query items",
		Long:    rootLong,
		Version: Version,
		Args:    cobra.ExactArgs(1),
		// Diagnostics are written explicitly by this package's own code.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Flags().StringVar(&consumer, "consumer", "", "pg-desk consumer name whose cursor the call reads and advances (required)")
	root.Flags().StringVar(&query, "query", "", "restrict the call to one configured watched query (passed through)")
	root.Flags().IntVar(&limit, "limit", 0, "return at most N records (passed through; 0 = all)")
	_ = root.MarkFlagRequired("consumer")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		argv := []string{args[0], "changes", "--consumer", consumer}
		if query != "" {
			argv = append(argv, "--query", query)
		}
		if cmd.Flags().Changed("limit") {
			argv = append(argv, "--limit", strconv.Itoa(limit))
		}
		argv = append(argv, "--json")

		stderr := cmd.ErrOrStderr()
		res, err := runPgDesk(cmd.Context(), argv)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return errFailed
		}
		if !classifyExit(res.exitCode) {
			if len(res.stderr) > 0 {
				_, _ = stderr.Write(res.stderr)
			}
			fmt.Fprintf(stderr, "pg-router-source-pg-desk: pg-desk exited %d\n", res.exitCode)
			return errFailed
		}
		var env envelope
		if err := json.Unmarshal(res.stdout, &env); err != nil {
			fmt.Fprintf(stderr, "pg-router-source-pg-desk: pg-desk output is not a change envelope: %v\n", err)
			return errFailed
		}
		items, err := itemsFromEnvelope(env)
		if err != nil {
			fmt.Fprintf(stderr, "pg-router-source-pg-desk: malformed envelope: %v\n", err)
			return errFailed
		}
		if err := writeItems(cmd.OutOrStdout(), items); err != nil {
			fmt.Fprintln(stderr, err)
			return errFailed
		}
		return nil
	}
	return root
}
