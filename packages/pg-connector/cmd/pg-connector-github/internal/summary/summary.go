// Package summary is the `summary` field group of a pull request entity: the
// batched search field set (internal/github's BatchedPRFields) plus the head
// and base branch names and the base commit, fetched in batches through
// `nodes(ids:)` and stored as the entity's `summary` group.
//
//   - Summary is the typed value; Fingerprint is the hash the keep-alive step
//     compares.
//   - Fetcher.Fetch turns node ids into Summaries, at most 74 ids per call, and
//     books each call's spend with the governor.
//   - Persister.Persist stores one Summary, applying the mergeability rule
//     (entity.Carry) and recording the head and base it describes.
//
// What decides WHEN a summary is fetched (queues, batches, keep-alive, expiry)
// and what serves it to a caller belong to other packages.
package summary

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
)

// GroupName is the `group` of the summary rows in the store's entity_group
// table.
const GroupName = "summary"

// Summary is the summary group of one pull request. Its JSON form is the
// stored group content.
type Summary struct {
	// NodeID is the PR's GraphQL node id, the key of its entity row.
	NodeID string `json:"node_id"`
	// Gone is true when GitHub returned null for the node id (deleted, or not
	// visible to the token) or an object that is not a pull request; every other
	// field is then empty.
	Gone bool `json:"gone,omitempty"`

	Repo           string   `json:"repo"` // GitHub's nameWithOwner
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	URL            string   `json:"url"`
	State          string   `json:"state"`
	Body           string   `json:"body"`
	IsDraft        bool     `json:"is_draft"`
	UpdatedAt      string   `json:"updated_at"`
	ReviewDecision string   `json:"review_decision"`
	Mergeable      string   `json:"mergeable"`
	Author         string   `json:"author"`
	LabelCount     int      `json:"label_count"`
	Labels         []string `json:"labels"`
	// CommentCount counts top-level PR comments only; ReviewThreadCount the
	// inline review threads.
	CommentCount      int    `json:"comment_count"`
	ReviewCount       int    `json:"review_count"`
	ReviewThreadCount int    `json:"review_thread_count"`
	HeadRefOid        string `json:"head_ref_oid"`
	HeadRefName       string `json:"head_ref_name"`
	BaseRefName       string `json:"base_ref_name"`
	BaseRefOid        string `json:"base_ref_oid"`
	// ChecksState is the head commit's statusCheckRollup state as GitHub spells
	// it; empty when the commit has no checks.
	ChecksState string `json:"checks_state"`
}

// Ref is the wire reference owner/repo#n, or "" for a Gone summary.
func (s Summary) Ref() string {
	if s.Gone || s.Repo == "" {
		return ""
	}
	return s.Repo + "#" + strconv.Itoa(s.Number)
}

// Fingerprint is the hash of everything the group carries, so it changes when
// any of it does: in particular on a change of comment, review or review-thread
// count, updatedAt, head, base or state. It is a pure function of the value:
// when the group was fetched (fetched_at) and how long it stays fresh are not
// part of it.
//
// It includes Mergeable, so call it after the mergeability rule has replaced the
// observed value with the carried one (Persister.Persist does): then the
// fingerprint is stable across UNKNOWN. Label names are hashed in sorted order,
// so a reordered answer does not look like a change.
func (s Summary) Fingerprint() string {
	c := s
	c.Labels = slices.Clone(s.Labels)
	slices.Sort(c.Labels)
	raw, err := json.Marshal(c)
	if err != nil { // a struct of strings, ints and bools cannot fail to encode
		panic("summary: marshal: " + err.Error())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
