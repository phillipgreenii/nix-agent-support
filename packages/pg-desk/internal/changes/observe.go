package changes

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// FlowEntityTypes are the entity types the change flow knows (the keys of
// config.WatchConfig). Observe always reports these, plus any other type it
// finds in the store.
var FlowEntityTypes = []string{"pr", "issue", "thread"}

// RecordCount is the number of change_log records of one (kind, origin)
// pair. A record carrying several kinds counts once under each.
type RecordCount struct {
	Kind   string
	Origin string
	Count  int64
}

// TypeFlow is one entity type's change-flow observability [design 11].
type TypeFlow struct {
	Type string
	// Records counts the retained change_log records by kind and origin,
	// sorted by (kind, origin).
	Records []RecordCount
	// MaxSeq is the highest change_log seq of this type (0 when none).
	MaxSeq int64
	// Active is ActiveCount and Due is DueBacklog at the observation time.
	Active int
	Due    int
	// Stats are the persisted hydration totals (never-nil Degraded map).
	Stats HydrationStats
	// Repeated is RepeatedDegraded(Stats).
	Repeated map[string]DegradedEntity
}

// ConsumerFlow is one registered consumer's position: Lag is the type's
// MaxSeq minus the consumer's Cursor (never negative); SeenAt is its liveness
// stamp (empty when it never read).
type ConsumerFlow struct {
	Name   string
	Type   string
	Cursor int64
	Lag    int64
	SeenAt string
}

// Flow is the whole change-flow observation. Migrated is false on a store
// that is not on the new schema (or has none): the change-flow tables do not
// exist there, so Types and Consumers are empty and the caller degrades
// instead of refusing.
type Flow struct {
	Migrated  bool
	Types     []TypeFlow
	Consumers []ConsumerFlow
}

// Observe reads the change-flow observability from the store at now: the
// single definition /metrics and `status` share. It only reads. maxAge is the
// sweep bound D used for the due backlog.
func Observe(st *store.Store, now time.Time, maxAge time.Duration) (Flow, error) {
	version, err := st.SchemaVersion()
	if err != nil {
		return Flow{}, err
	}
	if version < store.NewSchemaVersion {
		return Flow{}, nil
	}
	entities, err := st.ListEntities()
	if err != nil {
		return Flow{}, err
	}
	consumers, err := st.ListConsumers()
	if err != nil {
		return Flow{}, err
	}

	seen := map[string]bool{}
	var types []string
	addType := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	for _, t := range FlowEntityTypes {
		addType(t)
	}
	var extra []string
	for _, e := range entities {
		if !seen[e.EntityType] {
			extra = append(extra, e.EntityType)
		}
	}
	for _, c := range consumers {
		if !seen[c.Type] {
			extra = append(extra, c.Type)
		}
	}
	sort.Strings(extra)
	for _, t := range extra {
		addType(t)
	}

	flow := Flow{Migrated: true}
	maxSeq := map[string]int64{}
	for _, t := range types {
		records, err := st.ListChangesAfter(t, 0, 0)
		if err != nil {
			return Flow{}, err
		}
		type key struct{ kind, origin string }
		counts := map[key]int64{}
		var top int64
		for _, r := range records {
			if r.Seq > top {
				top = r.Seq
			}
			for _, k := range r.Kinds {
				counts[key{k, r.Origin}]++
			}
		}
		maxSeq[t] = top

		stats, err := ReadHydrationStats(st, t)
		if err != nil {
			return Flow{}, err
		}
		tf := TypeFlow{
			Type:     t,
			MaxSeq:   top,
			Active:   ActiveCount(entities, t),
			Due:      DueBacklog(entities, t, now, maxAge),
			Stats:    stats,
			Repeated: RepeatedDegraded(stats),
		}
		for k, n := range counts {
			tf.Records = append(tf.Records, RecordCount{Kind: k.kind, Origin: k.origin, Count: n})
		}
		sort.Slice(tf.Records, func(i, j int) bool {
			if tf.Records[i].Kind != tf.Records[j].Kind {
				return tf.Records[i].Kind < tf.Records[j].Kind
			}
			return tf.Records[i].Origin < tf.Records[j].Origin
		})
		flow.Types = append(flow.Types, tf)
	}
	for _, c := range consumers {
		lag := maxSeq[c.Type] - c.Cursor
		if lag < 0 {
			lag = 0
		}
		flow.Consumers = append(flow.Consumers, ConsumerFlow{
			Name: c.Name, Type: c.Type, Cursor: c.Cursor, Lag: lag, SeenAt: c.SeenAt,
		})
	}
	return flow, nil
}

// SweepInputs are the inputs of the sweep sizing bound [design 8.4] for one
// type, as `status` reports them: the bound itself needs the router's poll
// interval, which pg-desk does not own, so only `doctor --router-config`
// evaluates it.
type SweepInputs struct {
	ActiveCount int
	MaxPerPoll  int
	MaxAge      time.Duration
}

// SweepInputsFor returns the bound's inputs for one observed type.
func SweepInputsFor(cfg *config.Config, tf TypeFlow) SweepInputs {
	return SweepInputs{ActiveCount: tf.Active, MaxPerPoll: cfg.SweepMaxPerPoll(), MaxAge: cfg.SweepMaxAge()}
}
