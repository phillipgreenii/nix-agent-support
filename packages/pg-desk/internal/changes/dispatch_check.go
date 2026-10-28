package changes

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// MetaDispatchCheckPrefix + <instance> holds the JSON
// {"at":"<RFC3339>","mismatches":["<text>"]} of the latest start-up dispatch
// check of that owning backend instance (an empty list when it agreed).
const MetaDispatchCheckPrefix = "change_flow.dispatch_check."

// The kinds of a DispatchMismatch.
const (
	// MismatchInstanceUnanswered: the backend instance gave no capabilities
	// row (its call failed or the answer named no such backend).
	MismatchInstanceUnanswered = "instance_unanswered"
	// MismatchOwnsChangesDiffers: the backend's owns_changes declaration is
	// not the configured value (an owning entry means true).
	MismatchOwnsChangesDiffers = "owns_changes_differs"
	// MismatchQueryMissing: a configured query is absent from the backend's
	// queries.
	MismatchQueryMissing = "query_missing"
	// MismatchWatchedDiffers: a configured watched query is reported
	// unwatched by the backend.
	MismatchWatchedDiffers = "watched_differs"
)

// Capabilityer reads the umbrella's capabilities answer for one backend.
// *gather.Gatherer satisfies it.
type Capabilityer interface {
	Capabilities(ctx context.Context, entityType, backend string) (gather.CapabilitiesAnswer, error)
}

// DispatchMismatch is one disagreement between pg-desk's dispatch
// configuration and what a backend reports about itself.
type DispatchMismatch struct {
	Kind     string
	Instance string
	Query    string
	Detail   string
}

// String is the one-line text of the mismatch (the warning line, the meta
// record and the doctor line carry it).
func (m DispatchMismatch) String() string {
	s := m.Kind + ": backend " + m.Instance
	if m.Query != "" {
		s += " query " + m.Query
	}
	if m.Detail != "" {
		s += ": " + m.Detail
	}
	return s
}

// CheckDispatch compares the configured per-query backend dispatch of every
// issue instance named by watch.issue.backends with the capabilities answer of
// that backend: the answer is the check at start-up of the configuration. An
// OWNING instance (cfg.OwningInstances("issue") and its OwningWatchQueries) is
// also checked for its owns_changes declaration; a non-owning (beads) instance
// (cfg.NonOwningInstances("issue") and its NonOwningWatchQueries) is checked
// for its queries only, since it declares no owns_changes. A configured query
// is a watched query, so a backend that reports it unwatched, or does not list
// it, is a mismatch. Mismatches come back sorted by instance
// then query. An error from Capabilities is an instance_unanswered mismatch
// (its text in Detail); a caller that must tell an unavailable daemon apart
// wraps the Capabilityer.
func CheckDispatch(ctx context.Context, cfg *config.Config, g Capabilityer) []DispatchMismatch {
	var out []DispatchMismatch
	for _, inst := range cfg.OwningInstances(issueEntityType) {
		out = append(out, checkInstanceDispatch(ctx, cfg, g, inst, true)...)
	}
	for _, inst := range cfg.NonOwningInstances(issueEntityType) {
		out = append(out, checkInstanceDispatch(ctx, cfg, g, inst, false)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Query < out[j].Query
	})
	return out
}

func checkInstanceDispatch(ctx context.Context, cfg *config.Config, g Capabilityer, inst string, owning bool) []DispatchMismatch {
	ans, err := g.Capabilities(ctx, issueEntityType, inst)
	if err != nil {
		return []DispatchMismatch{{Kind: MismatchInstanceUnanswered, Instance: inst, Detail: err.Error()}}
	}
	b, ok := ans.Backend(inst)
	if !ok {
		detail := "the capabilities answer has no row for it"
		if r := ans.Reason(inst); r != "" {
			detail += " (" + r + ")"
		}
		return []DispatchMismatch{{Kind: MismatchInstanceUnanswered, Instance: inst, Detail: detail}}
	}
	var out []DispatchMismatch
	if owning && !b.OwnsChanges() {
		out = append(out, DispatchMismatch{
			Kind: MismatchOwnsChangesDiffers, Instance: inst,
			Detail: "configured owns_changes=true, the backend reports owns_changes=false",
		})
	}
	queries := cfg.OwningWatchQueries(issueEntityType)
	if !owning {
		queries = cfg.NonOwningWatchQueries(issueEntityType)
	}
	for _, q := range queries {
		if wb, _ := cfg.WatchQueryBackend(issueEntityType, q); wb.Instance != inst {
			continue
		}
		cq, found := b.Queries[q]
		switch {
		case !found:
			out = append(out, DispatchMismatch{
				Kind: MismatchQueryMissing, Instance: inst, Query: q,
				Detail: "the configured query is absent from the backend's queries",
			})
		case !cq.Watched:
			out = append(out, DispatchMismatch{
				Kind: MismatchWatchedDiffers, Instance: inst, Query: q,
				Detail: "configured as a watched query, the backend reports watched=false",
			})
		}
	}
	return out
}

// startupDispatchOnce makes the start-up dispatch check run at most once per
// process.
var (
	startupDispatchMu   sync.Mutex
	startupDispatchDone bool
)

// ResetStartupDispatchCheck re-arms the once-per-process start-up dispatch
// check. It exists for tests that run `issue changes` more than once in one
// process.
func ResetStartupDispatchCheck() {
	startupDispatchMu.Lock()
	startupDispatchDone = false
	startupDispatchMu.Unlock()
}

// takeStartupDispatchCheck reports whether the caller is the first of this
// process (and marks the check done).
func takeStartupDispatchCheck() bool {
	startupDispatchMu.Lock()
	defer startupDispatchMu.Unlock()
	if startupDispatchDone {
		return false
	}
	startupDispatchDone = true
	return true
}

// startupDispatchCheck is the first-poll-of-a-process dispatch check: for an
// issue poll with configured backend instances (owning or not) and a
// Capabilities reader, it runs CheckDispatch, writes each mismatch as a warning
// line and stores the outcome of every checked instance under
// MetaDispatchCheckPrefix + instance.
// It never fails the poll; a write failure is a warning too.
func (e *Engine) startupDispatchCheck(ctx context.Context, entityType string) {
	if entityType != issueEntityType || e.Capabilities == nil {
		return
	}
	insts := append(e.Cfg.OwningInstances(issueEntityType), e.Cfg.NonOwningInstances(issueEntityType)...)
	if len(insts) == 0 || !takeStartupDispatchCheck() {
		return
	}
	mismatches := CheckDispatch(ctx, e.Cfg, e.Capabilities)
	byInst := map[string][]string{}
	for _, m := range mismatches {
		e.warnf("warning: dispatch check: %s\n", m)
		byInst[m.Instance] = append(byInst[m.Instance], m.String())
	}
	at := e.now().UTC().Format(time.RFC3339)
	for _, inst := range insts {
		texts := byInst[inst]
		if texts == nil {
			texts = []string{}
		}
		body, err := json.Marshal(struct {
			At         string   `json:"at"`
			Mismatches []string `json:"mismatches"`
		}{At: at, Mismatches: texts})
		if err == nil {
			err = e.Store.SetMeta(MetaDispatchCheckPrefix+inst, string(body))
		}
		if err != nil {
			e.warnf("warning: dispatch check: record the outcome of %s: %v\n", inst, err)
		}
	}
}
