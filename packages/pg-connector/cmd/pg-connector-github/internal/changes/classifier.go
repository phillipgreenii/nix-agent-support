package changes

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/revalidate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// classifier is the GitHub implementation of engine.Classifier: it wraps the
// pure rules of classify and revalidate over the stored summary and group
// content.
type classifier struct{}

var _ engine.Classifier = classifier{}

// Describe decodes the stored summary content (the leaf's Describer).
func (classifier) Describe(content []byte) (engine.Primary, error) {
	return groups.Describer{}.Describe(content)
}

// Primary classifies a summary change and names the groups it stales. The facts
// the persister knew when it stored the summary are re-derived from the two
// stored contents: a stored summary carries its mergeability already carried
// forward (entity.Carry) and a summary row records the head and base it describes, so
// the known-to-known mergeability move, the fingerprints and the baseline flag
// follow from prev and next. A prev that does not decode is a baseline.
func (classifier) Primary(prev, next []byte, bodyCap int) (engine.Result, []string, error) {
	var p, n summary.Summary
	if err := json.Unmarshal(prev, &p); err != nil {
		return engine.Result{}, nil, nil
	}
	if err := json.Unmarshal(next, &n); err != nil {
		return engine.Result{}, nil, fmt.Errorf("changes: decode summary: %w", err)
	}
	_, moved := entity.Carry(p.Mergeable, n.Mergeable, p.HeadRefOid != n.HeadRefOid, p.BaseRefOid != n.BaseRefOid)
	st := summary.Stored{
		Summary: n, Fingerprint: n.Fingerprint(), PrevFingerprint: p.Fingerprint(),
		MergeabilityChanged: moved, Prev: &p,
	}
	if moved {
		st.PrevMergeable = p.Mergeable
	}
	return classify.Summary(&p, st, bodyCap), revalidate.Decide(&p, st).Queue, nil
}

// Group classifies one non-summary group.
func (classifier) Group(name string, prev, next []byte, bodyCap int) (engine.Result, error) {
	return classify.Group(name, prev, next, bodyCap)
}

// Counts reports whether a fetch of next's group satisfies the awaited group a:
// it must have started at or after the group's Since and, for the stamped
// groups, was fetched for the head and base the pending row records.
func (classifier) Counts(a engine.Awaiting, fetched store.GroupRow, pending store.PendingChange, started time.Time) bool {
	if a.Group != fetched.Group || started.Before(a.Since) {
		return false
	}
	if !stamped(fetched.Group) {
		return true
	}
	fa, fb := fetched.Stamps()
	pa, pb := pending.Stamps()
	return fa == pa && fb == pb
}

// stamped reports whether the group's rows record the head and base they were
// fetched for (the summary rules' Stamped groups).
func stamped(group string) bool {
	return group == groups.Files || group == groups.Commits || group == groups.Runs
}
