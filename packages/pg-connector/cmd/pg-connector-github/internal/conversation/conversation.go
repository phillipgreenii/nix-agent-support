// Package conversation fetches the conversation field group of a pull request:
// every review with its body, state, submitted time and review commit, every
// review thread's comments (nested in the review they belong to), and the PR's
// issue comments, together with the per-connection truncation report. The group
// is read per PR, paged as `pr show` does today with today's caps (1,000
// reviews, 1,000 threads, 1,000 comments), by the two Provider reads
// ListCommentsReport and ListReviewsReport; behind the metered Provider every
// page is admitted and booked by the governor.
//
// The stored content is already in wire shape (schema.PRComment,
// schema.PRReview, schema.PRConnections), so the `show` op copies it into
// schema.PR. The conversion rules are copied from the old backend
// (cmd/pg-connector-pr-github/internal/provider.go: toSchemaPR's comment and
// review loops, toSchemaComment, toSchemaConnectionReport) because the new
// binary cannot import that package; convert_test.go carries copies of the old
// tests.
package conversation

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Content is the content of the conversation group. Its JSON is what the group
// stores. Comments are the PR's top-level comments (and any inline comment whose
// review could not be joined, so none is dropped); a review carries its own
// inline comments. Everything is newest first, as the Provider returns it.
type Content struct {
	Comments    []schema.PRComment   `json:"comments"`
	Reviews     []schema.PRReview    `json:"reviews"`
	Connections schema.PRConnections `json:"connections"`
}

// Decode reads the content of a stored conversation row.
func Decode(content []byte) (Content, error) {
	if len(content) == 0 {
		return Content{}, errors.New("conversation: no content")
	}
	var c Content
	if err := json.Unmarshal(content, &c); err != nil {
		return Content{}, fmt.Errorf("conversation: decode content: %w", err)
	}
	return c, nil
}

// canonical returns the content bytes of c: its JSON with non-nil Comments and
// Reviews, so an empty conversation is stored as two empty arrays.
func (c Content) canonical() ([]byte, error) {
	if c.Comments == nil {
		c.Comments = []schema.PRComment{}
	}
	if c.Reviews == nil {
		c.Reviews = []schema.PRReview{}
	}
	return json.Marshal(c)
}

// Fingerprint is the group's fingerprint: the hash of the canonical content.
// No timestamp is excluded: a comment's or review's time is part of the content
// and a change of it is a change of the group.
func Fingerprint(c Content) (string, error) {
	b, err := c.canonical()
	if err != nil {
		return "", err
	}
	return groups.Fingerprint(b), nil
}
