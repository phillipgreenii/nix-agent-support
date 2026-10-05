package rules

import (
	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const nodeIDRuleAdoption = "adoption.node-id"

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalAdoptionNodeID, nodeIDAdoptionRule{})
}

// nodeIDAdoptionRule is the node_id adoption backfill: a linked work item
// whose dedup_key still names the PR by its <repo>#<n> id gets that key
// rewritten to the node_id form, so a later repo rename or transfer does not
// orphan the item. It sits right after the adoption rule, which writes the
// keys this rule upgrades.
//
// The rewrite changes only the key's identity segment: the kind and the kind's
// context suffix are kept exactly. It applies to every keyed item, open or
// closed, human-parked or not, and the action writes the dedup_key metadata
// and nothing else (no status or label change). An item already keyed by
// node_id, an item with no (well-formed) dedup_key, and a snapshot with no
// node_id yield no action, so the rule is idempotent.
type nodeIDAdoptionRule struct{}

func (nodeIDAdoptionRule) ID() string          { return nodeIDRuleAdoption }
func (nodeIDAdoptionRule) Kind() workitem.Kind { return "" }

func (nodeIDAdoptionRule) Evaluate(in decide.Input) decide.Result {
	nodeID := in.View.Snapshot.NodeID
	if nodeID == "" {
		return anchorSkip(action.ReasonNotMatched, nil)
	}
	var acts []action.Action
	// Link order, the same classification BuildIndex applies: a work item is an
	// "issue" link whose dedup_key parses.
	for _, l := range in.View.Links {
		if l.Type != "issue" {
			continue
		}
		old := l.Metadata["dedup_key"]
		typ, ident, kind, suffix, ok := workitem.ParseKey(old)
		if !ok || ident == nodeID {
			continue
		}
		key := typ + ":" + nodeID + ":" + string(kind) + suffix
		acts = append(acts, action.Action{
			Op: action.OpUpdate, Kind: string(kind), Target: anchorTarget(l.ID),
			Fields: action.Fields{Metadata: map[string]string{"dedup_key": key}},
			Facts:  map[string]any{"from": old, "dedup_key": key},
		})
	}
	if len(acts) == 0 {
		return anchorSkip(action.ReasonNotMatched, nil)
	}
	return decide.Result{Actions: acts}
}
