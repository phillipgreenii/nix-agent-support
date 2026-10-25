package read

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The two served_from values.
const (
	servedFromOrigin = "origin"
	servedFromCache  = "cache"
)

// servedGroup is one group an answer was made of.
type servedGroup struct {
	group string
	row   store.GroupRow
	// fromOrigin is true when this very call fetched the group.
	fromOrigin bool
	// stale is true when the group is served although it could not be
	// refreshed.
	stale bool
}

// provenance is where one read answer came from.
type provenance struct {
	servedFrom string
	stale      bool
	// ageSeconds is now minus the OLDEST fetched_at among the groups served,
	// truncated to whole seconds and never negative.
	ageSeconds int64
	// asOf is the RFC 3339 UTC fetched_at of the oldest group served; empty
	// when none was.
	asOf   string
	groups []schema.GroupInfo
}

// provenanceOf computes the annotations of an answer built from served at now.
// A group with no fetch time was not served from a stored row and counts for
// nothing.
func provenanceOf(served []servedGroup, now time.Time) provenance {
	p := provenance{servedFrom: servedFromCache}
	var oldest time.Time
	for _, g := range served {
		if g.row.FetchedAt.IsZero() {
			continue
		}
		if g.fromOrigin {
			p.servedFrom = servedFromOrigin
		}
		if g.stale {
			p.stale = true
		}
		if oldest.IsZero() || g.row.FetchedAt.Before(oldest) {
			oldest = g.row.FetchedAt
		}
		p.groups = append(p.groups, groupInfo(g.group, g.row))
	}
	p.finish(oldest, now)
	return p
}

// finish sets the as-of and the age from the oldest fetch time.
func (p *provenance) finish(oldest, now time.Time) {
	if oldest.IsZero() {
		return
	}
	p.asOf = oldest.UTC().Format(time.RFC3339)
	if age := int64(now.Sub(oldest) / time.Second); age > 0 {
		p.ageSeconds = age
	}
}

// groupInfo is one groups[] entry; max_age_at is omitted exactly when the row's
// MaxAgeAt is the zero time, last_error when it is empty.
func groupInfo(group string, row store.GroupRow) schema.GroupInfo {
	g := schema.GroupInfo{
		Group:      group,
		FetchedAt:  row.FetchedAt.UTC().Format(time.RFC3339),
		FreshUntil: row.FreshUntil.UTC().Format(time.RFC3339),
		LastError:  row.LastError,
	}
	if !row.MaxAgeAt.IsZero() {
		g.MaxAgeAt = row.MaxAgeAt.UTC().Format(time.RFC3339)
	}
	return g
}

// record leaves p's served_from, stale and age_seconds on ctx's router
// annotation carrier, which the event log and the telemetry read.
func (p provenance) record(ctx context.Context) {
	router.Annotate(ctx, p.servedFrom, p.stale, p.ageSeconds, 0)
}

// applyIssue sets the annotations on a show answer.
func (p provenance) applyIssue(r *schema.Issue) {
	age := p.ageSeconds
	r.ServedFrom = p.servedFrom
	r.AgeSeconds = &age
	r.Stale = p.stale
	r.Groups = append([]schema.GroupInfo(nil), p.groups...)
	if p.asOf != "" {
		r.AsOf = p.asOf
	}
}

// applyList sets the annotations on a list answer.
func (p provenance) applyList(r *schema.IssueListResult) {
	age, stale := p.ageSeconds, p.stale
	r.ServedFrom = p.servedFrom
	r.AgeSeconds = &age
	r.Stale = &stale
	r.Groups = append([]schema.GroupInfo(nil), p.groups...)
}
