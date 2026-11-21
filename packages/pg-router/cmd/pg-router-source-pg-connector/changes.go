// changes.go: the "changes" verb — runs
// `pg-connector <type> changes --query <query> --consumer <id> --output
// json` and reprints its wire response as one pg-router rawItem array
// [design: section 6.1].
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	apievent "github.com/phillipgreenii/pg-router/api/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"

	"github.com/spf13/cobra"
)

// changesNow is the adapter's emit-time clock, a seam so tests can pin the
// "at" stamp --retry-window puts on each item (bead pg2-1ldvy).
var changesNow = time.Now

// emittedAtLayout is RFC 3339 UTC with millisecond precision (metadata.emitted_at).
const emittedAtLayout = "2006-01-02T15:04:05.000Z"

// retryIDHexLen is how many hex characters of the change-row digest the
// retry-window id suffix carries (bead pg2-1ldvy: "12 hex of sha256").
const retryIDHexLen = 12

// isAbsentRaw reports whether raw is missing or the JSON null.
func isAbsentRaw(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

// decodeChangeExtras validates and returns the optional seq, kinds and fields
// of one change row, as the values printed under metadata.seq, metadata.kinds
// and metadata.fields. Each is independent: an absent or null one is omitted
// from the result. A seq that is not a number, a kinds that is not an array of
// strings, or a fields that is not an array is an error. The elements of
// fields are not inspected and fields is kept as raw JSON, never re-encoded.
func decodeChangeExtras(c client.Change) (map[string]any, error) {
	extras := make(map[string]any, 3)
	if c.Seq != nil {
		extras["seq"] = json.RawMessage(strconv.FormatInt(*c.Seq, 10))
	}
	if !isAbsentRaw(c.Kinds) {
		var kinds []string
		if err := json.Unmarshal(c.Kinds, &kinds); err != nil {
			return nil, fmt.Errorf("change %q: kinds is not an array of strings: %w", c.Change, err)
		}
		if kinds == nil {
			kinds = []string{}
		}
		extras["kinds"] = kinds
	}
	if !isAbsentRaw(c.Fields) {
		var fields []json.RawMessage
		if err := json.Unmarshal(c.Fields, &fields); err != nil {
			return nil, fmt.Errorf("change %q: fields is not an array: %w", c.Change, err)
		}
		extras["fields"] = compactRaw(c.Fields)
	}
	return extras, nil
}

// entityIdentity decodes the subset of an entity's own wire shape
// (schema.PR/schema.Issue) this file needs: its own id and, if
// non-empty, its own title. Every entity pg-connector returns is
// schema-conformant and always carries a non-null id/title (phase 7,
// landed) [design: section 6.1, Freedom boundary].
//
// HeadSHA and Version (bead pg2-1ldvy) are the only other entity fields the
// retry-window change digest reads (changeDigest below); kept raw so any JSON
// type round-trips into the digest unchanged. Absent or null means "not
// present".
type entityIdentity struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	HeadSHA json.RawMessage `json:"head_sha"`
	Version json.RawMessage `json:"version"`
}

// changeIdentity is the canonical-JSON shape hashed into a retry-window item
// id (bead pg2-1ldvy). It names ONLY fields that are stable across polls of an
// unchanged row: the change kind, the source backend, the entity id, and the
// entity's head_sha / version when it carries one. It deliberately omits
// every entity field that varies per fetch (as_of, stale — the same two
// pg-connector's own ledger hash excludes) and every content field (title,
// body, comment counts, ...): a content-only change on the same head
// coalesces into the earlier id, which is the point of the bounded retry
// window. Struct field order IS the canonical key order, so the encoding is
// byte-stable; empty head_sha/version are omitted.
type changeIdentity struct {
	Change   string          `json:"change"`
	Source   string          `json:"source"`
	EntityID string          `json:"entity_id"`
	HeadSHA  json.RawMessage `json:"head_sha,omitempty"`
	Version  json.RawMessage `json:"version,omitempty"`
	// EmittedAt is set only for an "added" row of an entity with neither
	// head_sha nor version (see changeDigest), so a re-entry into the query is
	// a new id; empty (omitted) for every other row.
	EmittedAt string `json:"emitted_at,omitempty"`
	// Seq is set instead of EmittedAt under --deferred-ack: the row's own
	// seq, so a redelivered row is the same id and a re-entry (a new log
	// row, a new seq) is a new id. Empty (omitted) otherwise.
	Seq string `json:"seq,omitempty"`
}

// compactRaw returns raw as compact JSON, or nil when raw is empty or the
// JSON null (so omitempty drops it from changeIdentity).
func compactRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// changeDigestSeq returns the first retryIDHexLen hex characters of the SHA-256
// over the canonical JSON of c's changeIdentity.
//
// An "added" row of an entity that carries neither head_sha nor version (a
// bead) also digests emittedAt: nothing else tells one entry into the query
// from the next, and without it a re-entry would reuse the first entry's id
// and the router would drop it as a duplicate (INV-EVT-3). Every other row,
// "changed" included, is digested without it, so content-only changes of an
// entity that stays in the query still coalesce.
//
// changeDigestSeq is changeDigest with the --deferred-ack key: when seq is non-empty (--deferred-ack) it takes the place of emittedAt as
// the last key.
func changeDigestSeq(c client.Change, id entityIdentity, emittedAt, seq string) (string, error) {
	ident := changeIdentity{
		Change:   string(c.Change),
		Source:   c.Source,
		EntityID: id.ID,
		HeadSHA:  compactRaw(id.HeadSHA),
		Version:  compactRaw(id.Version),
	}
	if c.Change == "added" && ident.HeadSHA == nil && ident.Version == nil {
		if seq != "" {
			ident.Seq = seq
		} else {
			ident.EmittedAt = emittedAt
		}
	}
	data, err := json.Marshal(ident)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:retryIDHexLen], nil
}

// changeDigest is changeDigestSeq without the seq key (a call without
// --deferred-ack).
func changeDigest(c client.Change, id entityIdentity, emittedAt string) (string, error) {
	return changeDigestSeq(c, id, emittedAt, "")
}

func newChangesCmd() *cobra.Command {
	var consumer, beadsDir, backend string
	var retryWindow time.Duration
	var deferredAck bool
	cmd := &cobra.Command{
		Use:   "changes <type> [query]",
		Short: "Report entities changed since a consumer's last call, as pg-router rawItems (query optional: an unscoped consumer)",
		Args:  cobra.RangeArgs(1, 2),
	}
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose cursor pg-connector's own ledger reads and advances (required)")
	cmd.Flags().StringVar(&beadsDir, "beads-dir", "", "sets PG_CONNECTOR_ISSUE_BEADS_DIR in the pg-connector child's environment")
	cmd.Flags().StringVar(&backend, "backend", "", "pin the fan-out to exactly this registered backend instance (passed through as pg-connector's own --backend); omitted means every registered backend of the type")
	cmd.Flags().DurationVar(&retryWindow, "retry-window", 0,
		"when > 0, give each item a per-change id <entity id>@<12 hex> plus at (emit time) and expiresAt (at + window), so pg-router retries a failed dispatch inside the window; 0 (default) keeps bare entity ids")
	cmd.Flags().BoolVar(&deferredAck, "deferred-ack", false,
		"opt in to deferred acknowledgement: pass --no-ack to pg-connector, print an ack {lane, token} on every item and use per-change ids; the cursor advances only through the settle subcommand (or a poll with no change). Requires --retry-window and a backend that owns its changes")
	_ = cmd.MarkFlagRequired("consumer")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		entityType := args[0]
		// The query is optional: an unscoped consumer names none. An empty
		// second argument is the same as an absent one, so --query is never
		// passed with an empty value.
		var query string
		if len(args) == 2 {
			query = args[1]
		}
		if retryWindow < 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-source-pg-connector changes: --retry-window must not be negative")
			return errUsage
		}
		if deferredAck && retryWindow <= 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-source-pg-connector changes: --deferred-ack requires --retry-window > 0 (per-change item ids)")
			return errUsage
		}
		changesArgs := []string{entityType, "changes"}
		if query != "" {
			changesArgs = append(changesArgs, "--query", query)
		}
		changesArgs = append(changesArgs, "--consumer", consumer)
		if backend != "" {
			changesArgs = append(changesArgs, "--backend", backend)
		}
		if deferredAck {
			changesArgs = append(changesArgs, "--no-ack")
		}
		changesArgs = append(changesArgs, "--output", "json")
		out, err := invokeOrFail(cmd.Context(), cmd.ErrOrStderr(), changesArgs, beadsDirEnv(beadsDir))
		if errors.Is(err, errBudgetSkipped) {
			return writeItems(cmd.OutOrStdout(), []apievent.Item{})
		}
		if err != nil {
			return err
		}

		var wire client.ChangesOutcome
		if err := json.Unmarshal(out, &wire); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			return errFailed
		}

		// The same degraded-backend list applies to every item printed
		// by this one invocation — it describes the OVERALL call's
		// degraded backends, never a per-entity fact [design: section
		// 6.1]. degradedReasons (bead pg2-wa5uk) carries each degraded
		// backend's own real cause alongside it, keyed by backend name
		// rather than positionally paired with degraded, so a missing
		// reason on one backend can never desync the two; only
		// populated (and only then added to each item's own metadata
		// below) when at least one degraded backend actually reported
		// one.
		degraded := make([]string, 0)
		degradedReasons := make(map[string]string)
		for _, s := range wire.Sources {
			if s.Status == client.SourceDegraded {
				degraded = append(degraded, s.Backend)
				if s.Reason != "" {
					degradedReasons[s.Backend] = s.Reason
				}
			}
		}

		// One emit instant for the whole invocation: every item's at is
		// the same adapter emit time, and expiresAt = at + window (bead
		// pg2-1ldvy). Truncated to whole seconds because the wire carries
		// RFC3339 seconds; expiresAt is computed from the truncated at so
		// expiresAt - at is exactly the window. expiresAt is the bound pg-router's
		// retry evaluates at attempt time (INV-EVT-4).
		now := changesNow().UTC()
		emitAt := now.Truncate(time.Second)
		// emitted_at (bead pg2-ch5dm7.3): the same instant at millisecond
		// precision, on every item, window or not. Metadata only: it never
		// reaches changeIdentity, so event ids and router dedup are unchanged.
		emittedAt := now.Format(emittedAtLayout)

		var lanes *laneSet
		if deferredAck {
			var err error
			lanes, err = newLaneSet(cmd.ErrOrStderr(), wire.Sources)
			if err != nil {
				return err
			}
			if len(wire.Changes) == 0 {
				// A quiet poll: nothing is unsettled behind the cursor, so
				// the adapter acknowledges each succeeded source itself
				// (INTF-WIRE-ACK-2). A failed acknowledgement exits
				// non-zero without printing, and the next poll tries again.
				if err := lanes.ackEmptyPoll(cmd.Context(), cmd.ErrOrStderr(), entityType, query, consumer); err != nil {
					return err
				}
			}
		}

		items := make([]apievent.Item, 0, len(wire.Changes))
		lastOfLane := make(map[string]int)
		for _, c := range wire.Changes {
			var id entityIdentity
			if err := json.Unmarshal(c.Entity, &id); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errFailed
			}
			title := id.Title
			if title == "" {
				title = id.ID
			}
			metadata := map[string]any{
				"change":           c.Change,
				"source":           c.Source,
				"degraded_sources": degraded,
				// The bare entity id, always (bead pg2-1ldvy): with
				// --retry-window the item id carries an @<digest>
				// suffix, and a handler that needs the real entity id
				// (a desk argv) reads it from here.
				"entity_id":  id.ID,
				"emitted_at": emittedAt,
			}
			if len(degradedReasons) > 0 {
				metadata["degraded_reasons"] = degradedReasons
			}
			// The extended wire of a backend that owns its changes (seq,
			// kinds, fields), additive under the same keys and only when
			// the row carries them; never part of the digest below.
			extras, err := decodeChangeExtras(c)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errFailed
			}
			for k, v := range extras {
				metadata[k] = v
			}
			item := apievent.Item{
				ID:       id.ID,
				Type:     entityType,
				Title:    title,
				Metadata: metadata,
			}
			if retryWindow > 0 {
				seqKey := ""
				if deferredAck {
					seqKey = changeSeqKey(c)
				}
				digest, err := changeDigestSeq(c, id, emittedAt, seqKey)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
					return errFailed
				}
				// The suffixed id is what the core de-duplicates on (INV-EVT-3).
				item.ID = id.ID + "@" + digest
				item.At = emitAt.Format(time.RFC3339)
				item.ExpiresAt = emitAt.Add(retryWindow).Format(time.RFC3339)
			}
			if deferredAck {
				if c.Seq == nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "pg-router-source-pg-connector changes: change %q of backend %q carries no seq, which --deferred-ack needs\n", c.Change, c.Source)
					return errFailed
				}
				if _, ok := lanes.nextSeq[c.Source]; !ok {
					fmt.Fprintf(cmd.ErrOrStderr(), "pg-router-source-pg-connector changes: change of backend %q has no succeeded source row with a next_seq\n", c.Source)
					return errFailed
				}
				item.Ack = &apievent.Ack{Lane: c.Source, Token: changeSeqKey(c)}
				lastOfLane[c.Source] = len(items)
			}
			items = append(items, item)
		}
		// The last item of each lane carries the source row's scanned
		// next_seq: the position the umbrella acknowledges.
		for lane, idx := range lastOfLane {
			items[idx].Ack.Token = lanes.nextSeq[lane]
		}
		return writeItems(cmd.OutOrStdout(), items)
	}
	return cmd
}
