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

// changedSuffix is appended to the entity type to form the emit
// ("<type>.changed"), the only event type the adapter produces.
const changedSuffix = ".changed"

// entityGroup accumulates one entity's records for one poll.
type entityGroup struct {
	id      string
	typ     string
	title   string
	version json.Number
	origin  string
	seq     json.Number
	seqVal  float64
	kinds   []string
	seen    map[string]bool
}

// itemsFromEnvelope maps a decoded envelope to pg-router items: ONE item per
// distinct entity id, in the order of each entity's first record. The item
// carries emit "<type>.changed", id "<entity_id>@<seq>" (seq = the maximum seq
// among the poll's records for the entity) and the coalesced kinds (no
// duplicates, order of first appearance). Title, version and origin come from
// the maximum-seq record (the latest state of the entity). The result is
// always non-nil.
func itemsFromEnvelope(env envelope) ([]rawItem, error) {
	degraded := make([]string, 0, len(env.Sources))
	for _, s := range env.Sources {
		if s.Status != statusOK {
			degraded = append(degraded, s.Query)
		}
	}
	var order []string
	groups := make(map[string]*entityGroup)
	for _, r := range env.Records {
		seq, err := numberOrZero(r.Seq)
		if err != nil {
			return nil, fmt.Errorf("record %q: seq: %w", r.ID, err)
		}
		version, err := numberOrZero(r.Version)
		if err != nil {
			return nil, fmt.Errorf("record %q: version: %w", r.ID, err)
		}
		seqVal, _ := strconv.ParseFloat(string(seq), 64)
		g, ok := groups[r.ID]
		if !ok {
			g = &entityGroup{id: r.ID, typ: r.Type, seen: map[string]bool{}, kinds: []string{}}
			groups[r.ID] = g
			order = append(order, r.ID)
		}
		for _, kind := range r.Kinds {
			if !g.seen[kind] {
				g.seen[kind] = true
				g.kinds = append(g.kinds, kind)
			}
		}
		if g.seq == "" || seqVal > g.seqVal {
			g.seq, g.seqVal = seq, seqVal
			g.typ, g.title, g.version, g.origin = r.Type, r.Title, version, r.Origin
		}
	}
	items := make([]rawItem, 0, len(order))
	for _, id := range order {
		g := groups[id]
		items = append(items, rawItem{
			ID:    g.id + "@" + string(g.seq),
			Emit:  g.typ + changedSuffix,
			Type:  g.typ,
			Title: g.title,
			Metadata: map[string]any{
				"entity_type":      g.typ,
				"entity_id":        g.id,
				"kinds":            g.kinds,
				"seq":              g.seq,
				"version":          g.version,
				"origin":           g.origin,
				"degraded_sources": degraded,
			},
		})
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
array of items on stdout: ONE per changed entity per poll, {id
"<entity_id>@<seq>" (seq = the maximum seq among the poll's records for the
entity), emit "<type>.changed", type, title, metadata{entity_type, entity_id,
kinds (every kind of every record for the entity, coalesced), seq, version,
origin, degraded_sources}}. pg-router types the event from emit, so a query
running this adapter MUST declare emit "<type>.changed". --cached is never
passed (it does not advance the cursor).

Exit codes (translated, not mirrored): pg-desk 0 or 2 -> 0 with every entity
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
