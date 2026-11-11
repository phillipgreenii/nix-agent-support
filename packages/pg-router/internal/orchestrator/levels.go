package orchestrator

import (
	"slices"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// RegisterLevels hands the queue the compiled requirements it admits against:
// one requirement per role listener (the listener id is the role name) and, per
// polled query, its consumers (the roles that bind an event type the query
// emits, each with its requirement and whether it is enabled). A query that
// is a level source or the timer has no consumer, and the queue never blocks it.
// Call it once, after the roles are known and before the first dispatch pass.
//
// Exported names: Orchestrator.RegisterLevels.
func (o *Orchestrator) RegisterLevels(q *eventqueue.Queue) {
	for _, r := range o.Reg {
		q.RegisterListener(r.Name, o.Cfg.Requirements[r.Name].Requirement)
	}
	for _, src := range o.Cfg.Queries {
		if src.Query == nil || src.LevelSource != "" || query.IsTimer(src.Query) {
			continue
		}
		emits := src.Query.Emits()
		var consumers []levels.Consumer
		for _, r := range o.Reg {
			if !slices.ContainsFunc(r.Binds, func(b string) bool { return slices.Contains(emits, b) }) {
				continue
			}
			consumers = append(consumers, levels.Consumer{Req: o.Cfg.Requirements[r.Name].Requirement, Enabled: r.Enabled})
		}
		q.RegisterEmitter(src.Name, consumers)
	}
}
