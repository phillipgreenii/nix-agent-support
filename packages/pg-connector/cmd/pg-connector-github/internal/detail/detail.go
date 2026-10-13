// Package detail fetches the detail field group of a pull request: its
// mergeStateStatus, the requested reviewers (logins and team slugs), additions,
// deletions, changedFiles and merged. The group is read through GraphQL
// nodes(ids:) in small batches (mergeStateStatus in large batches answered HTTP
// 502 and 504 in the Phase 1 spike), with a per-PR fallback for a batch that
// does (fetch.go).
//
// This package is the template the other group fetchers follow: a typed content
// struct with Decode for the read ops, a fingerprint that ignores incidental
// ordering, and a groups.Fetcher whose every origin call is admitted by the
// governor and booked through the rate feed.
package detail

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
)

// Detail is the content of the detail group. Its JSON is what the group stores.
type Detail struct {
	// Merged is true when the PR has been merged (GitHub's merged flag, which is
	// set exactly when the PR has a mergedAt).
	Merged bool `json:"merged"`
	// MergeStateStatus is GitHub's merge state (CLEAN, BLOCKED, BEHIND, DIRTY,
	// DRAFT, HAS_HOOKS, UNKNOWN, UNSTABLE) as it spells it.
	MergeStateStatus string `json:"mergeStateStatus"`
	// ReviewRequests are the logins of the requested users, bots and mannequins
	// and the slugs of the requested teams, as schema.PR.ReviewRequests carries
	// them. GitHub's order is kept; never nil in a stored row.
	ReviewRequests []string `json:"reviewRequests"`
	Additions      int      `json:"additions"`
	Deletions      int      `json:"deletions"`
	ChangedFiles   int      `json:"changedFiles"`
}

// Decode reads the content of a stored detail row.
func Decode(content []byte) (Detail, error) {
	if len(content) == 0 {
		return Detail{}, errors.New("detail: no content")
	}
	var d Detail
	if err := json.Unmarshal(content, &d); err != nil {
		return Detail{}, fmt.Errorf("detail: decode content: %w", err)
	}
	return d, nil
}

// canonical returns the content bytes of d: its JSON with a non-nil
// ReviewRequests.
func (d Detail) canonical() ([]byte, error) {
	if d.ReviewRequests == nil {
		d.ReviewRequests = []string{}
	}
	return json.Marshal(d)
}

// Fingerprint is the group's fingerprint: the hash of the canonical JSON with
// ReviewRequests sorted, so a reordered answer is not a change.
func Fingerprint(d Detail) string {
	d.ReviewRequests = slices.Clone(d.ReviewRequests)
	slices.Sort(d.ReviewRequests)
	b, _ := d.canonical() // a struct of scalars and strings always marshals
	return groups.Fingerprint(b)
}
