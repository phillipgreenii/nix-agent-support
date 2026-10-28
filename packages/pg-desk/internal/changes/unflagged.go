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

// recordListedUnflagged is the beads (listed, non-owning) half of the
// unflagged-member count: for every instance named by a non-owning
// watch.issue.backends entry it stores, in the same
// change_flow.unflagged.<instance> record the owning half uses, the count of
// distinct members of the persisted watch sets (the last COMPLETE listing) of
// the instance's queries that pg-desk holds as an active entity whose watched
// flag is not true. A member is matched to its instance by membership of the
// query's persisted set, so an entity whose backend column is NULL still
// counts. A query whose baseline is open (no marker) for less than
// BaselineHoldSeconds is excluded, and a member pg-desk does not hold is never
// counted. An instance is read only when every one of its queries was consulted
// by this call (a --query call that skipped one would otherwise drop its
// members), and a baseline read error keeps the previous count and degrades the
// poll. Nothing is stored per member.
func (e *Engine) recordListedUnflagged(entityType string, selected []string, st *pollState) {
	if entityType != issueEntityType || e.Cfg == nil {
		return
	}
	byInstance := map[string][]string{}
	var instances []string
	for _, q := range e.Cfg.NonOwningWatchQueries(entityType) {
		b, ok := e.Cfg.WatchQueryBackend(entityType, q)
		if !ok || b.Instance == "" {
			continue
		}
		if _, seen := byInstance[b.Instance]; !seen {
			instances = append(instances, b.Instance)
		}
		byInstance[b.Instance] = append(byInstance[b.Instance], q)
	}
	if len(instances) == 0 {
		return
	}
	consulted := map[string]bool{}
	for _, q := range selected {
		consulted[q] = true
	}
	var held map[string]store.Entity // loaded once, only when an instance needs it
	now := e.now().UTC()
	for _, inst := range instances {
		queries := byInstance[inst]
		all := true
		for _, q := range queries {
			all = all && consulted[q]
		}
		if !all {
			continue
		}
		members := map[string]bool{}
		degraded := false
		for _, q := range queries {
			bs, err := ReadBaseline(e.Store, q)
			if err != nil {
				st.side = append(st.side, "unflagged read degraded: "+q)
				degraded = true
				break
			}
			// A baseline that has begun and not finished is excluded until it has
			// been open one day; one with no readable open time is still in
			// progress, so it is excluded as well.
			if !bs.Done && (!bs.HasOpenSince || now.Sub(bs.OpenSince) < BaselineHoldSeconds*time.Second) {
				continue
			}
			for id := range loadWatchSet(e.Store, entityType, q) {
				members[id] = true
			}
		}
		if degraded {
			continue
		}
		if held == nil {
			rows, err := e.Store.ListEntities()
			if err != nil {
				st.side = append(st.side, "unflagged read degraded: list entities: "+firstErrLine(err.Error()))
				return
			}
			held = make(map[string]store.Entity, len(rows))
			for _, ent := range rows {
				if ent.EntityType == entityType {
					held[ent.EntityID] = ent
				}
			}
		}
		count := 0
		for id := range members {
			// An inactive entity ended its watch on purpose (a closed bead's flag
			// ends with its deactivation), so it is not an unflagged member.
			if ent, ok := held[id]; ok && !ent.Inactive && (ent.Watched == nil || !*ent.Watched) {
				count++
			}
		}
		rec, err := json.Marshal(UnflaggedRecord{Count: count, At: now.Format(time.RFC3339)})
		if err != nil {
			continue
		}
		if err := e.Store.SetMeta(MetaUnflaggedPrefix+inst, string(rec)); err != nil {
			e.warnf("changes: record unflagged count %s: %v\n", inst, err)
		}
	}
}
