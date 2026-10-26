package backfill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The rollback of the first apply (design 6.5, step 8). The run record says WHICH
// writes the lane made (WritesSinceFlip) but not on which work item, so the
// listing the operator rolls back from is the tracker's own: every audit comment
// (internal/audit) the decider posted since the flip bound. Reverting one write
// needs the value it replaced, which no audit comment carries, so the operator
// takes a before-image of the tracker (Snapshot) just before the flip marker.
//
// The revert of a write is its inverse, not a restore of the whole item, so a
// change a person made since the flip is not undone:
//
//	create  -> close the created item
//	close   -> reopen it (only if it is still closed)
//	reopen  -> close it (only if it is still open)
//	update  -> put back the title, priority, description, labels and metadata
//	           the before-image holds (status and assignee are not part of it)
//
// Closing a created item is the only revert of a create: a tracker has no delete.

// SnapshotContract is the contract string of the tracker before-image file.
const SnapshotContract = "pg-pr-backfill.tracker-snapshot/v1"

// ErrNoBeforeImage means the snapshot cannot serve as the before-image of the
// flip: it is missing, newer than the flip bound, or does not agree with the audit
// comments. Nothing was written.
var ErrNoBeforeImage = errors.New("the tracker snapshot is not a before-image of the flip")

// Item is the part of one tracker work item the snapshot holds and a rollback
// compares and restores. The names are those of `bd list --json`.
type Item struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Status      string         `json:"status"`
	Priority    int            `json:"priority"`
	Labels      []string       `json:"labels,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
}

// Snapshot is the tracker's before-image: every work item in every status.
type Snapshot struct {
	Contract string `json:"contract"`
	// At is when the snapshot was taken (RFC 3339, UTC, second resolution).
	At    string `json:"at"`
	Items []Item `json:"items"`

	T time.Time `json:"-"`
}

// Tracker names the tracker a rollback reads and writes: the bd binary and the
// directory of the workspace it runs in (the deployment's beads_dir).
type Tracker struct {
	Bd     string
	Dir    string
	Actor  string
	Env    []string
	Runner Runner
}

// RollbackActor is the actor name the rollback's own writes carry.
const RollbackActor = "pg-pr-backfill-rollback"

func (tr Tracker) run(ctx context.Context, args ...string) (string, error) {
	run := tr.Runner
	if run == nil {
		run = ExecRunner
	}
	name := tr.Bd
	if name == "" {
		name = "bd"
	}
	actor := tr.Actor
	if actor == "" {
		actor = RollbackActor
	}
	res := run(ctx, Call{Name: name, Args: append([]string{"--actor", actor}, args...), Dir: tr.Dir, Env: tr.Env})
	if res.StartErr != nil {
		return "", fmt.Errorf("bd %s: %w", strings.Join(args, " "), res.StartErr)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("bd %s: exit %d: %s", strings.Join(args, " "), res.Code, firstLine(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

// jsonData decodes the {"data": ...} envelope of a bd --json answer, tolerating
// text printed before the JSON object.
func jsonData(out string, into any) error {
	i := strings.Index(out, "{")
	if i < 0 {
		return fmt.Errorf("no JSON in %q", firstLine(out))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(out[i:])).Decode(&env); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, into)
}

// ListItems lists every work item of the tracker in every status.
func (tr Tracker) ListItems(ctx context.Context) ([]Item, error) {
	out, err := tr.run(ctx, "list", "--all", "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	var items []Item
	if err := jsonData(out, &items); err != nil {
		return nil, fmt.Errorf("read the tracker listing: %w", err)
	}
	return items, nil
}

// TakeSnapshot reads the tracker into a before-image. It fails closed on an empty
// listing: a tracker with no item is a wrong directory, not a before-image.
func (tr Tracker) TakeSnapshot(ctx context.Context, now func() time.Time) (*Snapshot, error) {
	items, err := tr.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("the tracker lists no work item: refusing to take an empty before-image (is the tracker directory right?)")
	}
	if now == nil {
		now = time.Now
	}
	at := now().UTC().Truncate(time.Second)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return &Snapshot{Contract: SnapshotContract, At: at.Format(time.RFC3339), T: at, Items: items}, nil
}

// WriteSnapshot writes the snapshot to path (a temporary file, then a rename).
func WriteSnapshot(path string, s *Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSnapshot reads a snapshot file. A missing file is ErrNoBeforeImage.
func LoadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s does not exist (take it with `pg-pr-backfill snapshot` BEFORE the flip)", ErrNoBeforeImage, path)
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("the tracker snapshot %s is not JSON: %w", path, err)
	}
	if s.Contract != SnapshotContract {
		return nil, fmt.Errorf("the tracker snapshot %s has contract %q, want %q", path, s.Contract, SnapshotContract)
	}
	s.T = parseTime(s.At)
	if s.T.IsZero() {
		return nil, fmt.Errorf("the tracker snapshot %s has no readable time (at=%q)", path, s.At)
	}
	if len(s.Items) == 0 {
		return nil, fmt.Errorf("the tracker snapshot %s holds no work item", path)
	}
	return &s, nil
}

// AuditWrite is one write read back from an audit comment of the decider.
type AuditWrite struct {
	At      time.Time
	Item    string
	Op      string
	Rule    string
	Comment string
}

// AuditHeader is the first line of the decider's audit comment (internal/audit).
const AuditHeader = "pg-decider audit"

// ParseAudit reads the rule, op and time out of an audit comment body; false for
// any other comment.
func ParseAudit(text string) (rule, op string, at time.Time, ok bool) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != AuditHeader {
		return "", "", time.Time{}, false
	}
	for _, l := range lines[1:] {
		k, v, found := strings.Cut(l, ": ")
		if !found {
			continue
		}
		switch k {
		case "rule":
			rule = v
		case "op":
			op = v
		case "at":
			at = parseTime(v)
		}
	}
	return rule, op, at, rule != "" && op != "" && !at.IsZero()
}

// ListAuditWrites reads the audit comments of every item that may have been
// written since `since` (an item last updated before it cannot carry one), keeps
// the audit comments stamped at or after it, and returns them by timestamp (a tie
// by item, then by the comment's own id).
func (tr Tracker) ListAuditWrites(ctx context.Context, items []Item, since time.Time) ([]AuditWrite, error) {
	var out []AuditWrite
	for _, it := range items {
		if u := parseTime(it.UpdatedAt); !u.IsZero() && u.Before(since.Truncate(time.Second)) {
			continue
		}
		raw, err := tr.run(ctx, "comments", it.ID, "--json")
		if err != nil {
			return nil, err
		}
		var cs []struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			CreatedAt string `json:"created_at"`
		}
		if err := jsonData(raw, &cs); err != nil {
			return nil, fmt.Errorf("read the comments of %s: %w", it.ID, err)
		}
		// A rollback note marks the item as rolled back up to its time: the writes
		// before it are not listed again, so a second rollback is a no-op.
		var rolledBack time.Time
		for _, c := range cs {
			if strings.HasPrefix(c.Text, RollbackNotePrefix) {
				if t := parseTime(c.CreatedAt); t.After(rolledBack) {
					rolledBack = t
				}
			}
		}
		for _, c := range cs {
			rule, op, at, ok := ParseAudit(c.Text)
			if !ok || at.Before(since) || (!rolledBack.IsZero() && !at.After(rolledBack)) {
				continue
			}
			out = append(out, AuditWrite{At: at, Item: it.ID, Op: op, Rule: rule, Comment: c.ID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		return a.Comment < b.Comment
	})
	return out, nil
}

// Step is one revert the rollback makes, or leaves undone on purpose.
type Step struct {
	Item string
	// Kind is close, reopen, restore or leave.
	Kind string
	Why  string
	// Args is the bd call that does it (empty for leave).
	Args []string
}

// Expect is what a verified rollback finds on one item.
type Expect struct {
	// Status is the status the item MUST have afterwards ("" for no expectation).
	Status string
	// Restore: the title, priority, description, labels and metadata MUST equal the
	// before-image's.
	Restore bool
}

// RollbackPlan is everything a rollback would do.
type RollbackPlan struct {
	Bound  time.Time
	Writes []AuditWrite
	Steps  []Step
	Expect map[string]Expect

	snap *Snapshot
}

func metaText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func metaMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = metaText(v)
	}
	return out
}

func sortedCopyStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// restoreArgs is the `bd update` that puts the before-image's fields back on cur;
// nil when they already agree.
func restoreArgs(before, cur Item) []string {
	args := []string{"update", cur.ID}
	if before.Title != cur.Title {
		args = append(args, "--title", before.Title)
	}
	if before.Priority != cur.Priority {
		args = append(args, "--priority", strconv.Itoa(before.Priority))
	}
	if before.Description != cur.Description {
		args = append(args, "--description", before.Description)
	}
	have := map[string]bool{}
	for _, l := range cur.Labels {
		have[l] = true
	}
	want := map[string]bool{}
	for _, l := range before.Labels {
		want[l] = true
	}
	for _, l := range sortedCopyStrings(before.Labels) {
		if !have[l] {
			args = append(args, "--add-label", l)
		}
	}
	for _, l := range sortedCopyStrings(cur.Labels) {
		if !want[l] {
			args = append(args, "--remove-label", l)
		}
	}
	bm, cm := metaMap(before.Metadata), metaMap(cur.Metadata)
	for _, k := range sortedKeys(bm) {
		if v, ok := cm[k]; !ok || v != bm[k] {
			args = append(args, "--set-metadata", k+"="+bm[k])
		}
	}
	for _, k := range sortedKeys(cm) {
		if _, ok := bm[k]; !ok {
			args = append(args, "--unset-metadata", k)
		}
	}
	if len(args) == 2 {
		return nil
	}
	return args
}

func fieldsEqual(a, b Item) bool { return restoreArgs(a, b) == nil }

// RollbackNotePrefix starts the comment a rollback leaves on every item it changed.
const RollbackNotePrefix = "pg-pr-backfill rollback"

const rollbackReason = RollbackNotePrefix + " of the first apply"

// PlanRollback decides, from the audit comments since the flip bound, the
// before-image and the tracker as it is now, what to revert. It refuses (an error
// wrapping ErrNoBeforeImage) when the before-image is newer than the bound or does
// not agree with the writes.
func PlanRollback(snap *Snapshot, bound time.Time, now []Item, writes []AuditWrite) (*RollbackPlan, error) {
	if snap.T.After(bound) {
		return nil, fmt.Errorf("%w: it was taken at %s, after the flip bound %s; a snapshot taken after writes began holds their results", ErrNoBeforeImage, snap.T.Format(time.RFC3339), bound.Format(time.RFC3339))
	}
	before := map[string]Item{}
	for _, it := range snap.Items {
		before[it.ID] = it
	}
	cur := map[string]Item{}
	for _, it := range now {
		cur[it.ID] = it
	}
	byItem := map[string][]AuditWrite{}
	var order []string // items by their newest write, newest first
	for i := len(writes) - 1; i >= 0; i-- {
		w := writes[i]
		if _, seen := byItem[w.Item]; !seen {
			order = append(order, w.Item)
		}
		byItem[w.Item] = append(byItem[w.Item], w) // newest first
	}
	p := &RollbackPlan{Bound: bound, Writes: writes, Expect: map[string]Expect{}, snap: snap}
	for _, id := range order {
		c, ok := cur[id]
		if !ok {
			return nil, fmt.Errorf("an audit comment names %s and the tracker does not list it", id)
		}
		b, inSnap := before[id]
		sim := c.Status
		var steps []Step
		exp := Expect{}
		hasUpdate, hasCreate := false, false
		// moved is set once a write's result is not what the tracker shows now: a
		// person changed the status since, so no older status write is reverted.
		moved := false
		leave := func(w AuditWrite, what string) {
			steps = append(steps, Step{id, "leave", fmt.Sprintf("%s by %s at %s, but the item is %s now: changed since, left as it is", what, w.Rule, w.At.Format(time.RFC3339), sim), nil})
		}
		for _, w := range byItem[id] {
			switch w.Op {
			case "create":
				hasCreate = true
				if !moved && sim != "closed" {
					steps = append(steps, Step{
						id, "close", fmt.Sprintf("created by %s at %s: closing it is the revert of a create", w.Rule, w.At.Format(time.RFC3339)),
						[]string{"close", id, "--reason", rollbackReason + ": undo the create", "--force"},
					})
					sim = "closed"
				}
				exp.Status = "closed"
			case "close":
				switch {
				case moved:
					leave(w, "closed")
				case sim == "closed":
					steps = append(steps, Step{
						id, "reopen", fmt.Sprintf("closed by %s at %s", w.Rule, w.At.Format(time.RFC3339)),
						[]string{"reopen", id, "--reason", rollbackReason + ": undo the close"},
					})
					sim = "open"
					exp.Status = "open"
				default:
					moved = true
					leave(w, "closed")
				}
			case "reopen":
				switch {
				case moved:
					leave(w, "reopened")
				case sim != "closed":
					steps = append(steps, Step{
						id, "close", fmt.Sprintf("reopened by %s at %s", w.Rule, w.At.Format(time.RFC3339)),
						[]string{"close", id, "--reason", rollbackReason + ": undo the reopen", "--force"},
					})
					sim = "closed"
					exp.Status = "closed"
				default:
					moved = true
					leave(w, "reopened")
				}
			case "update":
				hasUpdate = true
			default:
				return nil, fmt.Errorf("the audit comment of %s at %s has the op %q, which a rollback does not know", id, w.At.Format(time.RFC3339), w.Op)
			}
		}
		switch {
		case hasCreate && inSnap:
			return nil, fmt.Errorf("%w: %s was created since the flip and the snapshot already holds it", ErrNoBeforeImage, id)
		case !hasCreate && !inSnap:
			return nil, fmt.Errorf("%w: %s has audit comments since the flip but no create among them and the snapshot does not hold it", ErrNoBeforeImage, id)
		}
		if hasUpdate && inSnap {
			if args := restoreArgs(b, c); args != nil {
				steps = append([]Step{{id, "restore", "fields put back from the before-image: " + strings.Join(args[2:], " "), args}}, steps...)
			}
			exp.Restore = true
		}
		p.Steps = append(p.Steps, steps...)
		p.Expect[id] = exp
	}
	return p, nil
}

// Planned counts the steps that change the tracker (the leaves do not).
func (p *RollbackPlan) Planned() (n int) {
	for _, s := range p.Steps {
		if s.Kind != "leave" {
			n++
		}
	}
	return n
}

// PrintRollback prints the by-timestamp listing of the writes since the bound and
// the steps the rollback takes.
func PrintRollback(w io.Writer, p *RollbackPlan) {
	fmt.Fprintf(w, "writes since: %s (the earlier of the flip marker and the first apply-mode run record)\n", p.Bound.Format(time.RFC3339))
	byOp := map[string]int{}
	for _, x := range p.Writes {
		fmt.Fprintf(w, "%s %s %s rule=%s\n", x.At.Format(time.RFC3339), x.Item, x.Op, x.Rule)
		byOp[x.Op]++
	}
	var parts []string
	for _, k := range sortedKeys(byOp) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, byOp[k]))
	}
	fmt.Fprintf(w, "writes total=%d %s\n", len(p.Writes), strings.Join(parts, " "))
	for _, s := range p.Steps {
		fmt.Fprintf(w, "revert %s %s: %s\n", s.Item, s.Kind, s.Why)
	}
}

// Apply makes the steps, newest write first, goes on past a step that fails, adds
// one comment naming what was done to each item it changed, and then verifies the
// tracker against what the plan expects. It returns the problems it found (a
// failed step, a failed verification); none means the rollback is complete.
func (p *RollbackPlan) Apply(ctx context.Context, tr Tracker) (done int, problems []string) {
	touched := map[string][]string{}
	failed := map[string]bool{}
	var ids []string
	for _, s := range p.Steps {
		if s.Kind == "leave" {
			continue
		}
		if _, err := tr.run(ctx, s.Args...); err != nil {
			problems = append(problems, fmt.Sprintf("%s %s failed: %v", s.Item, s.Kind, err))
			failed[s.Item] = true
			continue
		}
		done++
		if _, seen := touched[s.Item]; !seen {
			ids = append(ids, s.Item)
		}
		touched[s.Item] = append(touched[s.Item], s.Kind)
	}
	for _, id := range ids {
		if failed[id] {
			continue // no note: the next rollback must list its writes again
		}
		note := rollbackReason + ": " + strings.Join(touched[id], ", ") + " (writes since " + p.Bound.Format(time.RFC3339) + ")"
		if _, err := tr.run(ctx, "comments", "add", id, note); err != nil {
			problems = append(problems, fmt.Sprintf("%s: the rollback note was not posted: %v", id, err))
		}
	}
	problems = append(problems, p.Verify(ctx, tr)...)
	return done, problems
}

// Verify reads the tracker again and names every item that is not as the plan
// expects.
func (p *RollbackPlan) Verify(ctx context.Context, tr Tracker) []string {
	now, err := tr.ListItems(ctx)
	if err != nil {
		return []string{"verify: " + err.Error()}
	}
	cur := map[string]Item{}
	for _, it := range now {
		cur[it.ID] = it
	}
	before := map[string]Item{}
	for _, it := range p.snap.Items {
		before[it.ID] = it
	}
	var out []string
	for _, id := range sortedKeys(p.Expect) {
		e, c := p.Expect[id], cur[id]
		if e.Status != "" && c.Status != e.Status {
			out = append(out, fmt.Sprintf("verify: %s is %q, want %q", id, c.Status, e.Status))
		}
		if b, ok := before[id]; ok && e.Restore && !fieldsEqual(b, c) {
			out = append(out, fmt.Sprintf("verify: %s differs from the before-image: %s", id, strings.Join(restoreArgs(b, c)[2:], " ")))
		}
	}
	return out
}

// PrintSnapshot prints a one-line account of a snapshot.
func PrintSnapshot(w io.Writer, s *Snapshot, path string) {
	var open, closed int
	for _, it := range s.Items {
		if it.Status == "closed" {
			closed++
		} else {
			open++
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "snapshot recorded: items=%d (not closed=%d closed=%d) at=%s file=%s", len(s.Items), open, closed, s.At, path)
	fmt.Fprintln(w, b.String())
}
