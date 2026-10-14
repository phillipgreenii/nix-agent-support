// Package answer turns the group engine's per-group results into the spec's
// read-answer annotations ("Entity read"): served_from, stale, age_seconds and
// the optional groups[] of every daemon read answer (INV-GH-FRESH-8).
//
// From computes the values once as a Provenance; the Apply* helpers write them
// onto the wire result types (pkg/schema, pkg/provider/pr); Record leaves the
// same values on the router's annotation carrier so the event log and the
// telemetry read them.
package answer

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The two served_from values of INV-CACHE-5.
const (
	ServedFromOrigin = "origin"
	ServedFromCache  = "cache"
)

// Provenance is where one read answer came from.
type Provenance struct {
	// ServedFrom is "origin" when this very call fetched at least one group from
	// origin, "cache" otherwise.
	ServedFrom string
	// Stale is true when any group served is past its fresh_until (a stale answer
	// is cache with stale: true).
	Stale bool
	// AgeSeconds is now minus the OLDEST fetched_at among the groups served, in
	// whole seconds truncated toward zero, never negative.
	AgeSeconds int64
	// AsOf is the RFC 3339 UTC fetched_at of the OLDEST group served; empty when
	// no group was served.
	AsOf string
	// Groups has one entry per group served, in the order given.
	Groups []schema.GroupInfo
}

// From computes the Provenance of an answer built from results at now. Only
// groups that were actually served count: an Unavailable group (no usable row)
// contributes to nothing, since the op answers with an error rather than with
// content from it. With no group served the answer is cache, not stale, age 0
// and has no as-of.
func From(results []groupsvc.GroupResult, now time.Time) Provenance {
	p := Provenance{ServedFrom: ServedFromCache}
	var oldest time.Time
	for _, r := range results {
		if r.Status == groupsvc.Unavailable {
			continue
		}
		if r.FromOrigin {
			p.ServedFrom = ServedFromOrigin
		}
		if r.Status == groupsvc.Stale {
			p.Stale = true
		}
		row := r.Row
		if oldest.IsZero() || row.FetchedAt.Before(oldest) {
			oldest = row.FetchedAt
		}
		p.Groups = append(p.Groups, info(r.Group, row.FetchedAt, row.FreshUntil, row.MaxAgeAt, row.LastError))
	}
	if len(p.Groups) == 0 {
		return p
	}
	p.AsOf = oldest.UTC().Format(time.RFC3339)
	if age := int64(now.Sub(oldest) / time.Second); age > 0 {
		p.AgeSeconds = age
	}
	return p
}

// info builds one groups[] entry; max_age_at is omitted exactly when the row's
// MaxAgeAt is the zero time, last_error when it is empty.
func info(group string, fetchedAt, freshUntil, maxAgeAt time.Time, lastError string) schema.GroupInfo {
	g := schema.GroupInfo{
		Group:      group,
		FetchedAt:  fetchedAt.UTC().Format(time.RFC3339),
		FreshUntil: freshUntil.UTC().Format(time.RFC3339),
		LastError:  lastError,
	}
	if !maxAgeAt.IsZero() {
		g.MaxAgeAt = maxAgeAt.UTC().Format(time.RFC3339)
	}
	return g
}

// Record leaves p's served_from, stale and age_seconds, and wait (zero keeps the
// router's own measurement), on ctx's router annotation carrier.
func Record(ctx context.Context, p Provenance, wait time.Duration) {
	router.Annotate(ctx, p.ServedFrom, p.Stale, p.AgeSeconds, wait)
}
