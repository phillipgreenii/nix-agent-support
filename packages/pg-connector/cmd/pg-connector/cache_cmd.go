// cache_cmd.go: the "pg-connector cache show"/"pg-connector cache clear"
// top-level command group — the operator-facing inspection and reset
// surface over cache.go's on-disk entity cache, mirroring
// ledger_cmd.go's own "pg-connector ledger show"/"pg-connector ledger
// clear" pattern exactly [design: docket design field, "Design elements",
// "Verbs"; design of record section 5.1]. Registered from root.go as a
// new top-level command (newCacheCmd()), a sibling of ledger/pr/issue/
// etc., since a cache file — like a ledger file — is keyed by (type,
// backend) and is never scoped to one entity-type's own verb group.
//
// Neither verb here ever dispatches to a backend — both operate purely
// on the local on-disk cache state cache.go's own ListCacheKeys/
// loadCache/deleteCache already provide — so there is no
// SourceResult/fan-out exit-code scheme to reuse, mirroring
// ledger_cmd.go's own rationale exactly: a well-formed call always
// reports its result at exit 0; a filter that matches zero caches is not
// an error (an empty result list), matching every other Registry-style
// accessor's own "absent -> zero value, not an error" convention this
// module already uses throughout.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// filterCacheKeys resolves which of keys match the given PARTIAL filter —
// the same matching rule filterLedgerKeys already implements for
// LedgerKey: a key MATCHES iff every flag that WAS given (non-empty)
// equals that key's corresponding field; an omitted (empty) flag matches
// any value in that field. CacheKey has no Query field, so this filter
// takes only typ/backend. Order is preserved from keys (ListCacheKeys'
// own directory-scan order).
func filterCacheKeys(keys []CacheKey, typ, backend string) []CacheKey {
	out := make([]CacheKey, 0, len(keys))
	for _, k := range keys {
		if typ != "" && k.Type != typ {
			continue
		}
		if backend != "" && k.Backend != backend {
			continue
		}
		out = append(out, k)
	}
	return out
}

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect or reset the umbrella entity cache's on-disk state",
	}
	cmd.AddCommand(newCacheShowCmd())
	cmd.AddCommand(newCacheClearCmd())
	return cmd
}

// cacheShowRow is one matching (type, backend)'s own printed facts: live
// and tombstoned entry counts, reported separately — the one fact this
// packet's own Contract requires cache show to print per matching key
// [design: docket design field, "Contract", "Produces"]. Nothing else is
// required beyond "inspect cache state."
type cacheShowRow struct {
	Type              string `json:"type"`
	Backend           string `json:"backend"`
	LiveEntries       int    `json:"live_entries"`
	TombstonedEntries int    `json:"tombstoned_entries"`
}

func newCacheShowCmd() *cobra.Command {
	var typ, backend string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the live and tombstoned entry counts for matching caches",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&typ, "type", "", "filter to this entity type")
	cmd.Flags().StringVar(&backend, "backend", "", "filter to this backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		keys, err := ListCacheKeys()
		if err != nil {
			return err
		}
		matched := filterCacheKeys(keys, typ, backend)
		rows := make([]cacheShowRow, 0, len(matched))
		for _, k := range matched {
			c, err := loadCache(k)
			if err != nil {
				return err
			}
			rows = append(rows, buildCacheShowRow(k, c))
		}
		return writeFanOutResult(cmd, rows, 0, func() string { return humanizeCacheShowRows(rows) })
	}
	return cmd
}

// buildCacheShowRow assembles k's own show row from c, counting live
// (RemovedAt == nil) and tombstoned (RemovedAt != nil) entries
// separately.
func buildCacheShowRow(k CacheKey, c *Cache) cacheShowRow {
	row := cacheShowRow{Type: k.Type, Backend: k.Backend}
	for _, entry := range c.Entries {
		if entry.RemovedAt != nil {
			row.TombstonedEntries++
		} else {
			row.LiveEntries++
		}
	}
	return row
}

func humanizeCacheShowRows(rows []cacheShowRow) string {
	if len(rows) == 0 {
		return "cache: (no matching caches)"
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s/%s: live=%d tombstoned=%d", r.Type, r.Backend, r.LiveEntries, r.TombstonedEntries)
	}
	return b.String()
}

// cacheClearRow reports one cache file this call actually removed — same
// shape/pattern as ledgerClearRow.
type cacheClearRow struct {
	Type    string `json:"type"`
	Backend string `json:"backend"`
}

type cacheClearResult struct {
	Cleared []cacheClearRow `json:"cleared"`
}

func newCacheClearCmd() *cobra.Command {
	var typ, backend string
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete the on-disk cache file(s) matching the given filters entirely",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&typ, "type", "", "filter to this entity type")
	cmd.Flags().StringVar(&backend, "backend", "", "filter to this backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		keys, err := ListCacheKeys()
		if err != nil {
			return err
		}
		matched := filterCacheKeys(keys, typ, backend)
		result := cacheClearResult{Cleared: make([]cacheClearRow, 0, len(matched))}
		for _, k := range matched {
			if err := deleteCache(k); err != nil {
				return err
			}
			result.Cleared = append(result.Cleared, cacheClearRow(k))
		}
		return writeFanOutResult(cmd, result, 0, func() string { return humanizeCacheClearResult(result) })
	}
	return cmd
}

func humanizeCacheClearResult(result cacheClearResult) string {
	if len(result.Cleared) == 0 {
		return "cache clear: (no matching caches)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cleared (%d):\n", len(result.Cleared))
	for _, c := range result.Cleared {
		fmt.Fprintf(&b, "  %s/%s\n", c.Type, c.Backend)
	}
	return strings.TrimRight(b.String(), "\n")
}
