package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/dashboard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/spf13/cobra"
)

// openFlags holds the flags for `pg-pr open`.
type openFlags struct {
	all            bool
	needsAttention bool
	mine           bool
	reason         string
	owner          string
	notOwner       string
	unapproved     bool
	max            int
	printOnly      bool
	noHyperlinks   bool
	addr           string
}

var opFlags openFlags

// openRow is the normalized view of one selectable PR.
//
// snapshot.MineRow and snapshot.TeamRow are deliberately different shapes — a
// MineRow has no Owner or MatchReason, a TeamRow has no draft/merge state — so
// both are projected onto this one shape and every filter, renderer and test
// downstream sees a single type.
type openRow struct {
	Number         int
	Owner          string
	Title          string
	URL            string
	CIStatus       string
	HumanApproved  bool
	AgentApproved  bool
	FilesChanged   int
	LinesChanged   int
	NeedsAttention bool
	MatchReason    []string
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the PRs needing your review in a new browser window",
	Long: `Open the pull requests that currently need your review as ONE new browser
window, with one tab per PR, in your existing browser profile.

The review set is read from the running sync daemon's /api/v1/dashboard
endpoint — the same data the Grafana "PRs to Review" panel shows. It is the only
cheap source: the daemon computes the set from live provider enrichment and
holds it in memory, so a one-shot command cannot re-derive it without repeating
every per-PR round-trip.

The attention filter defaults per source: the team's review set defaults to just
the PRs needing attention (it is large, and most of it is not waiting on you),
while --mine defaults to ALL of your own PRs. Override in whichever direction the
default forecloses — --all widens, --needs-attention narrows.

Pass --print to list the selection instead of opening a browser. Each row shows
its URL, which your terminal makes clickable on its own; where stdout is a
terminal the title is additionally an OSC 8 hyperlink (rendered as plain text
until hovered, opened with a modifier-click). --no-hyperlinks forces plain output.

Because the daemon's snapshot ages between ticks, a stale payload is reported as
a warning on stderr and then opened anyway — the operator decides whether
slightly old data is worth acting on.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateOpenFlags(opFlags); err != nil {
			return err
		}

		snap, err := dashboard.Fetch(cmd.Context(), opFlags.addr)
		if err != nil {
			return err
		}
		if snap.Stale {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: dashboard data is %ds old (stale after %ds) — the sync daemon is behind or stopped\n",
				snap.AgeSeconds, snap.StaleAfterSeconds)
		}

		rows := selectRows(projectRows(snap, opFlags.mine), opFlags)
		if len(rows) == 0 {
			_, err := io.WriteString(cmd.OutOrStdout(), "(no PRs match)\n")
			return err
		}
		if opFlags.max > 0 && len(rows) > opFlags.max {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %d PRs matched, showing the first %d (--max)\n", len(rows), opFlags.max)
			rows = rows[:opFlags.max]
		}

		if opFlags.printOnly {
			return renderOpenRows(cmd.OutOrStdout(), rows, useHyperlinks(cmd.OutOrStdout(), opFlags))
		}
		return browser.OpenWindow(urlsOf(rows))
	},
}

// attentionOnly decides whether the needs-attention filter applies. The default
// is PER SOURCE, which is the whole point:
//
//	team  — default attention-only. The review set is large (24 PRs at the time
//	        of writing) and most of it is not waiting on me, so the useful
//	        default is the subset that is.
//	mine  — default everything. For my OWN PRs I want the whole list; the
//	        composed attention signal (see projectRows) is frequently all-false,
//	        so defaulting to it made `--mine` alone print "(no PRs match)".
//
// Either default is overridable in the direction that default forecloses:
// --all widens, --needs-attention narrows.
func attentionOnly(f openFlags) bool {
	switch {
	case f.needsAttention:
		return true
	case f.all:
		return false
	default:
		return !f.mine
	}
}

// validateOpenFlags rejects flag combinations that could only ever match
// nothing, or that ask for two contradictory things.
//
// --reason, --owner and --not-owner all read fields that exist on a TeamRow but
// not a MineRow, so pairing them with --mine would silently return an empty
// selection — indistinguishable from "you have no work". Failing with a usage
// error says which flag is wrong instead.
func validateOpenFlags(f openFlags) error {
	if f.all && f.needsAttention {
		return fmt.Errorf("--all and --needs-attention are contradictory: one widens the selection, the other narrows it")
	}
	if !f.mine {
		return nil
	}
	for _, bad := range []struct {
		name  string
		unset bool
	}{
		{"--reason", f.reason == ""},
		{"--owner", f.owner == ""},
		{"--not-owner", f.notOwner == ""},
	} {
		if !bad.unset {
			return fmt.Errorf("%s cannot be combined with --mine: your own PRs carry no owner or match-reason fields", bad.name)
		}
	}
	return nil
}

// projectRows flattens the snapshot's chosen half onto []openRow.
//
// The needs-attention mapping differs per half and cannot be shared. A TeamRow
// carries the daemon's own NeedsAttention predicate. A MineRow has no such
// field, so "needs attention" is composed from the three signals that mean I
// personally have to act: someone is waiting on my reply, the PR is mergeable
// but nothing armed the merge, or it has conflicts only I can resolve.
func projectRows(snap *snapshot.Snapshot, mine bool) []openRow {
	if mine {
		out := make([]openRow, 0, len(snap.Mine))
		for _, r := range snap.Mine {
			out = append(out, openRow{
				Number:         r.Number,
				Title:          r.Title,
				URL:            r.URL,
				CIStatus:       r.CIStatus,
				HumanApproved:  r.HumanApproved,
				AgentApproved:  r.AgentApproved,
				NeedsAttention: r.WaitingOnMe || r.NeedsMergeReminder || r.HasConflicts,
			})
		}
		return out
	}
	out := make([]openRow, 0, len(snap.Team))
	for _, r := range snap.Team {
		out = append(out, openRow{
			Number:         r.Number,
			Owner:          r.Owner,
			Title:          r.Title,
			URL:            r.URL,
			CIStatus:       r.CIStatus,
			HumanApproved:  r.HumanApproved,
			AgentApproved:  r.AgentApproved,
			FilesChanged:   r.FilesChanged,
			LinesChanged:   r.LinesChanged,
			NeedsAttention: r.NeedsAttention,
			MatchReason:    r.MatchReason,
		})
	}
	return out
}

// selectRows applies every filter, preserving the snapshot's own ordering. It
// does NOT apply --max: truncation warns on stderr, which is the caller's job.
func selectRows(rows []openRow, f openFlags) []openRow {
	attention := attentionOnly(f)
	out := make([]openRow, 0, len(rows))
	for _, r := range rows {
		switch {
		case attention && !r.NeedsAttention:
		case f.reason != "" && !hasReason(r.MatchReason, f.reason):
		case f.owner != "" && r.Owner != f.owner:
		case f.notOwner != "" && r.Owner == f.notOwner:
		case f.unapproved && r.HumanApproved:
		default:
			out = append(out, r)
		}
	}
	return out
}

// hasReason matches want against a row's match reasons as an exact value OR a
// prefix, so the whole label family is selectable as `--reason label:` without
// needing a second flag, while `--reason review-requested` still means exactly
// that one reason.
func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, want) {
			return true
		}
	}
	return false
}

func urlsOf(rows []openRow) []string {
	urls := make([]string, 0, len(rows))
	for _, r := range rows {
		urls = append(urls, r.URL)
	}
	return urls
}

// useHyperlinks reports whether the listing should emit OSC 8 hyperlinks:
// only when the operator has not opted out AND the destination is a terminal.
func useHyperlinks(w io.Writer, f openFlags) bool {
	return !f.noHyperlinks && isTTY(w)
}

// isTTY reports whether w is a terminal.
//
// Deliberately stdlib-only (os.File.Stat + ModeCharDevice) rather than
// promoting mattn/go-isatty from an indirect to a direct dependency, which
// would force a gomod2nix.toml regeneration for a three-line check. A
// non-*os.File writer — the bytes.Buffer every test uses, and the pipe on the
// far side of `| grep` — reports false, so redirected output is plain by
// construction.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// hyperlink wraps text in an OSC 8 escape so a supporting terminal renders it
// as a clickable link to url.
func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// renderOpenRows writes the human listing.
//
// The URL is ALWAYS its own visible column, hyperlinked or not. An OSC 8 link
// renders as ordinary text until hovered and needs a modifier-click to open, so
// a listing that carried only the hyperlink left an operator who did not notice
// it with nothing to click and no URL to copy or pipe. A bare URL, by contrast,
// is clickable via the terminal's own URL detection in every terminal worth
// using — so it is the reliable affordance and the hyperlink is the bonus.
//
// The optional link rides on the TITLE column because that is the LAST column
// and tabwriter measures cell width in BYTES: an OSC 8 escape in any earlier
// column would inflate its apparent width and skew every column after it.
func renderOpenRows(w io.Writer, rows []openRow, link bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	if _, err := io.WriteString(tw, "PR\tOWNER\tCI\tAPPROVED\tSIZE\tURL\tTITLE\n"); err != nil {
		return err
	}

	for _, r := range rows {
		title := r.Title
		if link {
			title = hyperlink(r.URL, title)
		}
		cells := []string{
			"#" + strconv.Itoa(r.Number),
			orDash(r.Owner),
			orDash(r.CIStatus),
			approvedCell(r),
			sizeCell(r),
			r.URL,
			title,
		}
		if _, err := io.WriteString(tw, strings.Join(cells, "\t")+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// approvedCell reports which classes of reviewer have approved, as a stable
// comma-joined value so the column is both readable and greppable.
func approvedCell(r openRow) string {
	var got []string
	if r.HumanApproved {
		got = append(got, "human")
	}
	if r.AgentApproved {
		got = append(got, "agent")
	}
	return orDash(strings.Join(got, ","))
}

// sizeCell renders a PR's size as files/lines. Both are absent on a MineRow, in
// which case the cell collapses to a dash rather than a misleading "0f/0L".
func sizeCell(r openRow) string {
	if r.FilesChanged == 0 && r.LinesChanged == 0 {
		return "-"
	}
	return strconv.Itoa(r.FilesChanged) + "f/" + strconv.Itoa(r.LinesChanged) + "L"
}

func init() {
	openCmd.Flags().BoolVar(&opFlags.all, "all", false,
		"Widen to the whole set (the default already, with --mine)")
	openCmd.Flags().BoolVar(&opFlags.needsAttention, "needs-attention", false,
		"Narrow to the PRs needing attention (the default already, without --mine)")
	openCmd.Flags().BoolVar(&opFlags.mine, "mine", false,
		"Open your own PRs instead of the team's review set; defaults to all of them")
	openCmd.Flags().StringVar(&opFlags.reason, "reason", "",
		"Keep only PRs matched for this reason; exact or prefix (team-authored, review-requested, label:team/findev, label:)")
	openCmd.Flags().StringVar(&opFlags.owner, "owner", "",
		"Keep only PRs owned by this login")
	openCmd.Flags().StringVar(&opFlags.notOwner, "not-owner", "",
		"Drop PRs owned by this login")
	openCmd.Flags().BoolVar(&opFlags.unapproved, "unapproved", false,
		"Drop PRs a human has already approved")
	openCmd.Flags().IntVar(&opFlags.max, "max", 0,
		"Cap how many PRs are opened; 0 (the default) opens every match")
	openCmd.Flags().BoolVar(&opFlags.printOnly, "print", false,
		"List the selected PRs instead of opening a browser window")
	openCmd.Flags().BoolVar(&opFlags.noHyperlinks, "no-hyperlinks", false,
		"With --print, never emit OSC 8 terminal hyperlinks")
	openCmd.Flags().StringVar(&opFlags.addr, "addr", sync.DefaultMetricsAddr,
		"Address of the pg-pr sync daemon serving the dashboard endpoint")

	rootCmd.AddCommand(openCmd)
}
