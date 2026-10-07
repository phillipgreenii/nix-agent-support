// Package warmup primes and seeds the scratch store so the remote
// re-hydration tier is effectively off during phase A. It is a warm-up
// simplification, NOT the cutover treatment bead pg2-5rb3t weighs.
package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
)

// Listing is the decoded `pg-connector pr list --fingerprints` output of one
// watched query.
type Listing struct {
	Query        string
	Entities     []Listed
	Fingerprints map[string]string
}

// Listed is one listed entity.
type Listed struct {
	ID    string
	Stale bool
}

// ParseListing decodes a list outcome (exit 0 or 2 are both usable; the
// caller rejects other exits).
func ParseListing(query string, stdout []byte) (Listing, error) {
	var wire struct {
		Entities []struct {
			ID    string `json:"id"`
			Stale bool   `json:"stale"`
		} `json:"entities"`
		Fingerprints map[string]string `json:"fingerprints"`
	}
	if err := json.Unmarshal(stdout, &wire); err != nil {
		return Listing{}, fmt.Errorf("warmup: decode listing of %q: %w", query, err)
	}
	l := Listing{Query: query, Fingerprints: wire.Fingerprints}
	if l.Fingerprints == nil {
		l.Fingerprints = map[string]string{}
	}
	for _, e := range wire.Entities {
		if e.ID == "" {
			return Listing{}, fmt.Errorf("warmup: listing of %q has an entity with no id", query)
		}
		l.Entities = append(l.Entities, Listed{ID: e.ID, Stale: e.Stale})
	}
	return l, nil
}

// Primed is the seeding input drawn from the listings.
type Primed struct {
	// FP maps each primed (listed, non-stale) id to its fingerprint; an id in
	// both queries takes the FIRST query's fingerprint.
	FP map[string]string
	// Listed is every id any listing returned, stale ones included.
	Listed map[string]bool
	// Stale are the ids skipped because every listing that returned them marked
	// them stale.
	Stale []string
}

// Prime merges the listings in config order.
func Prime(listings []Listing) Primed {
	p := Primed{FP: map[string]string{}, Listed: map[string]bool{}}
	staleOnly := map[string]bool{}
	for _, l := range listings {
		for _, e := range l.Entities {
			p.Listed[e.ID] = true
			if e.Stale {
				if _, ok := p.FP[e.ID]; !ok {
					staleOnly[e.ID] = true
				}
				continue
			}
			if _, ok := p.FP[e.ID]; ok {
				continue
			}
			delete(staleOnly, e.ID)
			p.FP[e.ID] = l.Fingerprints[e.ID]
		}
	}
	for id := range staleOnly {
		p.Stale = append(p.Stale, id)
	}
	sort.Strings(p.Stale)
	return p
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$`)

// SeedSQL renders the seeding script for the scratch store.
func SeedSQL(p Primed, now time.Time) (string, error) {
	if len(p.Listed) == 0 {
		return "", errors.New("warmup: no listed ids: refusing to seed (an empty listing would deactivate every row)")
	}
	var b strings.Builder
	b.WriteString("BEGIN;\n")
	fmt.Fprintf(&b, "UPDATE entity SET hydrated_at=%s WHERE entity_type='pr' AND active=1;\n", sqlite.Quote(now.UTC().Format("2006-01-02T15:04:05Z")))
	ids := make([]string, 0, len(p.FP))
	for id := range p.FP {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !idRE.MatchString(id) {
			return "", fmt.Errorf("warmup: refusing an id of unexpected shape (%d bytes)", len(id))
		}
		fp := p.FP[id]
		if fp == "" {
			continue
		}
		fmt.Fprintf(&b, "UPDATE entity SET list_fp=%s WHERE entity_type='pr' AND entity_id=%s;\n", sqlite.Quote(fp), sqlite.Quote(id))
	}
	all := make([]string, 0, len(p.Listed))
	for id := range p.Listed {
		if !idRE.MatchString(id) {
			return "", fmt.Errorf("warmup: refusing an id of unexpected shape (%d bytes)", len(id))
		}
		all = append(all, sqlite.Quote(id))
	}
	sort.Strings(all)
	fmt.Fprintf(&b, "UPDATE entity SET active=0 WHERE entity_type='pr' AND entity_id NOT IN (%s);\n", strings.Join(all, ","))
	b.WriteString("COMMIT;\n")
	return b.String(), nil
}

// NeverHydratedSQL is the assertion query; it must return 0 after seeding.
const NeverHydratedSQL = "SELECT count(*) FROM entity WHERE entity_type='pr' AND active=1 AND (hydrated_at IS NULL OR hydrated_at='')"

// Seed applies the script and checks the assertion. It returns the number of
// active rows left.
func Seed(ctx context.Context, db sqlite.DB, p Primed, now time.Time) (active int64, err error) {
	script, err := SeedSQL(p, now)
	if err != nil {
		return 0, err
	}
	if err := db.Exec(ctx, script); err != nil {
		return 0, fmt.Errorf("warmup: seed: %w", err)
	}
	n, err := db.Int(ctx, NeverHydratedSQL)
	if err != nil {
		return 0, err
	}
	if n != 0 {
		return 0, fmt.Errorf("warmup: %d active pr rows are still never hydrated after seeding", n)
	}
	return db.Int(ctx, "SELECT count(*) FROM entity WHERE entity_type='pr' AND active=1")
}
