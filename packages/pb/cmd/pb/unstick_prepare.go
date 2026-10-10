package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/unstick"
	"github.com/spf13/cobra"
)

// Gate-check statuses reported by prepare.
const (
	gateCheckOK          = "ok"
	gateCheckPartial     = "partial"
	gateCheckUnavailable = "unavailable"
)

type batchSummary struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type malformedSummary struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// prepareSummary is the stdout / --json result of `pb unstick prepare`.
type prepareSummary struct {
	Workdir          string                   `json:"workdir"`
	Start            string                   `json:"start"`
	Counts           unstick.TriageCounts     `json:"counts"`
	Partition        string                   `json:"partition"`
	Batches          []batchSummary           `json:"batches"`
	ClaimCandidates  []unstick.ClaimCandidate `json:"claim_candidates"`
	MalformedMarkers malformedSummary         `json:"malformed_markers"`
	GateCheck        string                   `json:"gate_check"`
	Warnings         []string                 `json:"warnings"`
	Written          []string                 `json:"written"`
}

func newUnstickPrepareCmd(env unstickEnv) *cobra.Command {
	var (
		root, workdir, label, idPrefix string
		full, asJSON                   bool
		now                            nowFlag
	)
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Inventory, triage and batch the open-but-not-ready beads into a fresh work directory",
		Long: `Stages 1, 3, 4, 5 and 7-prep of /pb:unstick-beads in one call.

Allocates a FRESH work directory (/tmp/bead-unstick-<YYYY-MM-DD>[-N], never
reused; --workdir names one explicitly and must not exist), exports the bead
database (bd export -o), reads bd ready, runs the pn:applied gate check
in-process (dry-run), then computes LIVE / marker-skip / REVIEW over the WHOLE
workspace. --label and --id-prefix narrow the TARGETS only after LIVE and
DRAINABLE are computed. --full ignores sweep markers.

Writes triage-*.txt, batches/B<NN>, facts/B<NN>.json and prepare.json. A gate
check that skipped gates is reported as gate_check "partial", one that failed
outright as "unavailable"; neither is fatal.

Exit codes: 0 ok; 1 usage, IO or internal error (including a broken triage
partition); 2 a bd call failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := now.resolve(env)
			if err != nil {
				return err
			}
			r, err := resolveRoot(env, root)
			if err != nil {
				return err
			}
			if workdir != "" && !filepath.IsAbs(workdir) {
				return fmt.Errorf("--workdir must be an absolute path, got %q", workdir)
			}
			var w unstick.Workdir
			if workdir != "" {
				w, err = unstick.CreateWorkdir(workdir, t)
			} else {
				w, err = unstick.AllocateWorkdir(env.TmpBase, t)
			}
			if err != nil {
				return err
			}
			sum, err := runPrepare(cmd, env, w, r, unstick.TriageOptions{Full: full, Label: label, IDPrefix: idPrefix}, t)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), sum)
			}
			renderPrepareHuman(cmd.OutOrStdout(), sum)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "pn workspace root holding the beads DB (default: $PN_WORKSPACE_ROOT, else the nearest pn-workspace.toml)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "absolute path of the work directory to create (default: a fresh /tmp/bead-unstick-<date>[-N])")
	cmd.Flags().BoolVar(&full, "full", false, "ignore sweep markers (no marker skip)")
	cmd.Flags().StringVar(&label, "label", "", "keep only targets carrying this label (applied after LIVE is computed)")
	cmd.Flags().StringVar(&idPrefix, "id-prefix", "", "keep only targets whose id starts with this prefix (applied after LIVE is computed)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	now.register(cmd)
	return cmd
}

func runPrepare(cmd *cobra.Command, env unstickEnv, w unstick.Workdir, root string, opts unstick.TriageOptions, now time.Time) (prepareSummary, error) {
	ctx := cmdCtx(cmd)
	client := bd.Client{R: env.Runner}
	sum := prepareSummary{Workdir: w.Path, Start: unstick.FormatTime(now), Warnings: []string{}}
	written := []string{unstick.ProgressFile, unstick.FollowupsFile}

	if err := client.Export(ctx, root, w.Join(unstick.ExportFile)); err != nil {
		return sum, bdFailure(err)
	}
	written = append(written, unstick.ExportFile)
	rawReady, err := client.Ready(ctx, root)
	if err != nil {
		return sum, bdFailure(err)
	}
	if err := os.WriteFile(w.Join(unstick.ReadyFile), rawReady, 0o600); err != nil {
		return sum, err
	}
	written = append(written, unstick.ReadyFile)
	ready, err := unstick.ParseReady(rawReady)
	if err != nil {
		return sum, bdFailure(err) // bd answered, but not with a usable ready list
	}
	rows, err := unstick.ReadExportFile(w.Join(unstick.ExportFile))
	if err != nil {
		return sum, err
	}

	// Gate check: advisory, never fatal.
	sum.GateCheck = gateCheckOK
	gres, gerr := env.gateCheck(ctx, root, now)
	switch {
	case gerr != nil:
		sum.GateCheck = gateCheckUnavailable
		sum.Warnings = append(sum.Warnings, "gate check unavailable: "+gerr.Error())
	default:
		if len(gres.Skipped) > 0 {
			sum.GateCheck = gateCheckPartial
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("gate check partial: %d gate(s) could not be determined", len(gres.Skipped)))
		}
		if err := writeJSONFile(w.Join(unstick.ProbesDir, unstick.GateCheckFile), gres); err != nil {
			return sum, err
		}
		written = append(written, unstick.ProbesDir+"/"+unstick.GateCheckFile)
	}

	res := unstick.Triage(rows, ready, opts, now)
	if err := res.CheckPartition(); err != nil {
		return sum, err
	}

	lists := []struct {
		name string
		ids  []string
	}{
		{"triage-targets.txt", res.Targets},
		{"triage-live.txt", res.Live},
		{"triage-marker.txt", res.MarkerSkip},
		{"triage-review.txt", res.Review},
		{"triage-inprog.txt", res.InProgress},
		{"triage-assigned_open.txt", res.AssignedOpen},
		{"triage-drain.txt", res.Drainable},
	}
	for _, l := range lists {
		if err := writeIDList(w.Join(l.name), l.ids); err != nil {
			return sum, err
		}
		written = append(written, l.name)
	}

	g := unstick.NewGraph(rows)
	batches := unstick.Cluster(res.Review, g)
	if err := checkBatchCoverage(res.Review, batches); err != nil {
		return sum, err
	}
	names, err := unstick.WriteBatches(w, batches, g)
	if err != nil {
		return sum, err
	}
	sum.Batches = []batchSummary{}
	for i, n := range names {
		sum.Batches = append(sum.Batches, batchSummary{Name: n, Size: len(batches[i])})
		written = append(written, unstick.BatchesDir+"/"+n, unstick.FactsDir+"/"+n+".json")
	}

	st := unstick.PrepareState{
		Start: sum.Start, Now: sum.Start, Review: res.Review,
		Counts: countsMap(res.Counts), Pre: map[string]string{},
	}
	for _, rr := range ready {
		st.ReadyIDs = append(st.ReadyIDs, rr.ID)
	}
	for _, r := range rows {
		if r.Status == unstick.StatusClosed {
			st.Closed = append(st.Closed, r.ID)
		} else {
			st.Pre[r.ID] = r.Status
		}
	}
	if err := unstick.WritePrepare(w.Join(unstick.PrepareFile), st); err != nil {
		return sum, err
	}
	written = append(written, unstick.PrepareFile)

	sum.Counts = res.Counts
	sum.Partition = res.Arithmetic()
	sum.ClaimCandidates = res.ClaimCandidates
	if sum.ClaimCandidates == nil {
		sum.ClaimCandidates = []unstick.ClaimCandidate{}
	}
	sum.MalformedMarkers = malformedSummary{Count: len(res.MalformedMarkers), IDs: []string{}}
	for _, m := range res.MalformedMarkers {
		sum.MalformedMarkers.IDs = append(sum.MalformedMarkers.IDs, m.ID)
	}
	sort.Strings(written)
	sum.Written = written
	return sum, nil
}

// checkBatchCoverage asserts every REVIEW id is in exactly one batch and no
// batch holds anything else.
func checkBatchCoverage(review []string, batches [][]string) error {
	seen := map[string]int{}
	for _, b := range batches {
		for _, id := range b {
			seen[id]++
		}
	}
	for _, id := range review {
		if seen[id] != 1 {
			return fmt.Errorf("batch coverage broken: review bead %s is in %d batches", id, seen[id])
		}
		delete(seen, id)
	}
	for id := range seen {
		return fmt.Errorf("batch coverage broken: batched bead %s is not a review bead", id)
	}
	return nil
}

func countsMap(c unstick.TriageCounts) map[string]int {
	return map[string]int{
		"open": c.Open, "blocked": c.Blocked, "deferred": c.Deferred, "in_progress": c.InProgress,
		"ready": c.Ready, "targets": c.Targets, "live_skip": c.LiveSkip, "marker_skip": c.MarkerSkip,
		"review": c.Review, "drainable": c.Drainable,
	}
}

func writeIDList(path string, ids []string) error {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func writeJSONFile(path string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := writeJSON(f, v); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func renderPrepareHuman(w io.Writer, s prepareSummary) {
	c := s.Counts
	fmt.Fprintf(w, "workdir:  %s\n", s.Workdir)
	fmt.Fprintf(w, "start:    %s\n", s.Start)
	fmt.Fprintf(w, "counts:   open %d, blocked %d, deferred %d, in_progress %d, ready %d, drainable %d\n",
		c.Open, c.Blocked, c.Deferred, c.InProgress, c.Ready, c.Drainable)
	fmt.Fprintf(w, "triage:   %s\n", s.Partition)
	fmt.Fprintf(w, "batches:  %d", len(s.Batches))
	for _, b := range s.Batches {
		fmt.Fprintf(w, " %s(%d)", b.Name, b.Size)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "claim candidates: %d\n", len(s.ClaimCandidates))
	for _, cc := range s.ClaimCandidates {
		fmt.Fprintf(w, "  %s  %s  assignee=%s  updated=%s\n", cc.ID, cc.Status, cc.Assignee, cc.UpdatedAt)
	}
	fmt.Fprintf(w, "malformed markers: %d", s.MalformedMarkers.Count)
	if s.MalformedMarkers.Count > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(s.MalformedMarkers.IDs, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "gate check: %s\n", s.GateCheck)
	for _, wn := range s.Warnings {
		fmt.Fprintf(w, "warning: %s\n", wn)
	}
	fmt.Fprintln(w, "written:")
	for _, p := range s.Written {
		fmt.Fprintf(w, "  %s/%s\n", s.Workdir, p)
	}
}
