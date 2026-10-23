package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Awaiting is one group a pending row waits for: a fetch of Group counts only
// when it started at or after Since.
type Awaiting struct {
	Group string    `json:"group"`
	Since time.Time `json:"since"`
}

// DecodeAwaited reads the Awaited column of a pending row; an empty or null
// document is no awaited groups.
func DecodeAwaited(raw json.RawMessage) ([]Awaiting, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var aw []Awaiting
	if err := json.Unmarshal(raw, &aw); err != nil {
		return nil, fmt.Errorf("engine: decode awaited groups: %w", err)
	}
	return aw, nil
}

func encodeAwaited(aw []Awaiting) (json.RawMessage, error) {
	if len(aw) == 0 {
		return json.RawMessage("[]"), nil
	}
	raw, err := json.Marshal(aw)
	if err != nil {
		return nil, fmt.Errorf("engine: encode awaited groups: %w", err)
	}
	return raw, nil
}

// groupRank orders awaited groups the way Spec.Groups lists them.
func (e *Engine) groupRank(g string) int {
	if i := slices.Index(e.spec.Groups, g); i >= 0 {
		return i
	}
	return len(e.spec.Groups)
}

// await returns aw with every group of queue awaited since now: a group already
// awaited gets the new Since, a new one is added. The result is in Spec.Groups
// order.
func (e *Engine) await(aw []Awaiting, queue []string, now time.Time) []Awaiting {
	out := slices.Clone(aw)
	for _, g := range queue {
		if i := slices.IndexFunc(out, func(a Awaiting) bool { return a.Group == g }); i >= 0 {
			out[i].Since = now
			continue
		}
		out = append(out, Awaiting{Group: g, Since: now})
	}
	slices.SortStableFunc(out, func(a, b Awaiting) int { return e.groupRank(a.Group) - e.groupRank(b.Group) })
	return out
}
