package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/dashboard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
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
	includeHidden  bool
	max            int
	printOnly      bool
	noHyperlinks   bool
	addr           string
	jsonOutput     bool
}

var opFlags openFlags

// openRow is the normalized view of one selectable PR.
//
// snapshot.MineRow and snapshot.TeamRow are deliberately different shapes — a
// MineRow has no Owner or MatchReason, a TeamRow has no draft/merge state — so
// both are projected onto this one shape and every filter, renderer and test
// downstream sees a single type.
type openRow struct {
	Number   int
	Owner    string
	Title    string
	URL      string
	CIStatus string
	// HumanApprovers / AgentApprovers count the DISTINCT approvers whose
	// approval currently stands (snapshot.MineRow / snapshot.TeamRow carry the
	// same pair). They replaced a `HumanApproved`/`AgentApproved` bool pair
	// (pg2-4dz88.1.9): a bool cannot tell two approvers from one, and cannot
	// drop a STALE or DISMISSED approval back out of the count.
	HumanApprovers int
	AgentApprovers int
	FilesChanged   int
	LinesChanged   int
	NeedsAttention bool
	MatchReason    []string
	// Hidden / HiddenReason mirror snapshot.MineRow/TeamRow's identically-named
	// fields (pg2-4dz88.4.3): a hidden PR is dropped by selectRows unless
	// --include-hidden is passed, and the reason is shown in renderOpenRows
	// when it is included.
	Hidden       bool
	HiddenReason string
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

Pass --print to list the selection instead of opening a browser. On a terminal
each title is an OSC 8 hyperlink (rendered as plain text until hovered, opened
with a modifier-click). When output is piped or redirected — or with
--no-hyperlinks — the bare URL is printed as its own column instead, so the
listing stays greppable and pipeable.

Because the daemon's snapshot ages between ticks, a stale payload is reported as
a warning on stderr and then opened anyway — the operator decides whether
slightly old data is worth acting on.

A PR the operator hid ('pg-pr pr hide') is excluded by default — with --all
too, since --all only widens the attention filter, not this one. Pass
--include-hidden to see hidden PRs anyway; the human table then shows the
hide reason in its HIDDEN column.

Pass --json (or set PGPR_OUTPUT=json) to emit the selection as a bare JSON
array instead of opening a browser or printing the human table. Every row
repeats the snapshot's freshness scalars (generated_at/age_seconds/stale/
stale_after_seconds) and a truncated flag reflecting --max, since a machine
consumer cannot see the stderr warnings a human reads for either signal.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateOpenFlags(opFlags); err != nil {
			return err
		}
		jsonMode := output.Resolve(opFlags.jsonOutput)

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
			if jsonMode {
				return writeJSON(cmd.OutOrStdout(), openJSONRows(rows, snap, false))
			}
			_, err := io.WriteString(cmd.OutOrStdout(), "(no PRs match)\n")
			return err
		}
		truncated := opFlags.max > 0 && len(rows) > opFlags.max
		if truncated {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %d PRs matched, showing the first %d (--max)\n", len(rows), opFlags.max)
			rows = rows[:opFlags.max]
		}

		if jsonMode {
			return writeJSON(cmd.OutOrStdout(), openJSONRows(rows, snap, truncated))
		}

		if opFlags.printOnly {
			return renderOpenRows(cmd.OutOrStdout(), rows, useHyperlinks(cmd.OutOrStdout(), opFlags))
		}
		return browser.OpenWindow(urlsOf(rows))
	},
}

// openJSONRow is the JSON shape emitted per selected row by `pg-pr open
// --json`. It projects openRow's already-curated column set into
// machine-readable form, additionally carrying the snapshot's shared
// freshness scalars and the --max truncation fact on EVERY row.
//
// The freshness scalars are repeated per row rather than hoisted into an
// envelope because the top-level payload is a bare array (pg2-4dz88.7.7's
// pinned JSON shape) — an envelope would break the "empty selection is an
// empty array" contract. Unlike cmd/pg-pr/pr_list.go's prListItem, where
// LastSyncedAt/Stale genuinely vary per row (real per-PR store columns),
// these four values are identical across every row in one response: they
// describe the one dashboard snapshot the whole selection was read from.
type openJSONRow struct {
	Number         int      `json:"number"`
	Owner          string   `json:"owner"`
	Title          string   `json:"title"`
	URL            string   `json:"url"`
	CIStatus       string   `json:"ci_status"`
	HumanApprovers int      `json:"human_approvers"`
	AgentApprovers int      `json:"agent_approvers"`
	FilesChanged   int      `json:"files_changed"`
	LinesChanged   int      `json:"lines_changed"`
	NeedsAttention bool     `json:"needs_attention"`
	MatchReason    []string `json:"match_reason"`
	Hidden         bool     `json:"hidden"`
	HiddenReason   string   `json:"hidden_reason"`

	// GeneratedAt/AgeSeconds/Stale/StaleAfterSeconds mirror
	// snapshot.Snapshot's identically-tagged fields verbatim — the same
	// freshness contract (pr-pool INV-FRESH-1) the human renderer's
	// stderr warning is derived from, but JSON-visible on every row so a
	// stale snapshot yields stale:true in the payload itself.
	GeneratedAt       time.Time `json:"generated_at"`
	AgeSeconds        int       `json:"age_seconds"`
	Stale             bool      `json:"stale"`
	StaleAfterSeconds int       `json:"stale_after_seconds"`

	// Truncated is true on every row when --max cut the result set short.
	// It is the JSON-visible counterpart to the existing stderr warning
	// (TestOpenCmdMaxTruncatesAndWarns) — that warning alone is invisible
	// to a machine consumer, the same class of gap this bead already
	// fixes for staleness.
	Truncated bool `json:"truncated"`
}

// openJSONRows projects rows into their --json shape, stamping every row
// with snap's freshness scalars and the truncated fact. rows is iterated in
// place, so the emitted order is exactly selectRows' order — the same order
// renderOpenRows and urlsOf already consume — which is what keeps --json's
// row order identical to the human renderer's over the same input.
func openJSONRows(rows []openRow, snap *snapshot.Snapshot, truncated bool) []openJSONRow {
	out := make([]openJSONRow, 0, len(rows))
	for _, r := range rows {
		matchReason := r.MatchReason
		if matchReason == nil {
			matchReason = []string{}
		}
		out = append(out, openJSONRow{
			Number:            r.Number,
			Owner:             r.Owner,
			Title:             r.Title,
			URL:               r.URL,
			CIStatus:          r.CIStatus,
			HumanApprovers:    r.HumanApprovers,
			AgentApprovers:    r.AgentApprovers,
			FilesChanged:      r.FilesChanged,
			LinesChanged:      r.LinesChanged,
			NeedsAttention:    r.NeedsAttention,
			MatchReason:       matchReason,
			Hidden:            r.Hidden,
			HiddenReason:      r.HiddenReason,
			GeneratedAt:       snap.GeneratedAt,
			AgeSeconds:        snap.AgeSeconds,
			Stale:             snap.Stale,
			StaleAfterSeconds: snap.StaleAfterSeconds,
			Truncated:         truncated,
		})
	}
	return out
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
//
// --json and --print are likewise contradictory: both mean "list the
// selection instead of opening a browser," but in two different formats
// (machine JSON vs. the human table), so combining them can only ever pick
// one silently. This is a DECIDED rejection (pg2-4dz88.7.7), not an
// oversight — a future bead that finds a genuine need to combine them must
// change this decision deliberately, including the pinning test.
func validateOpenFlags(f openFlags) error {
	if f.all && f.needsAttention {
		return fmt.Errorf("--all and --needs-attention are contradictory: one widens the selection, the other narrows it")
	}
	if f.jsonOutput && f.printOnly {
		return fmt.Errorf("--json and --print are contradictory: --json already lists the selection instead of opening a browser")
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
			if r.Merged {
				// pg2-ew4kf: the dashboard retains a merged PR of mine for a
				// 24h grace period so the post-merge follow-up isn't
				// forgotten, but `pg-pr open` opens ACTIONABLE PRs in a
				// browser — an already-merged PR is never a candidate for
				// that, with or without --all/--needs-attention. Exclude it
				// here rather than relying on the NeedsAttention filter,
				// which --all bypasses entirely.
				continue
			}
			out = append(out, openRow{
				Number:         r.Number,
				Title:          r.Title,
				URL:            r.URL,
				CIStatus:       r.CIStatus,
				HumanApprovers: r.HumanApprovers,
				AgentApprovers: r.AgentApprovers,
				NeedsAttention: r.WaitingOnMe || r.NeedsMergeReminder || r.HasConflicts,
				Hidden:         r.Hidden,
				HiddenReason:   r.HiddenReason,
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
			HumanApprovers: r.HumanApprovers,
			AgentApprovers: r.AgentApprovers,
			FilesChanged:   r.FilesChanged,
			LinesChanged:   r.LinesChanged,
			NeedsAttention: r.NeedsAttention,
			MatchReason:    r.MatchReason,
			Hidden:         r.Hidden,
			HiddenReason:   r.HiddenReason,
		})
	}
	return out
}

// selectRows applies every filter, preserving the snapshot's own ordering. It
// does NOT apply --max: truncation warns on stderr, which is the caller's job.
//
// The hidden-PR exclusion (pg2-4dz88.4.3) is independent of --all/
// --needs-attention: --all only widens the attention filter, so a hidden row
// stays excluded even with --all, exactly like --owner/--unapproved already
// do. --include-hidden is the one flag that admits it.
func selectRows(rows []openRow, f openFlags) []openRow {
	attention := attentionOnly(f)
	out := make([]openRow, 0, len(rows))
	for _, r := range rows {
		switch {
		case r.Hidden && !f.includeHidden:
		case attention && !r.NeedsAttention:
		case f.reason != "" && !hasReason(r.MatchReason, f.reason):
		case f.owner != "" && r.Owner != f.owner:
		case f.notOwner != "" && r.Owner == f.notOwner:
		case f.unapproved && r.HumanApprovers > 0:
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

// renderOpenRows writes the human listing. The URL column is present exactly
// when the title is NOT hyperlinked:
//
//	hyperlinked (a terminal) — no URL column. The hyperlinked title IS the link,
//	  so a URL column only spends horizontal width to repeat it.
//	plain (piped, redirected, or --no-hyperlinks) — URL column. There is no link
//	  to carry the target, and a bare URL keeps the listing greppable and
//	  pipeable into xargs.
//
// This deliberately reverts an earlier always-show-the-URL layout, which existed
// because an OSC 8 link renders as ordinary text until hovered and opens only on
// a modifier-click — so if the terminal silently ignored the escape, the listing
// offered nothing to click and no URL to copy. That premise was tested and did
// not hold: both affordances work here. --no-hyperlinks remains the one-flag
// recovery for a terminal that genuinely does not honour OSC 8 (ssh, a
// multiplexer without passthrough), which keeps that failure mode reachable
// rather than merely reintroduced.
//
// The link rides on the TITLE column because that is the LAST column and
// tabwriter measures cell width in BYTES: an OSC 8 escape in any earlier column
// would inflate its apparent width and skew every column after it.
func renderOpenRows(w io.Writer, rows []openRow, link bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	header := "PR\tOWNER\tCI\tAPPROVED\tSIZE\tHIDDEN\tTITLE\n"
	if !link {
		header = "PR\tOWNER\tCI\tAPPROVED\tSIZE\tHIDDEN\tURL\tTITLE\n"
	}
	if _, err := io.WriteString(tw, header); err != nil {
		return err
	}

	for _, r := range rows {
		cells := []string{
			"#" + strconv.Itoa(r.Number),
			orDash(r.Owner),
			orDash(r.CIStatus),
			approvedCell(r),
			sizeCell(r),
			hiddenCell(r),
		}
		if link {
			cells = append(cells, hyperlink(r.URL, r.Title))
		} else {
			cells = append(cells, r.URL, r.Title)
		}
		if _, err := io.WriteString(tw, strings.Join(cells, "\t")+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// approvedCell reports which classes of reviewer have approved, as a stable
// comma-joined value so the column is both readable and greppable.
//
// A class with MORE THAN ONE standing approver carries its count — `human(2)` —
// because the underlying facts are per-approver (INV-APPROVAL-1) and collapsing
// two approvers back into one label at the last step would discard exactly what
// pg2-4dz88.1.9 made representable. The suffix is strictly ADDITIVE: with one
// approver per class the rendering is byte-identical to the pre-cutover one
// (`-`, `human`, `agent`, `human,agent`), and the class name still leads, so an
// existing `grep human` keeps matching.
func approvedCell(r openRow) string {
	var got []string
	if label := approverLabel("human", r.HumanApprovers); label != "" {
		got = append(got, label)
	}
	if label := approverLabel("agent", r.AgentApprovers); label != "" {
		got = append(got, label)
	}
	return orDash(strings.Join(got, ","))
}

// approverLabel renders one approver class: empty for none, the bare class name
// for exactly one, and the name plus a parenthesised count beyond that.
func approverLabel(class string, n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return class
	default:
		return class + "(" + strconv.Itoa(n) + ")"
	}
}

// sizeCell renders a PR's size as files/lines. Both are absent on a MineRow, in
// which case the cell collapses to a dash rather than a misleading "0f/0L".
func sizeCell(r openRow) string {
	if r.FilesChanged == 0 && r.LinesChanged == 0 {
		return "-"
	}
	return strconv.Itoa(r.FilesChanged) + "f/" + strconv.Itoa(r.LinesChanged) + "L"
}

// hiddenCell renders a row's hide state: "-" when not hidden, the recorded
// reason when one was given, or the literal "hidden" when it was hidden with
// no reason. Only reachable with --include-hidden — selectRows drops every
// hidden row otherwise, so this column reads "-" everywhere by default.
func hiddenCell(r openRow) string {
	if !r.Hidden {
		return "-"
	}
	if r.HiddenReason != "" {
		return r.HiddenReason
	}
	return "hidden"
}

func init() {
	openCmd.Flags().BoolVar(&opFlags.all, "all", false,
		"Widen to the whole set (the default already, with --mine)")
	openCmd.Flags().BoolVar(&opFlags.needsAttention, "needs-attention", false,
		"Narrow to the PRs needing attention (the default already, without --mine)")
	openCmd.Flags().BoolVar(&opFlags.mine, "mine", false,
		"Open your own PRs instead of the team's review set; defaults to all of them")
	openCmd.Flags().StringVar(&opFlags.reason, "reason", "",
		"Keep only PRs matched for this reason; exact or prefix (team-authored, review-requested, reviewed-by-me, assigned-to-me, label:team/lbl-one, label:)")
	openCmd.Flags().StringVar(&opFlags.owner, "owner", "",
		"Keep only PRs owned by this login")
	openCmd.Flags().StringVar(&opFlags.notOwner, "not-owner", "",
		"Drop PRs owned by this login")
	openCmd.Flags().BoolVar(&opFlags.unapproved, "unapproved", false,
		"Drop PRs a human has already approved")
	openCmd.Flags().BoolVar(&opFlags.includeHidden, "include-hidden", false,
		"Include PRs hidden via `pg-pr pr hide` (excluded by default, even with --all)")
	openCmd.Flags().IntVar(&opFlags.max, "max", 0,
		"Cap how many PRs are opened; 0 (the default) opens every match")
	openCmd.Flags().BoolVar(&opFlags.printOnly, "print", false,
		"List the selected PRs instead of opening a browser window")
	openCmd.Flags().BoolVar(&opFlags.noHyperlinks, "no-hyperlinks", false,
		"With --print, never emit OSC 8 terminal hyperlinks")
	openCmd.Flags().StringVar(&opFlags.addr, "addr", sync.DefaultMetricsAddr,
		"Address of the pg-pr sync daemon serving the dashboard endpoint")
	openCmd.Flags().BoolVar(&opFlags.jsonOutput, "json", false,
		"Emit the selection as a bare JSON array instead of opening a browser (also selected by PGPR_OUTPUT=json)")

	rootCmd.AddCommand(openCmd)
}
