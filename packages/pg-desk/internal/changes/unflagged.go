package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// MetaUnflaggedPrefix + <instance> holds the JSON UnflaggedRecord of an owning
// instance: ONLY the count and the read time, never a member set.
const MetaUnflaggedPrefix = "change_flow.unflagged."

// BaselineHoldSeconds is how long a query's baseline may stay open before its
// members count as unflagged: one day.
const BaselineHoldSeconds = 86400

// UnflaggedRecord is the value stored under MetaUnflaggedPrefix + <instance>.
type UnflaggedRecord struct {
	Count int    `json:"count"`
	At    string `json:"at"`
}

// ReadUnflagged reads the stored unflagged-member record of an instance.
// found is false before the first successful read; a malformed value is an
// error.
func ReadUnflagged(st *store.Store, instance string) (rec UnflaggedRecord, found bool, err error) {
	v, ok, err := st.GetMeta(MetaUnflaggedPrefix + instance)
	if err != nil || !ok {
		return UnflaggedRecord{}, false, err
	}
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		return UnflaggedRecord{}, false, fmt.Errorf("changes: decode %s%s: %w", MetaUnflaggedPrefix, instance, err)
	}
	return rec, true, nil
}

// MemberReader reads an owning backend's query members and its baseline
// state. *gather.Gatherer satisfies it.
type MemberReader interface {
	ListQueryMembers(ctx context.Context, query, backend string) (gather.MemberList, error)
	BaselineOpenAges(ctx context.Context, entityType, backend string) (map[string]int64, error)
}

// recordUnflagged is the unflagged-member read of one poll: for every owning
// instance whose feed step answered, it lists the members of each of the
// instance's owning watched queries and stores ONLY the count of distinct
// member ids that pg-desk holds with a watched flag that is not true (a member
// pg-desk does not hold is never counted) and the read time. A query whose
// baseline is open for less than BaselineHoldSeconds is skipped. A failed
// baseline or list read, or a truncated list, leaves the previous count
// untouched; a failure is a degraded poll (a side line, hence the partial
// exit), a truncation only a warning. Nothing is stored per member.
func (e *Engine) recordUnflagged(ctx context.Context, entityType string, outcomes []feedOutcome, st *pollState) error {
	if e.Members == nil || entityType != EntityTypeIssue {
		return nil
	}
	for _, o := range outcomes {
		if o.failed {
			continue // the feed step already reported this instance degraded
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("changes: %w", err)
		}
		if err := e.unflaggedInstance(ctx, entityType, o.inst.name, st); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) unflaggedInstance(ctx context.Context, entityType, instance string, st *pollState) error {
	var queries []string
	for _, q := range e.Cfg.OwningWatchQueries(entityType) {
		if b, _ := e.Cfg.WatchQueryBackend(entityType, q); b.Instance == instance {
			queries = append(queries, q)
		}
	}
	if len(queries) == 0 {
		return nil
	}
	degrade := func() {
		for _, q := range queries {
			st.side = append(st.side, "unflagged read degraded: "+q)
		}
	}
	ages, err := e.Members.BaselineOpenAges(ctx, entityType, instance)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("changes: %w", cerr)
		}
		degrade()
		return nil
	}
	all, err := e.Store.ListEntities()
	if err != nil {
		return fmt.Errorf("changes: list entities: %w", err)
	}
	unflagged := map[string]bool{} // held ids of the instance whose watched is not true
	for _, ent := range all {
		if ent.EntityType == entityType && ent.Backend == instance && (ent.Watched == nil || !*ent.Watched) {
			unflagged[ent.EntityID] = true
		}
	}
	counted := map[string]bool{}
	for _, q := range queries {
		if age, open := ages[q]; open && age < BaselineHoldSeconds {
			continue
		}
		list, err := e.Members.ListQueryMembers(ctx, q, instance)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return fmt.Errorf("changes: %w", cerr)
			}
			st.side = append(st.side, "unflagged read degraded: "+q)
			return nil
		}
		if list.Truncated {
			e.warnf("changes: unflagged read truncated: %s (count kept)\n", q)
			return nil
		}
		for _, m := range list.Members {
			if unflagged[m.ID] {
				counted[m.ID] = true
			}
		}
	}
	rec, err := json.Marshal(UnflaggedRecord{Count: len(counted), At: e.now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	if err := e.Store.SetMeta(MetaUnflaggedPrefix+instance, string(rec)); err != nil {
		e.warnf("changes: record unflagged count %s: %v\n", instance, err)
	}
	return nil
}
