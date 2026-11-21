// changes.go: the envelope-to-items mapping and the adapter's command.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	apievent "github.com/phillipgreenii/pg-router/api/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"

	"github.com/spf13/cobra"
)

// now is the adapter's emit-time clock, a seam so tests can pin the "at"
// stamp --retry-window puts on each item.
var now = time.Now

// stampLayout is the UTC RFC 3339 second-precision layout of at and expiresAt.
const stampLayout = "2006-01-02T15:04:05Z"

// statusOK is the envelope source status that does not count as degraded.
const statusOK = deskapi.StatusOK

// changedSuffix is appended to the entity type to form the emit
// ("<type>.changed"), the only event type the adapter produces.
const changedSuffix = ".changed"

// entityGroup accumulates one entity's records for one poll.
type entityGroup struct {
	id      string
	typ     string
	title   string
	version int64
	origin  string
	seq     int64
	minSeq  int64
	kinds   []string
	seen    map[string]bool
}

// itemsWithLowest maps a decoded envelope to pg-router items: ONE item per
// distinct entity id, in the order of each entity's first record. The item
// carries emit "<type>.changed", id "<entity_id>@<seq>" (seq = the maximum seq
// among the poll's records for the entity) and the coalesced kinds (no
// duplicates, order of first appearance). Title, version and origin come from
// the maximum-seq record (the latest state of the entity). The result is
// always non-nil.
//
// It also returns, per item, the lowest record sequence of its entity in the
// poll (the input of the deferred-ack token rule).
func itemsWithLowest(env deskapi.Changes) ([]apievent.Item, []int64, error) {
	degraded := make([]string, 0, len(env.Sources))
	for _, s := range env.Sources {
		if s.Status != statusOK {
			degraded = append(degraded, s.Query)
		}
	}
	var order []string
	groups := make(map[string]*entityGroup)
	for _, r := range env.Records {
		seq, version := r.Seq, r.Version
		g, ok := groups[r.ID]
		if !ok {
			g = &entityGroup{id: r.ID, typ: r.Type, seen: map[string]bool{}, kinds: []string{}}
			groups[r.ID] = g
			g.minSeq = seq
			g.seq = seq - 1 // so the first record always becomes the latest
			order = append(order, r.ID)
		}
		if seq < g.minSeq {
			g.minSeq = seq
		}
		for _, kind := range r.Kinds {
			if !g.seen[kind] {
				g.seen[kind] = true
				g.kinds = append(g.kinds, kind)
			}
		}
		if seq > g.seq {
			g.seq = seq
			g.typ, g.title, g.version, g.origin = r.Type, r.Title, version, r.Origin
		}
	}
	items := make([]apievent.Item, 0, len(order))
	lows := make([]int64, 0, len(order))
	for _, id := range order {
		g := groups[id]
		lows = append(lows, g.minSeq)
		items = append(items, apievent.Item{
			ID:    g.id + "@" + strconv.FormatInt(g.seq, 10),
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
	return items, lows, nil
}

// withAck orders items by each entity's lowest record sequence and gives item
// k the token (lowest sequence of item k+1) - 1 and the last item the
// envelope's cursor.to, so every record at or below a token belongs to an item
// at or before it. The lane of every item is its entity type.
func withAck(items []apievent.Item, lows []int64, last int64) ([]apievent.Item, error) {
	if len(items) == 0 {
		return items, nil
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return lows[idx[a]] < lows[idx[b]] })
	out := make([]apievent.Item, len(items))
	for k, i := range idx {
		tok := last
		if k+1 < len(idx) {
			tok = lows[idx[k+1]] - 1
		}
		it := items[i]
		it.Ack = &apievent.Ack{Lane: it.Type, Token: strconv.FormatInt(tok, 10)}
		out[k] = it
	}
	return out, nil
}

const rootLong = `Exposes pg-desk's change envelope as pg-router command-query items.

Invocation shape:

  pg-router-source-pg-desk <type> --consumer <name> [--query Q] [--limit N] [--retry-window D]

It execs "pg-desk <type> changes --consumer <name> [--query Q] [--limit N] --json"
(pg-desk on $PATH), decodes the pg-desk.changes/v1 envelope and prints one JSON
array of items on stdout: ONE per changed entity per poll, {id
"<entity_id>@<seq>" (seq = the maximum seq among the poll's records for the
entity), emit "<type>.changed", type, title, metadata{entity_type, entity_id,
kinds (every kind of every record for the entity, coalesced), seq, version,
origin, degraded_sources}}. pg-router types the event from emit, so a query
running this adapter MUST declare emit "<type>.changed". --cached is never
passed (it does not advance the cursor).

--deferred-ack opts one query in to settlement: pg-desk is run with --no-ack
(the cursor does not move), the items are ordered by each entity's lowest
record sequence and each carries ack {"lane": <type>, "token": <decimal
sequence>}, where item k's token is (lowest sequence of item k+1) - 1 and the
last item's is the envelope's cursor.to. The "settle" subcommand, run by the
router once the items are accepted, acknowledges through a token. Without
--deferred-ack nothing changes.

--retry-window D (a Go duration such as 10m; default 0 = off) is NOT passed to
pg-desk. When D is greater than zero every item additionally carries "at" (this
invocation's emit time, one UTC second-precision RFC 3339 value for the whole
call) and "expiresAt" (at + D), so pg-router re-offers a failed dispatch until
the window ends. A negative D exits 1 before pg-desk is run. A router role must
also opt in with max_dispatch_retries to re-run a handler that failed.

Exit codes (translated, not mirrored): pg-desk 0 or 5 -> 0 with every entity
emitted (degraded detail in metadata.degraded_sources); pg-desk 3, any other
pg-desk exit code, an unparseable envelope, or pg-desk not startable -> 1 with
nothing on stdout and the diagnostic on stderr.`

func newRootCmd() *cobra.Command {
	var consumer, query string
	var deferredAck bool
	var limit int
	var retryWindow time.Duration
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
	root.Flags().DurationVar(&retryWindow, "retry-window", 0, "when > 0, give each item at (emit time) and expiresAt (at + window) so pg-router retries a failed dispatch inside the window; 0 (default) = off, not passed to pg-desk")
	root.Flags().BoolVar(&deferredAck, "deferred-ack", false, "opt in to settlement: pass --no-ack to pg-desk, order items by each entity's lowest record sequence and give each an ack {lane, token}; the cursor then moves only when the settle subcommand runs")
	_ = root.MarkFlagRequired("consumer")
	root.AddCommand(newSettleCmd())
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if retryWindow < 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-source-pg-desk: --retry-window must not be negative")
			return errFailed
		}
		argv := []string{args[0], "changes", "--consumer", consumer}
		if query != "" {
			argv = append(argv, "--query", query)
		}
		if cmd.Flags().Changed("limit") {
			argv = append(argv, "--limit", strconv.Itoa(limit))
		}
		if deferredAck {
			argv = append(argv, "--no-ack")
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
		var env deskapi.Changes
		// Lenient on purpose: the contract evolves additively, so an unknown
		// member must not stop a poll (the strict decode is the contract test's).
		if err := json.Unmarshal(res.stdout, &env); err != nil {
			fmt.Fprintf(stderr, "pg-router-source-pg-desk: pg-desk output is not a change envelope: %v\n", err)
			return errFailed
		}
		items, lows, err := itemsWithLowest(env)
		if err == nil && deferredAck {
			items, err = withAck(items, lows, env.Cursor.To)
		}
		if err != nil {
			fmt.Fprintf(stderr, "pg-router-source-pg-desk: malformed envelope: %v\n", err)
			return errFailed
		}
		if retryWindow > 0 {
			// One emit instant for the whole call; expiresAt is computed from the
			// truncated at so expiresAt - at is exactly the window.
			at := now().UTC().Truncate(time.Second)
			stampAt := at.Format(stampLayout)
			stampExp := at.Add(retryWindow).Format(stampLayout)
			for i := range items {
				items[i].At = stampAt
				items[i].ExpiresAt = stampExp
			}
		}
		if err := writeItems(cmd.OutOrStdout(), items); err != nil {
			fmt.Fprintln(stderr, err)
			return errFailed
		}
		return nil
	}
	return root
}
