package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// annotationConsumerPrefix is the reserved prefix of every consumer that
// `pg-desk <type> annotation-items` reads. The consumer table is keyed
// (name, type) with no kind column, so the two feeds of a type share a cursor
// unless their names can never collide: this verb requires the prefix and
// `<type> changes` refuses it.
const annotationConsumerPrefix = "annotations:"

// annotationItemsNow is the clock for seen_at, pruning and expiresAt; tests
// replace it.
var annotationItemsNow = func() time.Time { return time.Now().UTC() }

// The local link-derived change_log kinds (the values of classify.KindWorkChanged
// and classify.KindLinkChanged, spelled out so this verb does not import the
// classifier).
const (
	changeKindWorkChanged = "work_changed"
	changeKindLinkChanged = "link_changed"
)

// annotationItemHexLen is the length of the id digest suffix; it is the width
// pg-pr-change's delivery key reads (`@<12 hex>`).
const annotationItemHexLen = 12

// annotationItem is the pg-router item shape (the one heartbeatItem mirrors),
// with the expiresAt member the router's retry window reads.
type annotationItem struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Metadata  map[string]any `json:"metadata"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
}

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newAnnotationItemsCmd(t))
	}
}

// newAnnotationItemsCmd builds `pg-desk <type> annotation-items --consumer
// annotations:NAME [--retry-window DUR] [--limit N]`: the pg-router query that
// turns annotation_changed change_log records into router items, one per
// record, so a change of an entity's annotations can wake the entity's
// decider. It reads the local store only: no pg-connector call and no
// hydration.
func newAnnotationItemsCmd(entityType string) *cobra.Command {
	var consumer string
	var retryWindow time.Duration
	var limit int
	c := &cobra.Command{
		Use:   "annotation-items",
		Short: fmt.Sprintf("Print the %s annotation changes past a consumer's cursor as pg-router items", entityType),
		Long: fmt.Sprintf(`Print a JSON array of pg-router items, one per annotation_changed record of %[1]s
entities past the consumer's cursor, in ascending change_log seq, and advance
the cursor. Items have type "%[1]s.changed", id <entity id>@<12 hex> (a digest
of type, entity id, seq and origin: stable per record, distinct across
records), metadata.entity_id, metadata.origin and metadata.kinds
(["annotation_changed"]). There is no metadata.seq: a pg-desk seq would share
the delivery-key space with the GitHub daemon's change sequence. With
--retry-window the items carry expiresAt = now + window so the router retries a
failed dispatch inside it. An empty poll prints [].

The consumer name MUST start with %[2]q: the consumer table has no kind column,
so the prefix keeps this feed's cursor apart from "%[1]s changes". A consumer
registers at the current maximum seq on its first call: records that already
exist are not delivered. --limit counts the change_log rows SCANNED, not the
items emitted, and the cursor advances to the last row scanned, so a page of
other records (hydrations) cannot stall the feed.`, entityType, annotationConsumerPrefix),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnnotationItems(cmd, entityType, consumer, retryWindow, limit)
		},
	}
	c.Flags().StringVar(&consumer, "consumer", "", "Consumer name, with the reserved prefix "+annotationConsumerPrefix+" (required)")
	c.Flags().DurationVar(&retryWindow, "retry-window", 0, "Give each item expiresAt = now + window (0 = none)")
	c.Flags().IntVar(&limit, "limit", 0, "Scan at most N change_log rows (0 = all)")
	_ = c.MarkFlagRequired("consumer")
	return c
}

func runAnnotationItems(cmd *cobra.Command, entityType, consumer string, retryWindow time.Duration, limit int) error {
	if !strings.HasPrefix(consumer, annotationConsumerPrefix) || strings.TrimSpace(strings.TrimPrefix(consumer, annotationConsumerPrefix)) == "" {
		return fmt.Errorf("annotation-items: --consumer %q must be %sNAME", consumer, annotationConsumerPrefix)
	}
	if limit < 0 {
		return fmt.Errorf("annotation-items: --limit must be >= 0, got %d", limit)
	}
	if retryWindow < 0 {
		return fmt.Errorf("annotation-items: --retry-window must be >= 0, got %s", retryWindow)
	}
	st, err := openNewSchemaStore("annotation-items")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	// Read, flush and advance under the per-(type, consumer) lock, as
	// `changes` does, so two overlapping polls do not both deliver a record.
	unlock, err := st.LockConsumer(entityType, consumer)
	if err != nil {
		return fmt.Errorf("annotation-items: lock consumer %s/%s: %w", entityType, consumer, err)
	}
	defer unlock()

	now := annotationItemsNow()
	if err := st.RegisterConsumerAtMax(consumer, entityType, now); err != nil {
		return fmt.Errorf("annotation-items: %w", err)
	}
	rows, err := st.ReadChanges(consumer, entityType, limit, now)
	if err != nil {
		return fmt.Errorf("annotation-items: %w", err)
	}
	items := annotationItemsOf(entityType, rows, now, retryWindow, laneResidualCheck(st, entityType))
	b, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("annotation-items: marshal json: %w", err)
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(b)); err != nil {
		return fmt.Errorf("annotation-items: write output: %w", err)
	}
	if len(rows) > 0 {
		// The last row SCANNED, not the last item emitted.
		if err := st.AdvanceCursor(consumer, entityType, rows[len(rows)-1].Seq); err != nil {
			return fmt.Errorf("annotation-items: %w", err)
		}
	}
	// Prune like a real `changes` call: no other reader of a pr consumer does.
	// Not fatal, the output was delivered and the cursor advanced.
	if cfg, cerr := deskConfigLoad(cmd.Context()); cerr == nil {
		if _, perr := st.PruneChangeLog(now, cfg.ChangeLogRetention(), cfg.ConsumerStaleAfter()); perr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "annotation-items: prune change_log: %v\n", perr)
		}
	}
	return nil
}

// laneResidualCheck returns the predicate "this entity's lane row holds a
// non-zero residual". Only a pr has a lane row; for any other type the
// predicate is always false. A missing or unreadable row reads as no residual.
func laneResidualCheck(st *store.Store, entityType string) func(entityID string) bool {
	if entityType != "pr" {
		return func(string) bool { return false }
	}
	return func(entityID string) bool {
		raw, found, err := st.GetMeta(lanestate.KeyPRPrefix + entityID)
		if err != nil || !found {
			return false
		}
		var s lanestate.PRState
		return json.Unmarshal([]byte(raw), &s) == nil && s.Residual > 0
	}
}

// annotationItemsOf maps the scanned rows to router items: one per
// annotation_changed row, and one per work_changed or link_changed row of an
// entity for which hasResidual reports true (a PR whose lane row still holds a
// residual: the blocker behind it may just have been removed out of band, and
// nothing else re-runs the lane). Other kinds are dropped. The result is never
// nil, so an empty poll prints [].
func annotationItemsOf(entityType string, rows []store.ChangeRecord, now time.Time, retryWindow time.Duration, hasResidual func(entityID string) bool) []annotationItem {
	items := []annotationItem{}
	for _, r := range rows {
		kind := ""
		switch {
		case hasKind(r.Kinds, store.ChangeKindAnnotationChanged):
			kind = store.ChangeKindAnnotationChanged
		case hasKind(r.Kinds, changeKindWorkChanged) && hasResidual(r.EntityID):
			kind = changeKindWorkChanged
		case hasKind(r.Kinds, changeKindLinkChanged) && hasResidual(r.EntityID):
			kind = changeKindLinkChanged
		default:
			continue
		}
		it := annotationItem{
			ID:    r.EntityID + "@" + annotationItemDigest(entityType, r.EntityID, r.Seq, r.Origin),
			Type:  entityType + ".changed",
			Title: r.EntityID,
			Metadata: map[string]any{
				"entity_id": r.EntityID,
				"origin":    r.Origin,
				"kinds":     []string{kind},
			},
		}
		if retryWindow > 0 {
			it.ExpiresAt = now.Add(retryWindow).Format(time.RFC3339)
		}
		items = append(items, it)
	}
	return items
}

func hasKind(kinds []string, want string) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// annotationItemDigest is the first annotationItemHexLen hex characters of the
// SHA-256 over the canonical JSON of (type, entity id, seq, origin). The digest
// 000000000000 reads as "no key" to pg-pr-change's delivery key, so it is
// remapped to 000000000001.
func annotationItemDigest(entityType, entityID string, seq int64, origin string) string {
	data, _ := json.Marshal(struct {
		Type     string `json:"type"`
		EntityID string `json:"entity_id"`
		Seq      int64  `json:"seq"`
		Origin   string `json:"origin"`
	}{entityType, entityID, seq, origin})
	sum := sha256.Sum256(data)
	return remapZeroDigest(hex.EncodeToString(sum[:])[:annotationItemHexLen])
}

// remapZeroDigest keeps a digest from being the all-zero key.
func remapZeroDigest(d string) string {
	if d == strings.Repeat("0", annotationItemHexLen) {
		return strings.Repeat("0", annotationItemHexLen-1) + "1"
	}
	return d
}
