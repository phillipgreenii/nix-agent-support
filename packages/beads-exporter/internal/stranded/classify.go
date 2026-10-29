// Package stranded decides which claimed beads look abandoned, from the bead's
// own fields only: a claim is stranded when bd's updated_at is older than the
// configured age. The same rule applies to every assignee. Detection is
// report-only; nothing here releases or clears a claim.
//
// A bd comment does not move updated_at (pinned against a real bd by the
// contract suite's TestCommentDoesNotBumpUpdatedAt), so a claimant that only
// comments still looks idle. bd list carries no comment times, only a count, so
// reading them would cost one extra bd spawn per claim; comments do not count.
package stranded

import (
	"sort"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// Statuses are the stored statuses a claim can be in; a bead is a candidate
// only while its stored status is one of these and it has an assignee. The
// order is the label order of the exported metric.
func Statuses() []string { return []string{"open", "in_progress", "hooked"} }

// Claim is one stranded claim. Its fields are the bead's own.
type Claim struct {
	BeadID   string
	Assignee string
	Status   string
	// ClaimTime is started_at; zero when the bead has none.
	ClaimTime time.Time
	// LastUpdate is updated_at; zero when the bead has none.
	LastUpdate time.Time
}

// Result is the outcome of one classification: the stranded claims, ordered by
// bead id. It is the stranded pass's metrics.Emitter.
type Result struct {
	Claims []Claim
}

// Samples implements metrics.Emitter.
func (r Result) Samples(db string) []metrics.Sample {
	counts := map[string]int{}
	var oldest time.Time
	for _, c := range r.Claims {
		counts[c.Status]++
		if !c.LastUpdate.IsZero() && (oldest.IsZero() || c.LastUpdate.Before(oldest)) {
			oldest = c.LastUpdate
		}
	}
	var out []metrics.Sample
	for _, st := range Statuses() {
		out = append(out, metrics.Sample{
			Family: metrics.FamStrandedClaims,
			Labels: metrics.SeriesLabel("db", db, "status", st),
			Value:  float64(counts[st]),
		})
	}
	if !oldest.IsZero() {
		out = append(out, metrics.Sample{
			Family: metrics.FamOldestStranded,
			Labels: metrics.SeriesLabel("db", db),
			Value:  float64(oldest.Unix()),
		})
	}
	return out
}

func isCandidate(b bd.Bead) bool {
	if b.Assignee == "" {
		return false
	}
	for _, st := range Statuses() {
		if b.Status == st {
			return true
		}
	}
	return false
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Classify returns the stranded claims among beads at now. A claim (stored
// status open, in_progress or hooked, with an assignee) is stranded exactly
// when its updated_at is older than maxAge: a claim updated exactly maxAge ago
// is not yet stranded. A claim bd reports without updated_at is stranded too,
// because nothing shows it was touched recently.
func Classify(beads []bd.Bead, now time.Time, maxAge time.Duration) Result {
	cutoff := now.Add(-maxAge)
	var res Result
	for _, b := range beads {
		if !isCandidate(b) {
			continue
		}
		last := timeOf(b.UpdatedAt)
		if !last.IsZero() && !last.Before(cutoff) {
			continue
		}
		res.Claims = append(res.Claims, Claim{
			BeadID:     b.ID,
			Assignee:   b.Assignee,
			Status:     b.Status,
			ClaimTime:  timeOf(b.StartedAt),
			LastUpdate: last,
		})
	}
	sort.Slice(res.Claims, func(i, j int) bool { return res.Claims[i].BeadID < res.Claims[j].BeadID })
	return res
}
