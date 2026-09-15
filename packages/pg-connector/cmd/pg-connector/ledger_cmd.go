// ledger_cmd.go: the "pg-connector ledger show"/"pg-connector ledger
// clear" top-level command group — the operator-facing inspection and
// reset surface over ledger.go's on-disk delta ledger [design: section
// 5.1]. Registered from root.go as a new top-level command
// (newLedgerCmd()), a sibling of pr/issue/ci/scm/auth/config, since a
// ledger file is keyed by (type, backend, query) and so is never scoped
// to one entity-type's own verb group the way "changes" (changes.go) is.
//
// Neither verb here ever dispatches to a backend — both operate purely
// on the local on-disk ledger state ledger.go's own loadLedger/
// deleteLedger/ListLedgerKeys already provide — so there is no
// SourceResult/fan-out exit-code scheme to reuse (outcome.go's 0/2/3
// applies to a BACKEND health outcome; there is no such outcome here). A
// well-formed call always reports its result at exit 0; a filter that
// matches zero ledgers is not an error (an empty result list), matching
// every other Registry-style accessor's own "absent -> zero value, not
// an error" convention this module already uses throughout.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// filterLedgerKeys resolves which of keys match the given PARTIAL filter
// — this packet's own Contract/Consumes section's matching rule: a key
// MATCHES iff every flag that WAS given (non-empty) equals that key's
// corresponding field; an omitted (empty) flag matches any value in that
// field. Order is preserved from keys (ListLedgerKeys' own directory-scan
// order); callers needing a stable display order sort afterward.
func filterLedgerKeys(keys []LedgerKey, typ, backend, query string) []LedgerKey {
	out := make([]LedgerKey, 0, len(keys))
	for _, k := range keys {
		if typ != "" && k.Type != typ {
			continue
		}
		if backend != "" && k.Backend != backend {
			continue
		}
		if query != "" && k.Query != query {
			continue
		}
		out = append(out, k)
	}
	return out
}

func newLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Inspect or reset the delta ledger's on-disk state",
	}
	cmd.AddCommand(newLedgerShowCmd())
	cmd.AddCommand(newLedgerClearCmd())
	return cmd
}

// ledgerShowRow is one matching (type, backend, query)'s own printed
// facts: fetch cursor, index size, version, and consumer position(s) —
// the four facts this packet's own Contract requires ledger show to
// print per matching key [design: section 5.1]. Exact text/JSON layout
// beyond carrying all four is this packet's own freedom-boundary choice.
type ledgerShowRow struct {
	Type      string                   `json:"type"`
	Backend   string                   `json:"backend"`
	Query     string                   `json:"query"`
	Cursor    json.RawMessage          `json:"cursor"`
	IndexSize int                      `json:"index_size"`
	Version   int64                    `json:"version"`
	Consumers map[string]ConsumerState `json:"consumers"`
}

func newLedgerShowCmd() *cobra.Command {
	var typ, backend, query, consumer string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the stored cursor, index size, version, and consumer position(s) for matching ledgers",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&typ, "type", "", "filter to this entity type")
	cmd.Flags().StringVar(&backend, "backend", "", "filter to this backend")
	cmd.Flags().StringVar(&query, "query", "", "filter to this named query")
	cmd.Flags().StringVar(&consumer, "consumer", "", "narrow the printed consumer-position line(s) to this one consumer id, without narrowing which ledgers match")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		keys, err := ListLedgerKeys()
		if err != nil {
			return err
		}
		matched := filterLedgerKeys(keys, typ, backend, query)
		rows := make([]ledgerShowRow, 0, len(matched))
		for _, k := range matched {
			l, err := loadLedger(k)
			if err != nil {
				return err
			}
			rows = append(rows, buildLedgerShowRow(k, l, consumer))
		}
		return writeFanOutResult(cmd, rows, 0, func() string { return humanizeLedgerShowRows(rows) })
	}
	return cmd
}

// buildLedgerShowRow assembles k's own show row from l — consumer narrows
// the Consumers map to that one id (this packet's own Contract: "an
// ADDITIONAL filter... narrows ledger show's printed consumer-position
// line(s) to that one consumer id within each matching ledger, without
// narrowing which ledgers match"); "" means every consumer this ledger
// tracks.
func buildLedgerShowRow(k LedgerKey, l *Ledger, consumer string) ledgerShowRow {
	row := ledgerShowRow{
		Type:      k.Type,
		Backend:   k.Backend,
		Query:     k.Query,
		Cursor:    l.Cursor,
		IndexSize: len(l.Entries),
		Version:   l.Version,
		Consumers: make(map[string]ConsumerState),
	}
	if consumer != "" {
		if cs, ok := l.Consumers[consumer]; ok {
			row.Consumers[consumer] = cs
		}
		return row
	}
	for id, cs := range l.Consumers {
		row.Consumers[id] = cs
	}
	return row
}

func humanizeLedgerShowRows(rows []ledgerShowRow) string {
	if len(rows) == 0 {
		return "ledger: (no matching ledgers)"
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s/%s/%s: cursor=%s index_size=%d version=%d\n", r.Type, r.Backend, r.Query, cursorText(r.Cursor), r.IndexSize, r.Version)
		if len(r.Consumers) == 0 {
			b.WriteString("  consumers: (none)")
			continue
		}
		b.WriteString("  consumers:")
		for id, cs := range r.Consumers {
			fmt.Fprintf(&b, "\n    %s: cursor=%d last_seen=%s", id, cs.Cursor, cs.LastSeen.Format("2006-01-02T15:04:05Z07:00"))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func cursorText(cursor json.RawMessage) string {
	if len(cursor) == 0 {
		return "null"
	}
	return string(cursor)
}

// ledgerClearRow reports one ledger file this call actually removed.
type ledgerClearRow struct {
	Type    string `json:"type"`
	Backend string `json:"backend"`
	Query   string `json:"query"`
}

type ledgerClearResult struct {
	Cleared []ledgerClearRow `json:"cleared"`
}

func newLedgerClearCmd() *cobra.Command {
	var typ, backend, query string
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete the on-disk ledger file(s) matching the given filters entirely",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&typ, "type", "", "filter to this entity type")
	cmd.Flags().StringVar(&backend, "backend", "", "filter to this backend")
	cmd.Flags().StringVar(&query, "query", "", "filter to this named query")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		keys, err := ListLedgerKeys()
		if err != nil {
			return err
		}
		matched := filterLedgerKeys(keys, typ, backend, query)
		result := ledgerClearResult{Cleared: make([]ledgerClearRow, 0, len(matched))}
		for _, k := range matched {
			if err := deleteLedger(k); err != nil {
				return err
			}
			result.Cleared = append(result.Cleared, ledgerClearRow{Type: k.Type, Backend: k.Backend, Query: k.Query})
		}
		return writeFanOutResult(cmd, result, 0, func() string { return humanizeLedgerClearResult(result) })
	}
	return cmd
}

func humanizeLedgerClearResult(result ledgerClearResult) string {
	if len(result.Cleared) == 0 {
		return "ledger clear: (no matching ledgers)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cleared (%d):\n", len(result.Cleared))
	for _, c := range result.Cleared {
		fmt.Fprintf(&b, "  %s/%s/%s\n", c.Type, c.Backend, c.Query)
	}
	return strings.TrimRight(b.String(), "\n")
}
