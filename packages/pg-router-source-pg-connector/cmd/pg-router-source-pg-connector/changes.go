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
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// changesNow is the adapter's emit-time clock, a seam so tests can pin the
// "at" stamp --retry-window puts on each item (bead pg2-1ldvy).
var changesNow = time.Now

// retryIDHexLen is how many hex characters of the change-row digest the
// retry-window id suffix carries (bead pg2-1ldvy: "12 hex of sha256").
const retryIDHexLen = 12

// changesWire mirrors pg-connector's own already-landed "changes" JSON
// envelope (cmd/pg-connector/changes.go's changesWire) — decoded
// generically since this adapter has no compile-time dependency on
// packages/pg-connector [design: section 6.1].
type changesWire struct {
	Sources []changesSource `json:"sources"`
	Changes []changesEntry  `json:"changes"`
}

// changesSource is one row of the "changes" wire response's sources[]
// array. Backend/Status/Reason are needed here (building
// metadata.degraded_sources/degraded_reasons below); Version/Truncated
// are ignorable extra JSON for this call path.
//
// Reason (bead pg2-wa5uk) was previously left undecoded, so a degraded
// backend's real cause (e.g. a rate-limit guard tripping) never reached
// this adapter's own printed items — only the degraded backend's name
// did, via degraded_sources.
type changesSource struct {
	Backend string `json:"backend"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}

// changesEntry is one row of the "changes" wire response's changes[]
// array.
type changesEntry struct {
	Change string          `json:"change"`
	Source string          `json:"source"`
	Entity json.RawMessage `json:"entity"`
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

// changeDigest returns the first retryIDHexLen hex characters of the SHA-256
// over the canonical JSON of c's changeIdentity.
func changeDigest(c changesEntry, id entityIdentity) (string, error) {
	data, err := json.Marshal(changeIdentity{
		Change:   c.Change,
		Source:   c.Source,
		EntityID: id.ID,
		HeadSHA:  compactRaw(id.HeadSHA),
		Version:  compactRaw(id.Version),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:retryIDHexLen], nil
}

func newChangesCmd() *cobra.Command {
	var consumer, beadsDir string
	var retryWindow time.Duration
	cmd := &cobra.Command{
		Use:   "changes <type> <query>",
		Short: "Report entities changed since a consumer's last call, as pg-router rawItems",
		Args:  cobra.ExactArgs(2),
	}
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose cursor pg-connector's own ledger reads and advances (required)")
	cmd.Flags().StringVar(&beadsDir, "beads-dir", "", "sets PG_CONNECTOR_ISSUE_BEADS_DIR in the pg-connector child's environment")
	cmd.Flags().DurationVar(&retryWindow, "retry-window", 0,
		"when > 0, give each item a per-change id <entity id>@<12 hex> plus at (emit time) and expiresAt (at + window), so pg-router retries a failed dispatch inside the window; 0 (default) keeps bare entity ids")
	_ = cmd.MarkFlagRequired("consumer")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		entityType, query := args[0], args[1]
		if retryWindow < 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-source-pg-connector changes: --retry-window must not be negative")
			return errFailed
		}
		out, err := invokeOrFail(cmd.Context(), cmd.ErrOrStderr(),
			[]string{entityType, "changes", "--query", query, "--consumer", consumer, "--output", "json"},
			beadsDirEnv(beadsDir))
		if err != nil {
			return err
		}

		var wire changesWire
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
			if s.Status == "degraded" {
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
		// expiresAt - at is exactly the window.
		emitAt := changesNow().UTC().Truncate(time.Second)

		items := make([]rawItem, 0, len(wire.Changes))
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
				"entity_id": id.ID,
			}
			if len(degradedReasons) > 0 {
				metadata["degraded_reasons"] = degradedReasons
			}
			item := rawItem{
				ID:       id.ID,
				Type:     entityType,
				Title:    title,
				Metadata: metadata,
			}
			if retryWindow > 0 {
				digest, err := changeDigest(c, id)
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
					return errFailed
				}
				item.ID = id.ID + "@" + digest
				item.At = emitAt.Format(time.RFC3339)
				item.ExpiresAt = emitAt.Add(retryWindow).Format(time.RFC3339)
			}
			items = append(items, item)
		}
		return writeItems(cmd.OutOrStdout(), items)
	}
	return cmd
}
