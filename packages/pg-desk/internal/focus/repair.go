package focus

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// RepairResult is what Repair wrote (or, for a dry run, would write): one
// entry per entity whose annotation was missing or did not match the table.
type RepairResult struct{ Written []AnnotationOutcome }

// Repair re-writes every missing or mismatched focus_selected annotation from
// the committed table (`select --repair`, spec section 7.2). It scans EVERY
// entity whose annotation is not none and every entity with a plan row in
// any period, so the cross-period deletions of a first lock are mended too,
// reads no reply, and is exempt from the period-closed check: the annotation
// is a function of the table, so a row_without_annotation left by a crash
// just before a close and an annotation_without_row are both mended the same
// way (the annotation is rewritten to the function's value, none when no row
// remains). With dryRun it writes nothing and reports each entity as
// would-write.
//
// Telemetry (D24): no OpenTelemetry or Prometheus signal; the annotation's
// change_log record and the verb's run record are the trace.
func Repair(ctx context.Context, d LockDeps, dryRun bool) (RepairResult, error) {
	repo := d.repo()
	cands := map[Key]bool{}
	ents, err := d.Store.ListEntities()
	if err != nil {
		return RepairResult{}, fmt.Errorf("focus: repair: list entities: %w", err)
	}
	for _, e := range ents {
		if e.Repo != repo {
			continue
		}
		a, found, err := d.Store.GetKVAnnotation(repo, e.EntityType, e.EntityID, store.AnnotationFocusSelected)
		if err != nil {
			return RepairResult{}, fmt.Errorf("focus: repair: read annotation of %s:%s: %w", e.EntityType, e.EntityID, err)
		}
		if found && a.Value != store.FocusAnnotationNone {
			cands[Key{Type: e.EntityType, ID: e.EntityID}] = true
		}
	}
	rows, err := d.Store.FocusRowsAllPeriods()
	if err != nil {
		return RepairResult{}, fmt.Errorf("focus: repair: read plan rows: %w", err)
	}
	for _, r := range rows {
		if r.Repo == repo {
			cands[Key{Type: r.EntityType, ID: r.EntityID}] = true
		}
	}
	keys := make([]Key, 0, len(cands))
	for k := range cands {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })

	var stale []Key
	for _, k := range keys {
		want, err := d.Store.FocusAnnotationValue(repo, k.Type, k.ID)
		if err != nil {
			return RepairResult{}, fmt.Errorf("focus: repair: %w", err)
		}
		a, found, err := d.Store.GetKVAnnotation(repo, k.Type, k.ID, store.AnnotationFocusSelected)
		if err != nil {
			return RepairResult{}, fmt.Errorf("focus: repair: %w", err)
		}
		switch {
		case found && a.Value == want:
		case !found && want == store.FocusAnnotationNone:
		default:
			stale = append(stale, k)
		}
	}
	if dryRun {
		out := RepairResult{}
		for _, k := range stale {
			out.Written = append(out.Written, AnnotationOutcome{Key: k, Outcome: AnnotationWouldWrite})
		}
		return out, nil
	}
	return RepairResult{Written: ApplyAnnotations(ctx, d, stale, d.now().Format(time.RFC3339))}, nil
}
