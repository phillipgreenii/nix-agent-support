package unstick

import (
	"fmt"
	"sort"
	"strings"
)

// Finished-epic candidates (tc-tce2q). An epic that is open with every child
// closed is invisible to the REVIEW sweep (it is ready, so never a target) and
// to /pb:drain-beads (epics are never claimed), so it would stay open forever.
// "All children closed" is a CANDIDATE signal only: the worker still has to
// decide whether the epic's scope is complete (the epic text may describe more
// work than was ever decomposed).
//
// Epic candidates are disjoint from Targets by construction (a candidate is a
// ready row; a target is not), so the targets = live + marker + review
// partition is untouched.

// Epic batch bounds. Each epic needs live probes, so E batches are small and
// never clustered.
const (
	// EpicMinBatch and EpicMaxBatch bound an E batch when more than one is
	// needed (always satisfiable: ceil(n/5) batches split evenly give each at
	// least 3 once n > 5).
	EpicMinBatch = 3
	EpicMaxBatch = 5
)

// EpicBatchName returns the batch file name for the 0-based index i: E01, E02, ...
func EpicBatchName(i int) string { return fmt.Sprintf("E%02d", i+1) }

// epicCandidates returns every finished-epic candidate (after --label /
// --id-prefix narrowing), sorted by id. It is a pure function of the export
// graph and the ready list: no extra bd calls.
//
// A candidate is a row of `bd ready` with issue_type epic that is not a
// template, whose export row is open and unassigned, carries no focus-item
// label (on either row), has at least one parent-child child, and whose every
// child is closed. A child absent from the export counts as not closed. The
// human label does not disqualify an epic.
func (t *triager) epicCandidates() []string {
	var out []string
	for id, rr := range t.ready {
		if rr.IssueType != issueTypeEpic || rr.IsTemplate || rr.Assignee != "" {
			continue
		}
		row, ok := t.g.Row(id)
		if !ok || row.Status != StatusOpen || row.Assignee != "" {
			continue
		}
		if row.HasLabel(labelFocusItem) || hasAnyLabel(rr.Labels, []string{labelFocusItem}) {
			continue
		}
		if t.opts.Label != "" && !row.HasLabel(t.opts.Label) {
			continue
		}
		if t.opts.IDPrefix != "" && !strings.HasPrefix(id, t.opts.IDPrefix) {
			continue
		}
		kids := t.g.Children(id)
		if len(kids) == 0 {
			continue
		}
		allClosed := true
		for _, k := range kids {
			if kr, ok := t.g.Row(k); !ok || kr.Status != StatusClosed {
				allClosed = false
				break
			}
		}
		if allClosed {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// PackEpics splits the sorted epic ids to dispatch into E batches of
// EpicMinBatch..EpicMaxBatch epics (sizes differ by at most one). A total
// below EpicMinBatch is one batch; an empty input yields none. Epics are not
// clustered. The input is not modified.
func PackEpics(ids []string) [][]string {
	n := len(ids)
	if n == 0 {
		return nil
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	k := (n + EpicMaxBatch - 1) / EpicMaxBatch
	base, extra := n/k, n%k
	out := make([][]string, 0, k)
	for i, at := 0, 0; i < k; i++ {
		size := base
		if i < extra {
			size++
		}
		out = append(out, sorted[at:at+size:at+size])
		at += size
	}
	return out
}

// WriteEpicBatches writes every epic batch as E01, E02, ... (same file layout
// as WriteBatches) and returns the names.
func WriteEpicBatches(w Workdir, batches [][]string, g *Graph) ([]string, error) {
	names := make([]string, 0, len(batches))
	for i, b := range batches {
		name := EpicBatchName(i)
		if err := WriteBatch(w, name, b, g); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}
