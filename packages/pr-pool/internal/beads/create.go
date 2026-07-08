package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReviewPRTitlePrefix marks a review-pr work bead. Following the house pattern
// (draft-review / attention / processing-cycle all reuse the builtin `task`
// type discriminated by a title prefix), review-pr beads are `task` beads so no
// bd custom-type registration is required.
const ReviewPRTitlePrefix = "review-pr: "

// MergeRequestType is the (registered) custom type pg-pr creates PR beads under.
const MergeRequestType = "merge-request"

// Create runs `bd create --type=<typ> --title <title> -d <title> [--metadata
// <json>] --silent` and returns the new bead id. metadata, when non-empty, is
// serialized as a JSON object so numeric fields (pr_number) stay numeric.
func Create(ctx context.Context, r Runner, typ, title string, metadata map[string]any) (string, error) {
	args := []string{"create", "--type=" + typ, "--title", title, "-d", title}
	if len(metadata) > 0 {
		blob, err := json.Marshal(metadata)
		if err != nil {
			return "", fmt.Errorf("marshal metadata: %w", err)
		}
		args = append(args, "--metadata", string(blob))
	}
	args = append(args, "--silent")
	out, err := r.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("create %s bead: %w", typ, err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("create %s bead returned no id", typ)
	}
	return id, nil
}

// LinkChild links childID under parentID with a parent-child dependency, so the
// child is discoverable as a child of the PR (merge-request) bead and is closed
// by pg-pr's cascadeClose when the PR closes. bd has no `create --parent`; the
// edge is a separate `dep add` (mirroring pg-pr's draft-review linking).
func LinkChild(ctx context.Context, r Runner, childID, parentID string) error {
	if _, err := r.Run(ctx, "dep", "add", childID, parentID, "--type=parent-child", "--no-cycle-check"); err != nil {
		return fmt.Errorf("link %s under %s: %w", childID, parentID, err)
	}
	return nil
}

// MatchMergeRequest returns the merge-request bead for repo#number from a
// pre-fetched list (pass the result of `List(--type=merge-request --all)`), or
// nil. The ACL uses this to FIND-OR-REUSE the pg-pr-owned MR bead — it MUST NOT
// create one (pg-pr's sync daemon is the sole MR producer until the later
// strip). Matching over a pre-fetched list avoids a per-PR `bd list` spawn.
func MatchMergeRequest(issues []Issue, repo string, number int) *Issue {
	for i := range issues {
		if matchesPR(issues[i], repo, number) {
			return &issues[i]
		}
	}
	return nil
}

// MatchReviewPR returns the review-pr work bead for repo#number from a
// pre-fetched task list (pass `List(--type=task --all)` — INCLUDING closed, so a
// completed review is not resurrected). It matches on the review-pr title prefix
// + the repo/pr_number metadata, so it is distinct from pg-pr's own draft-review
// task beads.
func MatchReviewPR(issues []Issue, repo string, number int) *Issue {
	for i := range issues {
		if strings.HasPrefix(issues[i].Title, ReviewPRTitlePrefix) && matchesPR(issues[i], repo, number) {
			return &issues[i]
		}
	}
	return nil
}

// matchesPR reports whether an issue's metadata identifies repo#number. bd
// serializes metadata through JSON, so pr_number arrives as a float64; older or
// hand-written stores may carry it as a string.
func matchesPR(iss Issue, repo string, number int) bool {
	if iss.Metadata == nil {
		return false
	}
	if r, _ := iss.Metadata["repo"].(string); r != repo {
		return false
	}
	n, ok := asInt(iss.Metadata["pr_number"])
	return ok && n == number
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n, true
		}
	}
	return 0, false
}
