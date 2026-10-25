package changes

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// classifier is the Jira implementation of engine.Classifier: it wraps the pure
// rules of classify and groups over the stored summary and group content.
type classifier struct{}

var _ engine.Classifier = classifier{}

// Describe decodes the stored summary content (the leaf's Describer).
func (classifier) Describe(content []byte) (engine.Primary, error) {
	return groups.Describer{}.Describe(content)
}

// Primary classifies a summary change and names the groups it stales. A prev
// that does not decode is a baseline: the zero result and no dependents.
func (classifier) Primary(prev, next []byte, bodyCap int) (engine.Result, []string, error) {
	var p, n groups.SummaryContent
	if err := json.Unmarshal(prev, &p); err != nil {
		return engine.Result{}, nil, nil
	}
	if err := json.Unmarshal(next, &n); err != nil {
		return engine.Result{}, nil, fmt.Errorf("changes: decode summary: %w", err)
	}
	res, dependents := classify.Summary(p, n, bodyCap)
	return res, dependents, nil
}

// Group classifies one non-summary group: the detail group.
func (classifier) Group(name string, prev, next []byte, bodyCap int) (engine.Result, error) {
	if name != groups.Detail {
		return engine.Result{}, fmt.Errorf("changes: group %q is not classified", name)
	}
	var p, n groups.DetailContent
	if err := json.Unmarshal(prev, &p); err != nil {
		return engine.Result{}, fmt.Errorf("changes: decode previous detail: %w", err)
	}
	if err := json.Unmarshal(next, &n); err != nil {
		return engine.Result{}, fmt.Errorf("changes: decode detail: %w", err)
	}
	return classify.Detail(p, n, bodyCap), nil
}

// Counts reports whether a fetch of fetched's group satisfies the awaited group
// a: it must be the awaited group and have started at or after the group's
// Since. No group of an issue is stamped, so the pending row's stamps are not
// compared.
func (classifier) Counts(a engine.Awaiting, fetched store.GroupRow, _ store.PendingChange, started time.Time) bool {
	return a.Group == fetched.Group && !started.Before(a.Since)
}
