package focus

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Live facts and the bead column of a focus table row, read from the store
// with no tracker call: the source entity's own snapshot, annotations and
// links, and the bead the store links to it by relation source (a minted
// bead names its source in its own metadata, so the entity view of the
// source carries it as a derived link, D-F9, D-F13).
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is a pure function of Inputs.

// relationSource is the link relation from a minted bead to its source.
const relationSource = "source"

// metaFocusHold is the bead metadata key of the decider's hold marker, read
// to classify a bead and never written here.
const metaFocusHold = "focus_hold"

// humanLabel marks an escalation bead.
const humanLabel = "human"

// BeadCell is one row's bead column: the table row it falls in and the shown
// text.
type BeadCell struct {
	State BeadState
	Shown string
}

// Live is the live view of a set of keys: the facts, the bead column and the
// key an absorbed entity was absorbed into.
type Live struct {
	Facts  LiveFacts
	Beads  map[Key]BeadCell
	Absorb map[Key]Key
}

// LiveOptions carry what BuildLive cannot read from Inputs.
type LiveOptions struct {
	// Watched reports which sources are outside every watch query.
	Watched Watched
	// SelectedAt is when each plan row was selected, for the pending age.
	SelectedAt map[Key]time.Time
}

// entityFacts are the facts of one stored entity the live view reads.
type entityFacts struct {
	title, state, assignee string
	labels                 []string
	metadata               map[string]json.RawMessage
	parent                 string
	dueDate, priority      string
	merged                 bool
}

func decodeEntityFacts(e store.Entity) entityFacts {
	var v view
	v.key = Key{e.EntityType, e.EntityID}
	decodeFacts(e.Facts, &v)
	ef := entityFacts{
		state: v.state, assignee: v.assignee, labels: v.labels, metadata: v.metadata,
		parent: v.parent, dueDate: v.dueDate, priority: v.priority, merged: v.merged,
	}
	var f struct {
		PRShow struct {
			Title string `json:"title"`
		} `json:"pr_show"`
		IssueShow struct {
			Title string `json:"title"`
		} `json:"issue_show"`
	}
	if json.Unmarshal([]byte(e.Facts), &f) == nil {
		if f.PRShow.Title != "" {
			ef.title = f.PRShow.Title
		} else {
			ef.title = f.IssueShow.Title
		}
	}
	return ef
}

func metaString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}

// openStatus reports whether an issue status counts as an open child of an
// epic: anything that is not closed.
func openStatus(state string) bool {
	s := strings.ToLower(strings.TrimSpace(state))
	return s != "" && s != "closed" && s != "done"
}

// BuildLive builds the live facts and bead column of keys from in. fresh is
// the ranking of this run: a key it holds takes its effective due date and
// priority (a correlation group's), any other key its own values.
func BuildLive(in Inputs, keys []Key, fresh Ranking, opt LiveOptions) Live {
	live := Live{Facts: LiveFacts{}, Beads: map[Key]BeadCell{}, Absorb: map[Key]Key{}}
	decoded := make(map[Key]entityFacts, len(in.Entities))
	hasOpenChild := map[string]bool{}
	for k, e := range in.Entities {
		ef := decodeEntityFacts(e)
		decoded[k] = ef
		if k.Type == entityTypeIssue && ef.parent != "" && !e.Inactive && openStatus(ef.state) {
			hasOpenChild[ef.parent] = true
		}
	}
	// Minted beads by source key, in id order.
	minted := map[Key][]Key{}
	for _, l := range in.Links {
		if l.Relation != relationSource || l.FromType != entityTypeIssue {
			continue
		}
		src := Key{l.ToType, l.ToID}
		minted[src] = append(minted[src], Key{l.FromType, l.FromID})
	}
	for _, ids := range minted {
		sort.Slice(ids, func(i, j int) bool { return keyLess(ids[i], ids[j]) })
	}
	freshRow := make(map[Key]RankedRow, len(fresh.Rows))
	for _, r := range fresh.Rows {
		freshRow[r.Key] = r
	}
	beadRe := in.Config.BeadIDRegexp()
	suppress := ""
	if kind := in.Config.FocusSuppressedKind(); kind != "" {
		suppress = store.KeySuppress(kind)
	}

	for _, k := range keys {
		e, stored := in.Entities[k]
		ef := decoded[k]
		f := Facts{Title: ef.title, State: ef.state, Stale: e.Stale}
		if !stored {
			f.Inactive = true
		}
		f.Inactive = f.Inactive || e.Inactive
		anns := in.Annotations[k]
		f.Hidden = hidden(anns)
		if ef.merged {
			f.State = "merged"
		}
		if stored && (ef.merged || classify.SourceTerminal(k.Type, json.RawMessage(e.Facts), in.Now, 0)) {
			f.Finished = true
			f.Outcome = outcomeOf(k.Type, ef)
		}
		f.Due, f.Priority = ef.dueDate, ef.priority
		if len(f.Due) > 10 {
			f.Due = f.Due[:10]
		}
		if r, ok := freshRow[k]; ok {
			f.Due, f.Priority = r.Due, r.Priority
		}
		isBead := k.Type == entityTypeIssue && beadRe != nil && beadRe.MatchString(k.ID)

		// The bead this source stands for: itself when it is a bead, else
		// the minted bead linked to it by relation source.
		var link LinkedBead
		escalated := false
		target := ef
		targetPresent := isBead && stored
		if !isBead {
			for _, bk := range minted[k] {
				bf, ok := decoded[bk]
				if !ok {
					continue
				}
				if hasLabel(bf.labels, humanLabel) {
					escalated = true
					continue
				}
				if !targetPresent {
					target, targetPresent = bf, true
					link = LinkedBead{
						Present: true, Status: bf.state, Assignee: bf.assignee,
						FocusHold:    metaString(bf.metadata, metaFocusHold),
						OpenChildren: hasOpenChild[bk.ID],
					}
				}
			}
		}
		if targetPresent {
			st := strings.ToLower(strings.TrimSpace(target.state))
			if st == statusInProg || strings.TrimSpace(target.assignee) != "" {
				f.Claimant = strings.TrimSpace(target.assignee)
				if f.Claimant == "" {
					f.Claimant = "in_progress"
				}
			}
			if st == statusDeferred {
				f.Deferred = true
			}
		}
		if !link.Present {
			if at, ok := opt.SelectedAt[k]; ok && !at.IsZero() && in.Now.After(at) {
				link.PendingAge = in.Now.Sub(at)
			}
		}
		flags := SourceFlags{
			Hidden:              f.Hidden,
			SuppressedFocusKind: suppress != "" && anns[suppress] == "true",
			Terminal:            f.Finished,
			IsBeadItself:        isBead,
			Unwatched:           opt.Watched.Unwatched(k),
			Escalated:           escalated,
		}
		state := ClassifyBead(link, flags)
		live.Beads[k] = BeadCell{State: state, Shown: ShownAs(state, link, flags)}
		live.Facts[k] = f
		if by, ok := in.Absorbed[k]; ok {
			live.Absorb[k] = by
		}
	}
	return live
}

// outcomeOf names how a finished source finished.
func outcomeOf(entityType string, ef entityFacts) string {
	if entityType == entityTypePR {
		if ef.merged || strings.EqualFold(ef.state, "merged") {
			return "merged"
		}
		return "closed"
	}
	if s := strings.ToLower(strings.TrimSpace(ef.state)); s != "" {
		return s
	}
	return "done"
}
