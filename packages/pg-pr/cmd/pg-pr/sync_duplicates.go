package main

import (
	"context"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/spf13/cobra"
)

// duplicateAuditor is the narrow read surface `sync duplicates` needs, so tests
// can inject a fake without a bd workspace.
type duplicateAuditor interface {
	FindDuplicateMergeRequests(ctx context.Context) ([]beads.DuplicateMergeRequests, error)
	FindDuplicateProcessingCycles(ctx context.Context) ([]beads.DuplicateProcessingCycles, error)
}

// duplicateWorkspace pairs one configured bd workspace with its auditor.
type duplicateWorkspace struct {
	Path    string
	Auditor duplicateAuditor
}

// newDuplicateAuditors builds one auditor per DISTINCT configured bd workspace
// (repos may share one), keyed by path. Overridable for tests.
var newDuplicateAuditors = func(cfg *config.Config) []duplicateWorkspace {
	seen := map[string]struct{}{}
	var out []duplicateWorkspace
	for _, r := range cfg.Repos {
		if _, dup := seen[r.Path]; dup {
			continue
		}
		seen[r.Path] = struct{}{}
		out = append(out, duplicateWorkspace{Path: r.Path, Auditor: beads.NewClientForRepo(r.Path)})
	}
	if len(out) == 0 {
		out = append(out, duplicateWorkspace{Auditor: beads.NewClient()})
	}
	return out
}

// duplicatePopulation names the bead population BOTH arms of the audit scan. It
// is emitted in the human and --json output because the total is otherwise
// misreadable: an open-only count decays as beads close (pg2-0z8fw) and would be
// mistaken for duplicates having been resolved. Both arms are status-agnostic,
// so the total is a census of duplicated identities that is comparable across
// time.
const duplicatePopulation = "all statuses (open and closed)"

// duplicateExclusion names what the audit scans but does NOT count: a duplicate
// ADJUDICATED against a canonical bead sharing its identity, recorded as a
// `supersedes` dependency edge (pg2-peyf0). It is reported separately from
// duplicatePopulation because the two answer different questions — the population
// is what was scanned, and closing a duplicate never removes it from that
// (pg2-0z8fw); this is what a completed reconcile removes from the COUNT, which
// is what makes the total usable as a regression baseline.
const duplicateExclusion = "adjudicated duplicates (a supersedes edge to another bead with the same identity)"

// duplicateReport is the machine-readable shape of one workspace's audit.
type duplicateReport struct {
	Workspace string `json:"workspace"`
	// Population states which beads the counts below cover, so a consumer
	// diffing totals over time can tell the population did not change.
	Population string `json:"population"`
	// Exclusion states which of that population is deliberately NOT counted, so
	// a consumer can tell a total that dropped because duplicates were resolved
	// from one that dropped because the audit narrowed.
	Exclusion        string                            `json:"exclusion,omitempty"`
	MergeRequests    []beads.DuplicateMergeRequests    `json:"duplicate_merge_requests,omitempty"`
	ProcessingCycles []beads.DuplicateProcessingCycles `json:"duplicate_process_feedback,omitempty"`
	Error            string                            `json:"error,omitempty"`
}

var syncDuplicatesJSON bool

// syncDuplicatesCmd is a READ-ONLY audit. It issues `bd list` and nothing else:
// no create, no update, no close. Collapsing the beads it reports is an
// operator-scheduled data migration against a live workspace, so this command
// deliberately has NO apply/fix mode — it prints the excess bead ids and the
// commands a person may choose to run. Adding a mutating flag here would put a
// bulk bead rewrite one typo away from a sync tick.
var syncDuplicatesCmd = &cobra.Command{
	Use:   "duplicates",
	Short: "Report merge-request / process-feedback beads duplicated per PR (read-only)",
	Long: `Duplicates audits the bd workspace(s) for the bead identities pg-pr is
supposed to keep unique:

  - merge-request beads sharing one (repo, pr_number)
  - process-feedback beads sharing one (repo, pr_number)

Both arms cover beads in ALL statuses, open and closed. Closing both members of
a duplicate pair does not resolve it — both beads still exist — so a closed pair
stays in the report and the total stays comparable across runs.

ADJUDICATED duplicates are excluded. A duplicate is adjudicated when a
"supersedes" dependency edge records that it resolves into a named canonical bead
with the SAME identity:

  bd dep add <excess-id> <keep-id> -t supersedes

The discriminator is that EDGE, never the wording of a close reason, so a
reconcile that only closes the excess beads does not move the total — record the
edge as well. The edge does not gate readiness, so it is a pure annotation.

Because the process-feedback arm counts closed beads, a CLOSED PREDECESSOR and
the successor that supersedes it also share one key. Such a pair is a legitimate
lifecycle, not a duplicate: it is excluded once the succession is recorded as a
supersedes edge, but a succession recorded only in the successor's description
prose is still listed. Read each group before acting.

It is READ-ONLY: it runs bd list and never writes. Nothing is closed, updated,
or merged — the excess beads are printed with the bd commands a person may run
after reviewing them.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cfg, err := loadConfigForCLI(ctx)
		if err != nil {
			return err
		}
		var reports []duplicateReport
		for _, ws := range newDuplicateAuditors(cfg) {
			rep := duplicateReport{
				Workspace:  ws.Path,
				Population: duplicatePopulation,
				Exclusion:  duplicateExclusion,
			}
			if rep.Workspace == "" {
				rep.Workspace = "(cwd)"
			}
			mrs, err := ws.Auditor.FindDuplicateMergeRequests(ctx)
			if err != nil {
				rep.Error = err.Error()
				reports = append(reports, rep)
				continue
			}
			cycles, err := ws.Auditor.FindDuplicateProcessingCycles(ctx)
			if err != nil {
				rep.Error = err.Error()
				reports = append(reports, rep)
				continue
			}
			rep.MergeRequests, rep.ProcessingCycles = mrs, cycles
			reports = append(reports, rep)
		}
		if output.Resolve(syncDuplicatesJSON) {
			return writeJSON(cmd.OutOrStdout(), reports)
		}
		renderDuplicateReports(cmd.OutOrStdout(), reports)
		return nil
	},
}

// renderDuplicateReports prints the human-readable audit. Write errors are
// non-fatal — the caller may have closed the writer.
func renderDuplicateReports(w io.Writer, reports []duplicateReport) {
	mrExcess, pfExcess := 0, 0
	for _, rep := range reports {
		_, _ = fmt.Fprintf(w, "workspace %s\n", rep.Workspace)
		if rep.Population != "" {
			_, _ = fmt.Fprintf(w, "  scanned %s\n", rep.Population)
		}
		if rep.Exclusion != "" {
			_, _ = fmt.Fprintf(w, "  not counted: %s\n", rep.Exclusion)
		}
		if rep.Error != "" {
			_, _ = fmt.Fprintf(w, "  ! %s\n", rep.Error)
			continue
		}
		if len(rep.MergeRequests) == 0 && len(rep.ProcessingCycles) == 0 {
			_, _ = fmt.Fprintln(w, "  ok no duplicated bead identities")
			continue
		}
		for _, d := range rep.MergeRequests {
			_, _ = fmt.Fprintf(w, "  merge-request %s#%d: keep %s, excess %s\n",
				d.Repo, d.PRNumber, d.Canonical.ID, joinMergeRequestIDs(d.Excess))
			mrExcess += len(d.Excess)
		}
		for _, d := range rep.ProcessingCycles {
			_, _ = fmt.Fprintf(w, "  process-feedback %s: keep %s, excess %s\n",
				d.Key, d.Canonical.ID, joinCycleIDs(d.Excess))
			pfExcess += len(d.Excess)
		}
	}
	excess := mrExcess + pfExcess
	if excess == 0 {
		return
	}
	// The population is restated on the total line: this number is the operator's
	// reconcile baseline and the input to "MUST NOT increase" checks, so it must
	// not be readable as an open-only count (pg2-0z8fw).
	_, _ = fmt.Fprintf(w, "\n%d excess bead(s) in %s: %d merge-request + %d process-feedback.\n",
		excess, duplicatePopulation, mrExcess, pfExcess)
	_, _ = fmt.Fprintln(w, "This command does NOT change them.")
	_, _ = fmt.Fprintln(w, "Review each one first; a person may then close it, e.g.:")
	_, _ = fmt.Fprintln(w, "  bd close <excess-id> --reason \"duplicate of <keep-id> (same repo#pr)\"")
	// The close reason is PROSE and is deliberately not this audit's
	// discriminator, so closing alone leaves the bead in the count. Print the
	// structural half of the adjudication beside it, or a reconcile that looks
	// complete cannot move the number (pg2-peyf0).
	_, _ = fmt.Fprintln(w, "and record the adjudication structurally, or the count will not drop:")
	_, _ = fmt.Fprintln(w, "  bd dep add <excess-id> <keep-id> -t supersedes")
}

func joinMergeRequestIDs(mrs []beads.MergeRequest) string {
	ids := make([]string, 0, len(mrs))
	for _, m := range mrs {
		ids = append(ids, m.ID)
	}
	return joinIDs(ids)
}

func joinCycleIDs(cs []beads.ProcessingCycle) string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return joinIDs(ids)
}

func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	out := ids[0]
	for _, id := range ids[1:] {
		out += ", " + id
	}
	return out
}

func init() {
	syncDuplicatesCmd.Flags().BoolVar(&syncDuplicatesJSON, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	syncCmd.AddCommand(syncDuplicatesCmd)
}
