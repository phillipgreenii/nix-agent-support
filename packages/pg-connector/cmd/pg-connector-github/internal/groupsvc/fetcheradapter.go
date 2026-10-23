package groupsvc

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// fetcherAdapter is the Register boundary: it wraps a GitHub groups.Fetcher as
// the shared groupsvc.Fetcher. The GitHub fetchers keep their own Target (node
// id, entity.Ref, head and base stamps, head branch, state), Outcome and
// ratefeed.Meta; this adapter converts both ways.
type fetcherAdapter struct{ f groups.Fetcher }

var _ sharedsvc.Fetcher = fetcherAdapter{}

func (a fetcherAdapter) Group() string { return a.f.Group() }
func (a fetcherAdapter) MaxBatch() int { return a.f.MaxBatch() }
func (a fetcherAdapter) Spend(class governor.Class, n int) governor.Request {
	return a.f.Spend(class, n)
}

// Fetch converts the targets, fetches and converts the outcomes back. A target
// whose non-empty reference does not parse is not fetched: its outcome is the
// error "groupsvc: <node>: <err>". An empty reference becomes the zero
// entity.Ref.
func (a fetcherAdapter) Fetch(ctx context.Context, call sharedsvc.CallMeta, targets []sharedsvc.Target) []sharedsvc.Outcome {
	out := make([]sharedsvc.Outcome, len(targets))
	var (
		converted []groups.Target
		index     []int // index[k] is the position in targets of converted[k]
	)
	for i, t := range targets {
		var ref entity.Ref
		if t.Ref != "" {
			r, err := entity.ParseRef(t.Ref)
			if err != nil {
				out[i] = sharedsvc.Outcome{Err: fmt.Errorf("groupsvc: %s: %w", t.ID, err)}
				continue
			}
			ref = r
		}
		converted = append(converted, targetOf(t, ref))
		index = append(index, i)
	}
	if len(converted) == 0 {
		return out
	}
	res := a.f.Fetch(ctx, ratefeed.Meta(call), converted)
	for k, i := range index {
		if k >= len(res) {
			// The fetcher answered fewer targets than it was given: the shared
			// executor fails the missing tail.
			return out[:i]
		}
		out[i] = outcomeOf(res[k])
	}
	return out
}

// targetOf converts one shared Target; ref is its parsed reference.
func targetOf(t sharedsvc.Target, ref entity.Ref) groups.Target {
	return groups.Target{
		NodeID:      t.ID,
		Ref:         ref,
		Stamps:      groups.Stamps{Head: t.Stamp, Base: t.Stamp2},
		HeadRefName: t.Attrs[groups.AttrHeadRef],
		State:       t.Attrs[groups.AttrState],
		Terminal:    t.Terminal,
	}
}

// outcomeOf converts one GitHub Outcome to the shared one.
func outcomeOf(o groups.Outcome) sharedsvc.Outcome {
	return sharedsvc.Outcome{
		Content: o.Content, Fingerprint: o.Fingerprint,
		Stamp: o.Stamps.Head, Stamp2: o.Stamps.Base,
		FreshUntil: o.FreshUntil, LastError: o.LastError, Cost: o.Cost, Err: o.Err,
	}
}
