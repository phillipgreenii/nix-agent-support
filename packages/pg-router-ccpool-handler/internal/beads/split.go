package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Split-triage labels (bead pg2-47rsh). LabelNeedsSplitReview lives in
// budgetstop.go; these are the outcome markers the split-triage role writes.
const (
	LabelWasSplit        = "was-split"
	LabelSplitFromPrefix = "split-from:"
	LabelHuman           = "human"
)

// SplitChild is one self-contained child of a split. Every field arrives from
// the triage session as DATA (JSON on the split subcommand's stdin) and is
// handed to bd as a single argv element -- never through a shell -- so bead
// text cannot inject commands.
type SplitChild struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Acceptance  string `json:"acceptance"`
	Priority    string `json:"priority,omitempty"` // bd -p value; "" keeps bd's default
	Type        string `json:"type,omitempty"`     // bd --type value; "" => task
}

// SplitPlan is the triage session's decomposition decision.
type SplitPlan struct {
	Rationale string       `json:"rationale"`
	Children  []SplitChild `json:"children"`
}

// Validate rejects a plan that is not a real decomposition: fewer than two
// children, or a child that is not self-contained (title, description and
// acceptance criteria all required).
func (p SplitPlan) Validate() error {
	if len(p.Children) < 2 {
		return fmt.Errorf("a split needs at least 2 children, got %d", len(p.Children))
	}
	for i, c := range p.Children {
		switch {
		case strings.TrimSpace(c.Title) == "":
			return fmt.Errorf("child %d: title is required", i+1)
		case strings.TrimSpace(c.Description) == "":
			return fmt.Errorf("child %d (%s): description is required (children must be self-contained)", i+1, c.Title)
		case strings.TrimSpace(c.Acceptance) == "":
			return fmt.Errorf("child %d (%s): acceptance criteria are required", i+1, c.Title)
		}
	}
	return nil
}

// childLabels derives a child's labels from its parent: the parent's labels
// minus everything that is the PARENT's own state (stop history, split-review
// and outcome markers, human/escalated flags, an earlier split-from), plus
// split-from:<parent>. Dropping the budget-stop:* labels is what keeps a child
// from inheriting the parent's stop count.
func childLabels(parent Issue) []string {
	var out []string
	for _, l := range parent.Labels {
		switch {
		case strings.HasPrefix(l, BudgetStopLabelPrefix),
			strings.HasPrefix(l, LabelSplitFromPrefix),
			l == LabelNeedsSplitReview, l == LabelWasSplit, l == LabelHuman, l == "escalated":
			continue
		}
		out = append(out, l)
	}
	return append(out, LabelSplitFromPrefix+parent.ID)
}

// releaseIfClaimed returns a still-claimed parent to the pool in ONE bd update
// (status open + assignee cleared); a parent that is not in_progress is left
// alone.
func releaseIfClaimed(ctx context.Context, r Runner, iss Issue) error {
	if iss.Status != "in_progress" {
		return nil
	}
	return Unclaim(ctx, r, iss.ID)
}

// ApplySplit executes a SPLITTABLE decision on parentID and returns the new
// child ids. Order matters: the needs-split-review label is removed LAST,
// because its removal is the split-triage completion signal (complete.Tracker)
// and the session may be torn down the moment it is seen.
//
//  1. refuse a closed parent, one already split, or one that is itself a split
//     child (no second round);
//  2. create each child (labels per childLabels, so no stop-count inheritance);
//  3. wire parent blocked-by child -- `bd dep add <parent> <child>`, blocked id
//     first, default blocks type -- and verify with `bd dep list <parent>`;
//  4. mark the parent was-split, comment the decomposition, release a held
//     claim, then remove needs-split-review.
//
// If anything fails after children were created, the parent is failed safe to
// a human (human added, needs-split-review removed, comment naming the created
// children) so the session never loops back into the split path.
func ApplySplit(ctx context.Context, r Runner, parentID string, plan SplitPlan) ([]string, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	parent, err := ShowObj(ctx, r, parentID)
	if err != nil {
		return nil, fmt.Errorf("read parent %s: %w", parentID, err)
	}
	if parent.ID == "" {
		parent.ID = parentID
	}
	if reason := splitRefusal(parent); reason != "" {
		return nil, fmt.Errorf("refusing to split %s: %s", parentID, reason)
	}
	labels := childLabels(parent)
	var ids []string
	fail := func(err error) ([]string, error) {
		msg := fmt.Sprintf("split-triage failed after creating children [%s]: %v; escalated to human", strings.Join(ids, ", "), err)
		_ = AddHuman(ctx, r, parentID)
		_ = Comment(ctx, r, parentID, msg)
		_ = releaseIfClaimed(ctx, r, parent)
		_ = RemoveLabel(ctx, r, parentID, LabelNeedsSplitReview)
		return ids, fmt.Errorf("%w (parent %s failed safe to human)", err, parentID)
	}
	for _, c := range plan.Children {
		typ := c.Type
		if typ == "" {
			typ = "task"
		}
		args := []string{
			"create", "--title=" + c.Title, "--description=" + c.Description,
			"--acceptance=" + c.Acceptance, "--labels=" + strings.Join(labels, ","),
			"--type=" + typ, "--silent",
		}
		if c.Priority != "" {
			args = append(args, "--priority="+c.Priority)
		}
		out, err := r.Run(ctx, args...)
		if err != nil {
			return fail(fmt.Errorf("create child %q: %w", c.Title, err))
		}
		id := strings.TrimSpace(out)
		if id == "" {
			return fail(fmt.Errorf("create child %q returned no id", c.Title))
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, err := r.Run(ctx, "dep", "add", parentID, id); err != nil {
			return fail(fmt.Errorf("wire %s blocked-by %s: %w", parentID, id, err))
		}
	}
	if err := verifyBlockedBy(ctx, r, parentID, ids); err != nil {
		return fail(err)
	}
	if err := AddLabel(ctx, r, parentID, LabelWasSplit); err != nil {
		return fail(err)
	}
	if err := Comment(ctx, r, parentID, splitComment(plan, ids)); err != nil {
		return fail(err)
	}
	if err := releaseIfClaimed(ctx, r, parent); err != nil {
		return fail(err)
	}
	if err := RemoveLabel(ctx, r, parentID, LabelNeedsSplitReview); err != nil {
		return ids, err
	}
	return ids, nil
}

// MarkUnsplittable executes a NOT-splittable decision: human added, a summary
// comment (the triage session's reason plus the deterministic stop history),
// a held claim released, and needs-split-review removed last.
func MarkUnsplittable(ctx context.Context, r Runner, parentID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("a reason is required to mark a bead not splittable")
	}
	parent, err := ShowObj(ctx, r, parentID)
	if err != nil {
		return fmt.Errorf("read parent %s: %w", parentID, err)
	}
	if parent.ID == "" {
		parent.ID = parentID
	}
	if err := AddHuman(ctx, r, parentID); err != nil {
		return err
	}
	comment := fmt.Sprintf("split-triage: not splittable; escalated to human. reason: %s. budget stops: %d (sessions: %s)",
		strings.TrimSpace(reason), BudgetStopCount(parent), strings.Join(BudgetStopSessions(parent), ", "))
	if err := Comment(ctx, r, parentID, comment); err != nil {
		return err
	}
	if err := releaseIfClaimed(ctx, r, parent); err != nil {
		return err
	}
	return RemoveLabel(ctx, r, parentID, LabelNeedsSplitReview)
}

func splitRefusal(iss Issue) string {
	if iss.Status == "closed" {
		return "bead is closed"
	}
	if iss.HasLabel(LabelWasSplit) {
		return "already split"
	}
	for _, l := range iss.Labels {
		if strings.HasPrefix(l, LabelSplitFromPrefix) {
			return "bead is itself a split child"
		}
	}
	return ""
}

func splitComment(p SplitPlan, ids []string) string {
	titles := make([]string, len(ids))
	for i, id := range ids {
		titles[i] = id + " " + p.Children[i].Title
	}
	return fmt.Sprintf("split-triage: split into %d children, parent blocked-by each: %s. rationale: %s",
		len(ids), strings.Join(titles, "; "), strings.TrimSpace(p.Rationale))
}

// verifyBlockedBy reads `bd dep list <parent> --json` (dependencies of the
// parent, i.e. what blocks it) and requires every child id to appear. A
// reversed edge would show the children under --direction=up instead, so they
// would be missing here.
func verifyBlockedBy(ctx context.Context, r Runner, parentID string, childIDs []string) error {
	out, err := r.Run(ctx, "dep", "list", parentID, "--json")
	if err != nil {
		return fmt.Errorf("verify edges of %s: %w", parentID, err)
	}
	have := map[string]bool{}
	for _, d := range decodeMany([]byte(out)) {
		have[d.ID] = true
	}
	var missing []string
	for _, id := range childIDs {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("dep list %s lacks blocked-by edge(s) to %s", parentID, strings.Join(missing, ", "))
	}
	return nil
}

// DecodeSplitPlan decodes the JSON plan the triage session supplies.
func DecodeSplitPlan(b []byte) (SplitPlan, error) {
	var p SplitPlan
	if err := json.Unmarshal(b, &p); err != nil {
		return SplitPlan{}, fmt.Errorf("decode split plan: %w", err)
	}
	return p, nil
}
