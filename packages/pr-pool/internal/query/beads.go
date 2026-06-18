package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// BeadsReady runs `bd ready` with label filters, then applies optional client-side
// title_prefix / item_type post-filters (the former feedback cycle-identity guard).
type BeadsReady struct {
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	TitlePrefix   string   `toml:"title_prefix"`
	ItemType      string   `toml:"item_type"`
}

func (q BeadsReady) Validate() error { return nil }

func (q BeadsReady) Run(ctx context.Context, env Env) ([]item.Item, error) {
	issues, err := beads.Ready(ctx, env.BD, labelArgs(q.Labels, q.ExcludeLabels)...)
	if err != nil {
		return nil, fmt.Errorf("beads-ready query: %w", err)
	}
	return fromIssues(postFilter(issues, q.TitlePrefix, q.ItemType)), nil
}

// BeadsList runs `bd list` with the same filter shape.
type BeadsList struct {
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	TitlePrefix   string   `toml:"title_prefix"`
	ItemType      string   `toml:"item_type"`
}

func (q BeadsList) Validate() error { return nil }

func (q BeadsList) Run(ctx context.Context, env Env) ([]item.Item, error) {
	issues, err := beads.List(ctx, env.BD, labelArgs(q.Labels, q.ExcludeLabels)...)
	if err != nil {
		return nil, fmt.Errorf("beads-list query: %w", err)
	}
	return fromIssues(postFilter(issues, q.TitlePrefix, q.ItemType)), nil
}

func labelArgs(labels, exclude []string) []string {
	var a []string
	for _, l := range labels {
		a = append(a, "--label", l)
	}
	for _, l := range exclude {
		a = append(a, "--exclude-label", l)
	}
	return a
}

func postFilter(in []beads.Issue, titlePrefix, itemType string) []beads.Issue {
	if titlePrefix == "" && itemType == "" {
		return in
	}
	var out []beads.Issue
	for _, i := range in {
		if itemType != "" && i.Type != itemType {
			continue
		}
		if titlePrefix != "" && !strings.HasPrefix(i.Title, titlePrefix) {
			continue
		}
		out = append(out, i)
	}
	return out
}
