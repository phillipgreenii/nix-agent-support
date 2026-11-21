package focus

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Load reads the store into the pure Inputs the candidate computation takes.
// It only reads: no tracker is called, no clock is read (now is the caller's
// reading), nothing is written. An unreadable store, or an undecodable hidden
// annotation, is an error, never an empty set that reads as "all clear".
//
// Telemetry: Load emits no OpenTelemetry or Prometheus signal and logs
// nothing; the verbs that call it own the run record and the stderr line.
func Load(ctx context.Context, st *store.Store, cfg *config.Config, repo string, now time.Time) (Inputs, error) {
	return load(ctx, st, cfg, repo, now, st.ListEntities)
}

// LoadForHealth is Load for the health computation: each entity's Facts is
// the pruned document store.ListEntitiesForFocus returns (only the members the
// focus decoders read), so the load reads no facts overflow pages. The
// comments of an issue are not in it, so a verb that reads them (focus
// explain) uses Load.
func LoadForHealth(ctx context.Context, st *store.Store, cfg *config.Config, repo string, now time.Time) (Inputs, error) {
	if st == nil {
		return Inputs{}, fmt.Errorf("focus: load: no store")
	}
	return load(ctx, st, cfg, repo, now, st.ListEntitiesForFocus)
}

func load(ctx context.Context, st *store.Store, cfg *config.Config, repo string, now time.Time, listEntities func(context.Context) ([]store.Entity, error)) (Inputs, error) {
	if st == nil {
		return Inputs{}, fmt.Errorf("focus: load: no store")
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := st.RequireNewSchema(ctx); err != nil {
		return Inputs{}, err
	}
	in := Inputs{
		Repo:        repo,
		Now:         now,
		Config:      cfg,
		Entities:    map[Key]store.Entity{},
		Interps:     map[Key]store.Interpretation{},
		Annotations: map[Key]map[string]string{},
		PlanKeys:    map[Key]bool{},
		Absorbed:    map[Key]Key{},
	}

	ents, err := listEntities(ctx)
	if err != nil {
		return Inputs{}, fmt.Errorf("focus: load entities: %w", err)
	}
	for _, e := range ents {
		if e.Repo != repo {
			continue
		}
		in.Entities[Key{e.EntityType, e.EntityID}] = e
	}

	interps, err := st.ListInterpretations(ctx)
	if err != nil {
		return Inputs{}, fmt.Errorf("focus: load interpretations: %w", err)
	}
	for _, ip := range interps {
		if ip.Repo != repo {
			continue
		}
		in.Interps[Key{ip.EntityType, ip.EntityID}] = ip
	}

	// Every link leaving a stored entity: a link between two stored entities
	// is found from its source, so both directions are derivable from this
	// list and none is read twice. A link from an entity the store has never
	// gathered can yield nothing (a seed must be stored), so it is not read.
	// Two repo-wide reads, grouped here by entity, replace one annotation and
	// one link statement per stored entity (a store of 2,700 entities took
	// about 5,400 statements per focus computation).
	allAnns, err := st.ListKVAnnotationsForRepo(ctx, repo)
	if err != nil {
		return Inputs{}, fmt.Errorf("focus: load annotations: %w", err)
	}
	byEntity := map[Key][]store.KVAnnotation{}
	for _, a := range allAnns {
		k := Key{a.EntityType, a.EntityID}
		if _, ok := in.Entities[k]; ok {
			byEntity[k] = append(byEntity[k], a)
		}
	}
	keys := make([]Key, 0, len(byEntity))
	for k := range byEntity {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	for _, k := range keys {
		m := make(map[string]string, len(byEntity[k]))
		for _, a := range byEntity[k] {
			m[a.Key] = a.Value
		}
		if raw, ok := m[store.AnnotationHidden]; ok {
			if _, derr := decodeHidden(raw); derr != nil {
				return Inputs{}, fmt.Errorf("focus: decode hidden annotation of %s:%s: %w", k.Type, k.ID, derr)
			}
		}
		in.Annotations[k] = m
	}
	allLinks, err := st.ListXrefLinksForRepo(ctx, repo)
	if err != nil {
		return Inputs{}, fmt.Errorf("focus: load links: %w", err)
	}
	for _, l := range allLinks {
		if _, ok := in.Entities[Key{l.FromType, l.FromID}]; !ok {
			continue
		}
		in.Links = append(in.Links, l)
		if l.Relation == relationReferences && strings.HasPrefix(l.Origin, externalOriginPrefix) && l.Reason == MergeReason {
			in.Absorbed[Key{l.ToType, l.ToID}] = Key{l.FromType, l.FromID}
		}
	}

	rows, err := st.FocusRowsAllPeriods(ctx)
	if err != nil {
		return Inputs{}, fmt.Errorf("focus: load plan rows: %w", err)
	}
	for _, r := range rows {
		if r.Repo != repo {
			continue
		}
		in.PlanKeys[Key{r.EntityType, r.EntityID}] = true
	}
	return in, nil
}

// decodeHidden decodes the value of the reserved hidden annotation,
// {"value": bool, "reason": string|null}.
func decodeHidden(raw string) (bool, error) {
	var h struct {
		Value bool `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return false, err
	}
	return h.Value, nil
}
