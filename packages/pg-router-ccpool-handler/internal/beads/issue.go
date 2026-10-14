package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// idPattern matches bd issue ids: alphanumerics plus `.`, `_`, `-` (hierarchical
// ids like pg2-abc.1 included). Non-bead item ids never match: a PR item's id
// is `owner/repo#N` (contains `/` and `#`) and a heartbeat/tick item's id is an
// RFC3339 timestamp (contains `:`).
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsID reports whether id has the shape of a bd issue id. It is the cheap,
// local kind check for "could this item be a bead at all": bd itself only
// ever mints ids of this shape, so a false result proves the item is not a
// bead (PR, heartbeat, ...), while a true result only means a bd lookup is
// worth attempting. It also keeps an id from being read as a flag by bd.
func IsID(id string) bool { return idPattern.MatchString(id) }

// Issue is the subset of a bd issue pg-router reads. Metadata is left as a generic
// map (bd serializes merge-request fields like author/repo/pr_number into it).
type Issue struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Type   string   `json:"issue_type"`
	Parent string   `json:"parent"`
	Labels []string `json:"labels"`
	// CommentCount is bd's own comment_count on `bd show --json`; the
	// close-or-triage completion (internal/complete) reads it to see a triage
	// comment appear. 0 when absent.
	CommentCount int            `json:"comment_count"`
	Metadata     map[string]any `json:"metadata"`
	CreatedBy    string         `json:"created_by"` // bd attributes creation to BEADS_ACTOR; lets a dispatch claim only its own new beads
	// Assignee is the bd assignee (the claiming actor) on `bd show --json`; ""
	// when unassigned. The orphan reconcile unclaims only a bead still assigned to
	// the role's own actor (INV-CCH-18).
	Assignee string `json:"assignee"`
	// DeferUntil is bd's defer_until: an RFC3339 timestamp while the bead is
	// deferred until a date, "" when absent or null. Read it with
	// DeferredUntilAfter, which fails open on a value that does not parse.
	DeferUntil string `json:"defer_until"`
	// Dependencies is bd's dependencies array AS `bd show --json` shapes it
	// (full issues, each with its dependency_type). It is meaningful ONLY on an
	// Issue read through ShowObj: `bd ready`/`bd list` shape the same key as
	// edge records, which decode into this type with every field empty, so a
	// reader MUST NOT infer anything from Dependencies of an Issue that came
	// from Ready or List. Only the fields below are declared: any other field
	// of either shape (the edge records' metadata is a STRING, a show
	// dependency's is not) would make decodeMany fail for every list read.
	Dependencies []Dependency `json:"dependencies"`
}

// Dependency is the part of one bd dependency this module reads, as `bd show
// --json` shapes it: the depended-on issue's id and status, and the kind of
// edge. See Issue.Dependencies for why nothing else is declared.
type Dependency struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type"`
}

// blocksType is bd's dependency_type for an ordering edge ("parent-child" and
// the other kinds never block).
const blocksType = "blocks"

// HasOpenBlocker reports whether the issue has a `blocks` dependency on an
// issue that is not closed. Only meaningful for an Issue read by ShowObj (see
// Issue.Dependencies).
func (i Issue) HasOpenBlocker() bool {
	for _, d := range i.Dependencies {
		if d.DependencyType == blocksType && d.ID != "" && d.Status != "closed" {
			return true
		}
	}
	return false
}

// DeferredUntilAfter reports whether defer_until is set and later than now. An
// absent value, or one that does not parse as RFC3339, is NOT deferred: every
// reader of this treats an unreadable value as "proceed" (fail open).
func (i Issue) DeferredUntilAfter(now time.Time) bool {
	if i.DeferUntil == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, i.DeferUntil)
	return err == nil && t.After(now)
}

// HasLabel reports whether the issue carries the given label.
func (i Issue) HasLabel(label string) bool {
	return slices.Contains(i.Labels, label)
}

// ShowObj runs `bd show <id> --json` and normalizes bd's output: the
// `{"data":[<issue>]}` envelope as well as the legacy bare array/object forms.
func ShowObj(ctx context.Context, r Runner, id string) (Issue, error) {
	out, err := r.Run(ctx, "show", id, "--json")
	if err != nil {
		return Issue{}, err
	}
	return decodeOne([]byte(out))
}

// Ready runs `bd ready <args...> --json --limit 0` and returns the issues.
// bd wraps the result in a `{"data":[...]}` envelope (see unwrapData); a null /
// empty / unparseable payload yields an empty slice.
func Ready(ctx context.Context, r Runner, args ...string) ([]Issue, error) {
	full := append(append([]string{"ready"}, args...), "--json", "--limit", "0")
	out, err := r.Run(ctx, full...)
	if err != nil {
		return nil, err
	}
	return decodeMany([]byte(out)), nil
}

// List runs `bd list <args...> --json --limit 0` and returns the issues. Used to
// snapshot the store before/after a dispatch; pass `--all` to include closed so a
// bead the worker created and then closed is still seen. Decodes the same
// `{"data":[...]}` envelope as Ready; a null / empty payload yields an empty slice.
func List(ctx context.Context, r Runner, args ...string) ([]Issue, error) {
	full := append(append([]string{"list"}, args...), "--json", "--limit", "0")
	out, err := r.Run(ctx, full...)
	if err != nil {
		return nil, err
	}
	return decodeMany([]byte(out)), nil
}

// Status returns the issue's current status ("" if unset).
func Status(ctx context.Context, r Runner, id string) (string, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return "", err
	}
	return iss.Status, nil
}

// Unclaim returns a bead to the open pool: `bd update <id> --status=open --assignee=`.
func Unclaim(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--status=open", "--assignee=")
	if err != nil {
		return fmt.Errorf("unclaim %s: %w", id, err)
	}
	return nil
}

// guardStaleExit is the exit code bd update returns when every failure was a
// stale --if-assignee/--if-status guard: the precondition no longer held and
// nothing was written (another actor won the race).
const guardStaleExit = 13

// IsGuardStale reports whether err is bd's "guard no longer held" refusal (exit
// 13 from an update carrying --if-assignee/--if-status). It is NOT a failure of
// bd itself: the claim changed hands between the read and the write, so a retry
// of the same guard is pointless.
func IsGuardStale(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == guardStaleExit
}

// heldGuard reads the bead and, when it is in_progress under a named assignee,
// returns the bd update guard flags that make a release of that exact claim
// pass bd's claim guard: bd 1.3.1 refuses an unguarded `--assignee=` overwrite
// of another actor's in_progress claim ("cannot reassign ... held by ...";
// pg2-qyc3h), and `bd reclaim` would also rob a live worker whose lease merely
// lapsed (pg2-7gtae), so the release is conditioned on the claim still being
// the observed one: `--if-assignee <holder> --if-status in_progress`. A bead
// that is not in_progress under a named assignee, or that cannot be read, gets
// no guard (the legacy unguarded update, which the claim guard does not refuse
// for an unassigned bead).
func heldGuard(ctx context.Context, r Runner, id string) []string {
	iss, err := ShowObj(ctx, r, id)
	if err != nil || iss.Status != "in_progress" || iss.Assignee == "" {
		return nil
	}
	return []string{"--if-assignee", iss.Assignee, "--if-status", "in_progress"}
}

// UnclaimHeld is Unclaim for a bead whose holder may be another actor (the
// session that held it is gone): `bd update <id> --status=open --assignee=`
// plus the guard flags of heldGuard.
func UnclaimHeld(ctx context.Context, r Runner, id string) error {
	args := append([]string{"update", id, "--status=open", "--assignee="}, heldGuard(ctx, r, id)...)
	if _, err := r.Run(ctx, args...); err != nil {
		slog.Error("release of a dead session's claim failed; the bead keeps its claim",
			"bead", id, "guard_stale", IsGuardStale(err), "err", err)
		return fmt.Errorf("unclaim %s: %w", id, err)
	}
	return nil
}

// ReopenReview re-opens a closed review-pr bead and refreshes its review target:
// `bd update <id> --status=open --set-metadata head_sha=<new> --set-metadata
// branch=<new> --set-metadata ownership=<new> --assignee=`. Overwriting
// head_sha/branch is essential — the review worker checks out
// metadata.head_sha, so reopening WITHOUT refreshing it would re-review the
// same old commit forever (the ACL reopens only on head advance). Refreshing
// ownership matters too (pg2-ynhr.5): the review prompt's mine/co-owned vs.
// team branch reads this metadata, and an ownership transition (e.g. a team PR
// becoming co-owned between review cycles) must not leave a stale value behind
// — mirroring the legacy beadsbridge's EnsureDraftReviewMineLabel handling of
// the same transition. Clearing the assignee returns the bead to the pool for
// a fresh worker. --set-metadata is a per-key merge, so the numeric
// pr_number/repo keys are preserved and MatchReviewPR keeps matching
// repo#number.
func ReopenReview(ctx context.Context, r Runner, id, headSHA, branch, ownership string) error {
	_, err := r.Run(ctx, "update", id, "--status=open",
		"--set-metadata", "head_sha="+headSHA,
		"--set-metadata", "branch="+branch,
		"--set-metadata", "ownership="+ownership,
		"--assignee=")
	if err != nil {
		return fmt.Errorf("reopen review-pr %s: %w", id, err)
	}
	return nil
}

// AddHuman flags a bead for a human: `bd update <id> --add-label human`.
func AddHuman(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--add-label", "human")
	if err != nil {
		return fmt.Errorf("add-human %s: %w", id, err)
	}
	return nil
}

// DefaultParkNote is the comment AddHumanAndUnclaim writes when its caller
// supplied none, so no park of a bead as human is ever silent.
const DefaultParkNote = "Handler park note: this bead was parked as human by the pg-router-ccpool-handler, not by a person; " +
	"no person-question was identified. This is resume-or-land work, not a decision."

// AddHumanAndUnclaim flags a bead for a human AND releases its claim in ONE
// write: `bd update <id> --add-label human --status=open --assignee=`. For a
// session that is gone: the human label keeps the bead out of every retry
// queue, and the cleared assignee keeps it from staying in_progress under a
// session that no longer exists (pg2-0fsuu). One call, so a bd hiccup cannot
// land the label without the release or the reverse.
//
// The park note (why the bead was parked, where its drain worktree is) is
// written first, through the same runner, as a bd comment, so the moment the
// label makes the bead visible to the human queue the explanation is already
// on it (bead pg2-78mxs). An empty note is replaced by DefaultParkNote: the
// park is never silent. A comment that fails is logged and does NOT stop the
// label-and-release update, which is the pg2-0fsuu invariant; a failed update
// is logged too and returned.
func AddHumanAndUnclaim(ctx context.Context, r Runner, id, note string) error {
	return addHumanAndUnclaim(ctx, r, id, note, nil)
}

// AddHumanAndUnclaimHeld is AddHumanAndUnclaim for a bead whose claim may be
// held by ANOTHER actor (the session that held it is gone): the release is
// guarded with `--if-assignee <holder> --if-status in_progress` (see
// heldGuard) so bd's claim guard accepts it. A guard that has gone stale (the
// claim changed hands since the read) writes nothing and is returned; see
// IsGuardStale.
func AddHumanAndUnclaimHeld(ctx context.Context, r Runner, id, note string) error {
	return addHumanAndUnclaim(ctx, r, id, note, heldGuard(ctx, r, id))
}

func addHumanAndUnclaim(ctx context.Context, r Runner, id, note string, guard []string) error {
	if strings.TrimSpace(note) == "" {
		note = DefaultParkNote
	}
	if err := Comment(ctx, r, id, note); err != nil {
		slog.Error("park note not written; parking the bead anyway", "bead", id, "err", err)
	}
	args := append([]string{"update", id, "--add-label", "human", "--status=open", "--assignee="}, guard...)
	if _, err := r.Run(ctx, args...); err != nil {
		slog.Error("park: add human and release failed; the bead keeps its claim",
			"bead", id, "guarded", len(guard) > 0, "guard_stale", IsGuardStale(err), "err", err)
		return fmt.Errorf("add-human-and-unclaim %s: %w", id, err)
	}
	return nil
}

// AddLabel adds an arbitrary label: `bd update <id> --add-label <label>`.
func AddLabel(ctx context.Context, r Runner, id, label string) error {
	_, err := r.Run(ctx, "update", id, "--add-label", label)
	if err != nil {
		return fmt.Errorf("add-label %s %s: %w", id, label, err)
	}
	return nil
}

// RemoveLabel removes an arbitrary label: `bd update <id> --remove-label <label>`.
// Removing a label the bead does not carry is a no-op on bd's side.
func RemoveLabel(ctx context.Context, r Runner, id, label string) error {
	_, err := r.Run(ctx, "update", id, "--remove-label", label)
	if err != nil {
		return fmt.Errorf("remove-label %s %s: %w", id, label, err)
	}
	return nil
}

// HasLabel reads the bead via `bd show <id> --json` and reports whether it
// carries the given label.
func HasLabel(ctx context.Context, r Runner, id, label string) (bool, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return false, err
	}
	return iss.HasLabel(label), nil
}

// unwrapData peels bd's response envelope. bd >=1.0.x wraps every `--json`
// payload as `{"data": <array-or-object>, "schema_version": N}`; older bd (and
// the bash original this was ported from) emitted a bare top-level array/object.
// If b is an envelope carrying a "data" key, its raw value is returned;
// otherwise b is returned unchanged so the legacy bare shapes still decode.
// Decoupling this peel from the array/object decode keeps decodeMany/decodeOne
// tolerant of both the enveloped and bare forms (pg2-ygbt).
func unwrapData(b []byte) []byte {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	// A bare top-level array fails to unmarshal into a struct (err != nil) and is
	// returned as-is. A bare object without a "data" key unmarshals fine but
	// leaves Data empty, so it too is returned unchanged.
	if err := json.Unmarshal(b, &env); err == nil && len(env.Data) > 0 {
		return env.Data
	}
	return b
}

func decodeOne(b []byte) (Issue, error) {
	b = unwrapData(b)
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		if len(arr) > 0 {
			return arr[0], nil
		}
		return Issue{}, nil
	}
	var one Issue
	if err := json.Unmarshal(b, &one); err != nil {
		return Issue{}, fmt.Errorf("decode issue: %w", err)
	}
	return one, nil
}

// Comment adds a comment to a bead: `bd comment <id> <text>`.
func Comment(ctx context.Context, r Runner, id, text string) error {
	_, err := r.Run(ctx, "comment", id, text)
	if err != nil {
		return fmt.Errorf("comment %s: %w", id, err)
	}
	return nil
}

func decodeMany(b []byte) []Issue {
	b = unwrapData(b)
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr
	}
	return nil
}
