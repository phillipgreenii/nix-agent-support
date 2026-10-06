// Package freshness implements pg-desk's per-source data-age contract
// (docs/behavior/pg-desk/freshness.md, INV-FRESH-1 to INV-FRESH-5).
//
// The freshness of a source is the time of its last SUCCESSFUL origin fetch,
// as the connector's ledger records it (`pg-connector ledger show`'s
// refreshed_at). The heartbeat copies those times into pg-desk's meta table
// under source_fetch.* keys (Update); every reader (the `freshness` verb, the
// dashboard payload's sources[], the pg_desk_source_age_seconds gauge) derives
// ages from those keys alone (Sources, Build), so a read never touches the
// network or the connector.
//
// A source is a (backend, query) pair known to the ledger; the user-visible
// unit is the backend, whose age is the OLDEST age among its queries (the
// conservative reading). A backend with a query that has no recorded success
// is reported with a null age and stale: true (INV-FRESH-4, fail closed).
package freshness

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	// MetaKeyPrefix prefixes every per-(backend, query) meta key:
	// source_fetch.<backend>.<query>, with "@<instance>" appended to the query
	// part for a ledger that carries an instance discriminator.
	MetaKeyPrefix = "source_fetch."
	// BackendPrefix is stripped from a backend name to form its default
	// display label.
	BackendPrefix = "pg-connector-"
	// SchemaVersion is the freshness report's schemaVersion; the report
	// evolves additively only.
	SchemaVersion = 1
)

// LedgerError mirrors the connector ledger's last_error: when a fetch
// failed (or came back partial) and a low-cardinality code for why.
type LedgerError struct {
	At   time.Time `json:"at"`
	Code string    `json:"code"`
}

// LedgerRow is the subset of one `pg-connector ledger show` row the
// freshness contract reads. RefreshedAt and LastError are null when the
// connector has recorded none.
type LedgerRow struct {
	Type        string       `json:"type"`
	Backend     string       `json:"backend"`
	Query       string       `json:"query"`
	Instance    string       `json:"instance,omitempty"`
	RefreshedAt *time.Time   `json:"refreshed_at"`
	LastError   *LedgerError `json:"last_error"`
}

// ParseLedgerShow decodes `pg-connector ledger show`'s JSON output (an array
// of rows).
func ParseLedgerShow(b []byte) ([]LedgerRow, error) {
	var rows []LedgerRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("freshness: decode ledger show: %w", err)
	}
	return rows, nil
}

// Record is the value stored under one source_fetch.* meta key.
type Record struct {
	Backend       string       `json:"backend"`
	Query         string       `json:"query"`
	Instance      string       `json:"instance,omitempty"`
	LastSuccessAt *time.Time   `json:"last_success_at"`
	LastError     *LedgerError `json:"last_error"`
}

func (r Record) key() string {
	q := r.Query
	if r.Instance != "" {
		q += "@" + r.Instance
	}
	return MetaKeyPrefix + r.Backend + "." + q
}

// equal reports whether two records carry the same facts.
func (r Record) equal(o Record) bool {
	return r.Backend == o.Backend && r.Query == o.Query && r.Instance == o.Instance &&
		sameTime(r.LastSuccessAt, o.LastSuccessAt) && sameError(r.LastError, o.LastError)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func sameError(a, b *LedgerError) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.At.Equal(b.At) && a.Code == b.Code
}

// Merge folds an incoming record into a stored one. It is monotonic: a
// success time never moves backwards, and the last error is the later of the
// two by time. It is idempotent: Merge(x, x) == x.
func Merge(stored, incoming Record) Record {
	out := incoming
	out.LastSuccessAt = laterTime(stored.LastSuccessAt, incoming.LastSuccessAt)
	out.LastError = laterError(stored.LastError, incoming.LastError)
	return out
}

func laterTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	}
	return a
}

func laterError(a, b *LedgerError) *LedgerError {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.At.After(a.At):
		return b
	}
	return a
}

// collapse folds ledger rows that map to the same meta key (the same
// backend and query under two entity types) into one Record, keeping the
// OLDEST success (a missing success wins) and the latest error, so a
// duplicate can never make a source look fresher than its stalest ledger.
func collapse(rows []LedgerRow) []Record {
	byKey := map[string]Record{}
	var order []string
	for _, row := range rows {
		if strings.TrimSpace(row.Backend) == "" || strings.TrimSpace(row.Query) == "" {
			continue
		}
		rec := Record{
			Backend: row.Backend, Query: row.Query, Instance: row.Instance,
			LastSuccessAt: utc(row.RefreshedAt), LastError: utcError(row.LastError),
		}
		k := rec.key()
		prev, seen := byKey[k]
		if !seen {
			byKey[k] = rec
			order = append(order, k)
			continue
		}
		if prev.LastSuccessAt == nil || rec.LastSuccessAt == nil {
			prev.LastSuccessAt = nil
		} else if rec.LastSuccessAt.Before(*prev.LastSuccessAt) {
			prev.LastSuccessAt = rec.LastSuccessAt
		}
		prev.LastError = laterError(prev.LastError, rec.LastError)
		byKey[k] = prev
	}
	sort.Strings(order)
	out := make([]Record, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func utcError(e *LedgerError) *LedgerError {
	if e == nil {
		return nil
	}
	return &LedgerError{At: e.At.UTC(), Code: e.Code}
}

// Update records the ledger rows' freshness into the store's meta table. It
// is monotonic and idempotent (see Merge): re-running it with the same rows
// writes nothing, and a stale or reordered read can never move a success
// time backwards. A row for a source the store has never seen is recorded
// even when it has no success yet, so that source reports as unknown and
// stale (INV-FRESH-4) instead of being absent.
func Update(st *store.Store, rows []LedgerRow) error {
	for _, rec := range collapse(rows) {
		key := rec.key()
		raw, found, err := st.GetMeta(key)
		if err != nil {
			return fmt.Errorf("freshness: read %s: %w", key, err)
		}
		next := rec
		if found {
			var stored Record
			if json.Unmarshal([]byte(raw), &stored) == nil {
				next = Merge(stored, rec)
				if next.equal(stored) {
					continue
				}
			}
		}
		b, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("freshness: encode %s: %w", key, err)
		}
		if err := st.SetMeta(key, string(b)); err != nil {
			return fmt.Errorf("freshness: write %s: %w", key, err)
		}
	}
	return nil
}

// Row is one source in the freshness report and in the dashboard payload's
// sources[] (the same shape on both surfaces). LastSuccessAt and AgeSeconds
// are null when the source has no recorded success; such a source is always
// stale (INV-FRESH-4).
type Row struct {
	Source        string  `json:"source"`
	Label         string  `json:"label"`
	LastSuccessAt *string `json:"last_success_at"`
	AgeSeconds    *int    `json:"age_seconds"`
	Stale         bool    `json:"stale"`
}

// Report is `pg-desk freshness --json`'s document.
type Report struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Now               string `json:"now"`
	StaleAfterSeconds int    `json:"stale_after_seconds"`
	AnyStale          bool   `json:"any_stale"`
	Sources           []Row  `json:"sources"`
}

// ShortName is a backend name without its "pg-connector-" prefix: the
// default display label.
func ShortName(backend string) string {
	return strings.TrimPrefix(backend, BackendPrefix)
}

// resolve returns a source's label and stale threshold, honoring a
// freshness.sources override keyed by the full or the short backend name. A
// nil cfg yields the defaults.
func resolve(cfg *config.Config, backend string) (label string, staleAfter time.Duration) {
	label, staleAfter = ShortName(backend), cfg.SourceStaleAfter()
	if cfg == nil {
		return label, staleAfter
	}
	ov, ok := cfg.Freshness.Sources[backend]
	if !ok {
		ov, ok = cfg.Freshness.Sources[ShortName(backend)]
	}
	if !ok {
		return label, staleAfter
	}
	if strings.TrimSpace(ov.Label) != "" {
		label = ov.Label
	}
	if d, err := config.ParseDuration(ov.StaleAfter); err == nil && d > 0 {
		staleAfter = d
	}
	return label, staleAfter
}

// Sources reads the recorded source_fetch.* keys and returns one Row per
// backend, sorted by source, as of now. It reads the store only. The result
// is never nil, so it serializes as [] rather than null.
func Sources(st *store.Store, cfg *config.Config, now time.Time) ([]Row, error) {
	kv, err := st.ListMetaPrefix(MetaKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("freshness: %w", err)
	}
	// oldest success per backend; unknown wins.
	type agg struct {
		unknown bool
		oldest  time.Time
	}
	byBackend := map[string]*agg{}
	for _, raw := range kv {
		var rec Record
		if json.Unmarshal([]byte(raw), &rec) != nil || rec.Backend == "" {
			continue // a malformed row MUST NOT take the indicator down
		}
		a := byBackend[rec.Backend]
		if a == nil {
			a = &agg{}
			byBackend[rec.Backend] = a
		}
		if rec.LastSuccessAt == nil || rec.LastSuccessAt.IsZero() {
			a.unknown = true
			continue
		}
		if a.oldest.IsZero() || rec.LastSuccessAt.Before(a.oldest) {
			a.oldest = rec.LastSuccessAt.UTC()
		}
	}
	backends := make([]string, 0, len(byBackend))
	for b := range byBackend {
		backends = append(backends, b)
	}
	sort.Strings(backends)
	rows := make([]Row, 0, len(backends))
	for _, b := range backends {
		a := byBackend[b]
		label, staleAfter := resolve(cfg, b)
		row := Row{Source: b, Label: label, Stale: true}
		if !a.unknown && !a.oldest.IsZero() {
			ts := a.oldest.Format(time.RFC3339)
			age := ageSeconds(a.oldest, now)
			row.LastSuccessAt, row.AgeSeconds = &ts, &age
			row.Stale = now.Sub(a.oldest) > staleAfter
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ageSeconds is the whole seconds since t; a future t (clock skew) is 0.
func ageSeconds(t, now time.Time) int {
	d := now.Sub(t)
	if d < 0 {
		return 0
	}
	return int(d.Seconds())
}

// Build assembles the freshness report as of now.
func Build(st *store.Store, cfg *config.Config, now time.Time) (*Report, error) {
	rows, err := Sources(st, cfg, now)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		SchemaVersion:     SchemaVersion,
		Now:               now.UTC().Format(time.RFC3339),
		StaleAfterSeconds: int(cfg.SourceStaleAfter().Seconds()),
		Sources:           rows,
	}
	for _, r := range rows {
		if r.Stale {
			rep.AnyStale = true
		}
	}
	return rep, nil
}
