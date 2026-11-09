// Package triage is the logic of the pg-rescue triage command role: for one
// pg-rescue deferral item it verifies the live state the item points at
// (read-only, through livestate), comments the outcome on the item and then
// closes it, releases it or hands it to a person. It never mutates the state
// the item points at. This is option A of the in-place consumer design: an
// item in a canonical clone is never acted on, whatever the verifier says.
package triage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-rescue/internal/livestate"
)

// Metadata keys written by pg-rescue-bead at filing time.
const (
	MetaHost      = "pg_rescue_host"
	MetaCwd       = "pg_rescue_cwd"
	MetaState     = "pg_rescue_state"
	MetaStateKind = "pg_rescue_state_kind"
)

// RescueLabel is the label every pg-rescue item carries.
const RescueLabel = "pg-rescue"

// Location classes, matching pg_rescue_state_kind.
const (
	KindCanonicalClone = "canonical-clone"
	KindWorktree       = "worktree"
	KindOtherDir       = "other-dir"
	KindNotARepo       = "not-a-repo"
)

// snapshotPrefix starts the state block pg-rescue-bead appends to a dedup-hit
// comment: the most recent observation.
const snapshotPrefix = "State snapshot at "

// ErrPeerHeld is returned by Tracker.Claim when another actor holds the item
// or it is no longer claimable. It is not a failure of the run.
var ErrPeerHeld = errors.New("item is held by a peer or already closed")

// Item is the part of a tracker item the role reads.
type Item struct {
	ID       string
	Status   string
	Labels   []string
	Metadata map[string]any
}

// Tracker is the tracker the role works against.
type Tracker interface {
	Show(id string) (Item, error)
	Comments(id string) ([]string, error)
	Claim(id, actor string) error
	Comment(id, text string) error
	Close(id, reason string) error
	// Release clears the assignee and reopens the item, unchanged otherwise.
	Release(id string) error
	// HandOver releases the item and labels it for a person (label human).
	HandOver(id string) error
}

// Action is what the role did with the item.
type Action string

// The actions, one per terminal path.
const (
	ActionSkipped  Action = "skipped"   // not a live pg-rescue item; nothing changed
	ActionPeerHeld Action = "peer-held" // could not claim; nothing changed
	ActionClosed   Action = "closed"
	ActionReleased Action = "released"
	ActionHandover Action = "handover"
)

// Outcome is the full result of one run.
type Outcome struct {
	Action Action
	// Verdict is the verifier outcome, or "unverifiable" / "not-a-repo" when
	// the verifier was not run or not decisive.
	Verdict string
	Reason  string
}

// Options configures Run.
type Options struct {
	ItemID string
	Actor  string
	// Host is this machine's name, for the verifier's other-host check.
	Host string
	// Verify is the verifier; nil means livestate.Verify.
	Verify func(path string, snap livestate.Snapshot, opt livestate.Options) livestate.Result
}

// BeadID returns the bare bead id of an item id. pg-router names an event
// "<bead id>@<change id>", and the tracker knows only the bead id, so every
// tracker call MUST use this form; the full id stays for dedup and provenance.
func BeadID(itemID string) string {
	id, _, _ := strings.Cut(itemID, "@")
	return id
}

// Run triages one item. It returns an error only when the tracker failed; a
// claim it took is released on that path, best effort.
func Run(tr Tracker, o Options) (Outcome, error) {
	o.ItemID = BeadID(o.ItemID)
	it, err := tr.Show(o.ItemID)
	if err != nil {
		return Outcome{}, err
	}
	if it.Status == "closed" || !hasLabel(it.Labels, RescueLabel) {
		return Outcome{Action: ActionSkipped, Reason: "not an open pg-rescue item"}, nil
	}
	if err := tr.Claim(o.ItemID, o.Actor); err != nil {
		if errors.Is(err, ErrPeerHeld) {
			return Outcome{Action: ActionPeerHeld, Reason: err.Error()}, nil
		}
		return Outcome{}, err
	}
	out, err := decide(tr, it, o)
	if err == nil {
		err = apply(tr, o.ItemID, out)
	}
	if err != nil {
		_ = tr.Release(o.ItemID)
		return Outcome{}, err
	}
	return out, nil
}

// apply writes the comment first, so the record exists even if the state
// change then fails, then performs the action.
func apply(tr Tracker, id string, out Outcome) error {
	comment := fmt.Sprintf("pg-rescue triage: %s (%s): %s", out.Verdict, out.Action, out.Reason)
	if err := tr.Comment(id, comment); err != nil {
		return err
	}
	switch out.Action {
	case ActionClosed:
		return tr.Close(id, "pg-rescue triage: "+out.Reason)
	case ActionReleased:
		return tr.Release(id)
	default:
		return tr.HandOver(id)
	}
}

func decide(tr Tracker, it Item, o Options) (Outcome, error) {
	kind, _ := it.Metadata[MetaStateKind].(string)
	cwd, _ := it.Metadata[MetaCwd].(string)
	host, _ := it.Metadata[MetaHost].(string)

	snap, ok, err := latestSnapshot(tr, it)
	if err != nil {
		return Outcome{}, err
	}
	snap.Host, snap.Kind = host, kind

	switch {
	case !ok || cwd == "" || !knownKind(kind):
		return handover("unverifiable", "the item has no usable state snapshot, working directory or location class", nil), nil
	case kind == KindNotARepo:
		// No git probe can show it unchanged: verify only the host, then hand over.
		if o.Host != "" && host != "" && o.Host != host {
			return outcomeFor(ActionReleased, "unreachable", fmt.Sprintf("snapshot host %q differs from this host %q", host, o.Host), nil), nil
		}
		return handover("not-a-repo", "location class not-a-repo cannot be verified beyond existence", nil), nil
	}

	verify := o.Verify
	if verify == nil {
		verify = livestate.Verify
	}
	res := verify(cwd, snap, livestate.Options{Host: o.Host})
	switch res.Outcome {
	case livestate.Resolved:
		return outcomeFor(ActionClosed, "resolved", res.Reason, res.Observed), nil
	case livestate.Unreachable:
		return outcomeFor(ActionReleased, "unreachable", res.Reason, res.Observed), nil
	case livestate.Drifted:
		return handover("drifted", res.Reason, res.Observed), nil
	case livestate.Intact:
		// Option A: never act in place. A canonical clone is R-3 territory;
		// the act path for the other classes is a later, separate change.
		why := "state intact; in-place action on a canonical clone is not permitted, handing over with this diagnosis"
		if kind != KindCanonicalClone {
			why = "state intact; the in-place act path is not enabled yet, handing over with this diagnosis"
		}
		return handover("intact", why+": "+res.Reason, res.Observed), nil
	}
	return Outcome{}, fmt.Errorf("verifier returned unknown outcome %q", res.Outcome)
}

type observed = *livestate.Snapshot

func outcomeFor(a Action, verdict, reason string, obs observed) Outcome {
	return Outcome{Action: a, Verdict: verdict, Reason: reason + observedText(obs)}
}

func handover(verdict, reason string, obs observed) Outcome {
	return outcomeFor(ActionHandover, verdict, reason, obs)
}

func observedText(obs observed) string {
	if obs == nil {
		return ""
	}
	b, _ := json.Marshal(obs)
	return " (observed " + string(b) + ")"
}

func knownKind(k string) bool {
	switch k {
	case KindCanonicalClone, KindWorktree, KindOtherDir, KindNotARepo:
		return true
	}
	return false
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// latestSnapshot returns the most recent observation: the last state block in
// the comments, else the item's own pg_rescue_state metadata. ok is false when
// neither parses.
func latestSnapshot(tr Tracker, it Item) (livestate.Snapshot, bool, error) {
	comments, err := tr.Comments(it.ID)
	if err != nil {
		return livestate.Snapshot{}, false, err
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if s, ok := snapshotFromComment(comments[i]); ok {
			return s, true, nil
		}
	}
	raw, _ := it.Metadata[MetaState].(string)
	var s livestate.Snapshot
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return livestate.Snapshot{}, false, nil
	}
	return s, true, nil
}

func snapshotFromComment(text string) (livestate.Snapshot, bool) {
	idx := strings.LastIndex(text, snapshotPrefix)
	if idx < 0 {
		return livestate.Snapshot{}, false
	}
	lines := strings.Split(text[idx:], "\n")
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var s livestate.Snapshot
		if json.Unmarshal([]byte(l), &s) == nil {
			return s, true
		}
		return livestate.Snapshot{}, false
	}
	return livestate.Snapshot{}, false
}
