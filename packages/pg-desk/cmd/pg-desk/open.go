package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// openFlags holds the flags for `pg-desk open`. Flags/defaults are PINNED by
// the ported open_test.go/open_json_test.go goldens [docs/behavior/pg-desk/open.md]:
// --promotable is new this phase, --addr is dropped (open reads the store
// directly — no daemon).
type openFlags struct {
	all            bool
	needsAttention bool
	mine           bool
	reason         string
	owner          string
	notOwner       string
	unapproved     bool
	includeHidden  bool
	promotable     bool
	max            int
	printOnly      bool
	noHyperlinks   bool
	jsonOutput     bool
}

var opFlags openFlags

// openRow is the normalized view of one selectable PR — the pg-desk analog
// of pg-pr's cmd/pg-pr/open.go openRow (this packet's porting source), built
// from the STORE directly (buildOpenRows below) instead of a running
// daemon's dashboard snapshot, since `pg-desk open` has no daemon to poll
// [docs/behavior/pg-desk/open.md: "reading the store directly with no
// daemon required"].
type openRow struct {
	Number         int
	Owner          string
	Title          string
	URL            string
	CIStatus       string
	HumanApprovers int
	// AgentApprovers is always 0: this docket's interpret stage has no agent
	// registry to classify an approver as anything but human (see
	// internal/interpret's own package doc comment, "No agent registry").
	// Carried on the row and in --json for shape parity with the porting
	// source, and so a later phase that DOES gain an agent registry needs no
	// shape change here.
	AgentApprovers int
	FilesChanged   int
	LinesChanged   int
	NeedsAttention bool
	MatchReason    []string
	Hidden         bool
	HiddenReason   string
	// ReadyToPromote mirrors the stored interpretation's own ready_to_promote
	// flag (D15's promotion predicate) — surfaced so --promotable can filter
	// on it without recomputing anything.
	ReadyToPromote bool
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the PRs needing your review in a new browser window",
	Long: `Open the pull requests that currently need your review as ONE new browser
window, with one tab per PR, in your existing browser profile.

The review set is read directly from the store's interpretation table — no
daemon required. The attention filter defaults per source: the team's
review set defaults to just the PRs needing attention, while --mine defaults
to ALL of your own PRs. Override in whichever direction the default
forecloses — --all widens, --needs-attention narrows.

Pass --print to list the selection instead of opening a browser. Pass --json
(or set PG_DESK_OUTPUT=json) to emit the selection as a bare JSON array
instead. A PR the operator hid ('pg-desk hide') is excluded by default, even
with --all; pass --include-hidden to see it anyway. --promotable narrows to
PRs the store marked ready_to_promote (D15's own PR-facing lever, since
pg-desk never converts a draft to ready automatically).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateOpenFlags(opFlags); err != nil {
			return err
		}
		jsonMode := resolveJSONOutput(opFlags.jsonOutput)

		cfg, err := deskConfigLoad(cmd.Context())
		if err != nil {
			return fmt.Errorf("open: load config: %w", err)
		}
		st, err := deskStoreOpen()
		if err != nil {
			return fmt.Errorf("open: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		mineRows, teamRows, err := buildOpenRows(st, cfg)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		var candidates []openRow
		if opFlags.mine {
			candidates = mineRows
		} else {
			candidates = teamRows
		}

		rows := selectRows(candidates, opFlags)
		if len(rows) == 0 {
			if jsonMode {
				return writeJSON(cmd.OutOrStdout(), openJSONRows(rows, false))
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
			return writeJSON(cmd.OutOrStdout(), openJSONRows(rows, truncated))
		}

		if opFlags.printOnly {
			return renderOpenRows(cmd.OutOrStdout(), rows, useHyperlinks(cmd.OutOrStdout(), opFlags))
		}
		return browser.OpenWindow(urlsOf(rows), cfg.Open.ChromeBin)
	},
}

// buildOpenRows reads every "pr" interpretation from the store and projects
// it onto the mine/team openRow halves, joining the entity table (for
// display facts: title/url/number/author/CI/size) and the annotation table
// (hidden state) at read time — mirroring the design's own "hidden is
// joined at read time" rule [docs/behavior/pg-desk/hide-unhide-wip.md] for
// this packet's own read path.
func buildOpenRows(st *store.Store, cfg *config.Config) (mine, team []openRow, err error) {
	interps, err := st.ListInterpretations()
	if err != nil {
		return nil, nil, fmt.Errorf("list interpretations: %w", err)
	}

	for _, interp := range interps {
		if interp.EntityType != entityTypePR {
			continue
		}

		// PanelNone ("") means this entity is not currently admitted to any
		// of the five named panels (a merged PR of mine, or a draft/
		// reasonless team PR) — internal/interpret's own classifyPanel doc
		// comment. Such a row is excluded from `open` exactly as it is
		// excluded from `serve`'s dashboard payload.
		var actNowPanel string
		mineHalf := actsAsMine(interp.Ownership)
		if mineHalf {
			if interp.Panel != panelMineAwaitingMe && interp.Panel != panelMineAwaitingTeam {
				continue
			}
			actNowPanel = panelMineAwaitingMe
		} else {
			if interp.Panel != panelTeamAwaitingMe && interp.Panel != panelTeamAwaitingTeam && interp.Panel != panelTeamAwaitingOwner {
				continue
			}
			actNowPanel = panelTeamAwaitingMe
		}

		entity, found, gerr := st.GetEntity(interp.Repo, interp.EntityType, interp.EntityID)
		if gerr != nil {
			return nil, nil, fmt.Errorf("get entity (%s,%s,%s): %w", interp.Repo, interp.EntityType, interp.EntityID, gerr)
		}
		var facts struct {
			PRShow  json.RawMessage `json:"pr_show,omitempty"`
			PRFiles json.RawMessage `json:"pr_files,omitempty"`
			CI      json.RawMessage `json:"ci,omitempty"`
		}
		if found {
			_ = json.Unmarshal([]byte(entity.Facts), &facts)
		}
		pr, _ := decodeDeskPRShow(facts.PRShow)

		var approvals struct {
			HumanApprovers int `json:"human_approvers"`
		}
		_ = json.Unmarshal([]byte(interp.Approvals), &approvals)

		var matchReasons []string
		_ = json.Unmarshal([]byte(interp.MatchReasons), &matchReasons)

		hidden := false
		hiddenReason := ""
		if ann, found, aerr := st.GetPRAnnotation(interp.Repo, interp.EntityType, interp.EntityID); aerr == nil && found {
			if ann.Hidden != nil {
				hidden = *ann.Hidden
			}
			hiddenReason = ann.HiddenReason
		}

		row := openRow{
			Number:         pr.Number,
			Title:          pr.Title,
			URL:            pr.URL,
			CIStatus:       deskCIStatus(facts.CI),
			HumanApprovers: approvals.HumanApprovers,
			FilesChanged:   deskFilesCount(facts.PRFiles),
			LinesChanged:   pr.Additions + pr.Deletions,
			NeedsAttention: interp.Panel == actNowPanel,
			MatchReason:    matchReasons,
			Hidden:         hidden,
			HiddenReason:   hiddenReason,
			ReadyToPromote: interp.ReadyToPromote,
		}
		if row.Number == 0 {
			if n, cerr := strconv.Atoi(interp.EntityID); cerr == nil {
				row.Number = n
			}
		}

		if mineHalf {
			mine = append(mine, row)
		} else {
			row.Owner = pr.Author
			team = append(team, row)
		}
	}
	return mine, team, nil
}

// attentionOnly decides whether the needs-attention filter applies. Ported
// verbatim from pg-pr's cmd/pg-pr/open.go: the default is PER SOURCE — team
// defaults to attention-only, --mine defaults to everything — overridable in
// the direction that default forecloses.
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
// nothing, or that ask for two contradictory things. Ported verbatim from
// pg-pr's cmd/pg-pr/open.go (pg2-4dz88.7.7's decided rejections).
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

// selectRows applies every filter, preserving the input's own ordering. It
// does NOT apply --max: truncation warns on stderr, which is the caller's
// job. Ported from pg-pr's cmd/pg-pr/open.go, plus --promotable (new this
// phase).
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
		case f.promotable && !r.ReadyToPromote:
		default:
			out = append(out, r)
		}
	}
	return out
}

// hasReason matches want against a row's match reasons as an exact value OR
// a prefix. Ported verbatim from pg-pr's cmd/pg-pr/open.go.
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

// useHyperlinks reports whether the listing should emit OSC 8 hyperlinks.
// Ported verbatim from pg-pr's cmd/pg-pr/open.go.
func useHyperlinks(w io.Writer, f openFlags) bool {
	return !f.noHyperlinks && isTTY(w)
}

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

func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// renderOpenRows writes the human listing. Ported verbatim from pg-pr's
// cmd/pg-pr/open.go.
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

// approvedCell reports which classes of reviewer have approved. Ported from
// pg-pr's cmd/pg-pr/open.go; AgentApprovers is always 0 here (see openRow's
// doc comment) but the rendering is kept shape-identical.
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

func sizeCell(r openRow) string {
	if r.FilesChanged == 0 && r.LinesChanged == 0 {
		return "-"
	}
	return strconv.Itoa(r.FilesChanged) + "f/" + strconv.Itoa(r.LinesChanged) + "L"
}

func hiddenCell(r openRow) string {
	if !r.Hidden {
		return "-"
	}
	if r.HiddenReason != "" {
		return r.HiddenReason
	}
	return "hidden"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// openJSONRow is the JSON shape emitted per selected row by
// `pg-desk open --json`.
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
	ReadyToPromote bool     `json:"ready_to_promote"`
	Truncated      bool     `json:"truncated"`
}

// openJSONRows projects rows into their --json shape, in the same order
// selectRows produced them.
func openJSONRows(rows []openRow, truncated bool) []openJSONRow {
	out := make([]openJSONRow, 0, len(rows))
	for _, r := range rows {
		matchReason := r.MatchReason
		if matchReason == nil {
			matchReason = []string{}
		}
		out = append(out, openJSONRow{
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
			MatchReason:    matchReason,
			Hidden:         r.Hidden,
			HiddenReason:   r.HiddenReason,
			ReadyToPromote: r.ReadyToPromote,
			Truncated:      truncated,
		})
	}
	return out
}

// writeJSON marshals v as indented JSON and writes it (plus a trailing
// newline) to w. rows is always non-nil coming from openJSONRows (make with
// cap 0 still yields a non-nil, empty slice), so an empty selection encodes
// as a bare "[]", never "null".
func writeJSON(w io.Writer, rows []openJSONRow) error {
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("open: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func init() {
	openCmd.Flags().BoolVar(&opFlags.all, "all", false,
		"Widen to the whole set (the default already, with --mine)")
	openCmd.Flags().BoolVar(&opFlags.needsAttention, "needs-attention", false,
		"Narrow to the PRs needing attention (the default already, without --mine)")
	openCmd.Flags().BoolVar(&opFlags.mine, "mine", false,
		"Open your own PRs instead of the team's review set; defaults to all of them")
	openCmd.Flags().StringVar(&opFlags.reason, "reason", "",
		"Keep only PRs matched for this reason; exact or prefix")
	openCmd.Flags().StringVar(&opFlags.owner, "owner", "",
		"Keep only PRs owned by this login")
	openCmd.Flags().StringVar(&opFlags.notOwner, "not-owner", "",
		"Drop PRs owned by this login")
	openCmd.Flags().BoolVar(&opFlags.unapproved, "unapproved", false,
		"Drop PRs a human has already approved")
	openCmd.Flags().BoolVar(&opFlags.includeHidden, "include-hidden", false,
		"Include PRs hidden via `pg-desk hide` (excluded by default, even with --all)")
	openCmd.Flags().BoolVar(&opFlags.promotable, "promotable", false,
		"Keep only PRs the store marked ready to promote (D15)")
	openCmd.Flags().IntVar(&opFlags.max, "max", 0,
		"Cap how many PRs are opened; 0 (the default) opens every match")
	openCmd.Flags().BoolVar(&opFlags.printOnly, "print", false,
		"List the selected PRs instead of opening a browser window")
	openCmd.Flags().BoolVar(&opFlags.noHyperlinks, "no-hyperlinks", false,
		"With --print, never emit OSC 8 terminal hyperlinks")
	openCmd.Flags().BoolVar(&opFlags.jsonOutput, "json", false,
		"Emit the selection as a bare JSON array instead of opening a browser (also selected by PG_DESK_OUTPUT=json)")

	rootCmd.AddCommand(openCmd)
}
