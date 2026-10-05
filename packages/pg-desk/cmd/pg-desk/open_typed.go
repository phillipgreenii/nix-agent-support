package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// typedOpenNow is the clock the as_of staleness is measured against; tests
// replace it.
var typedOpenNow = time.Now

// typedOpenWindow opens the browser window; tests replace it.
var typedOpenWindow = browser.OpenWindow

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newTypedOpenCmd(t))
	}
}

// newTypedOpenCmd builds `pg-desk <type> open [<id>]` [design 6.9]: with an id
// it handles that one entity, without it the list selected from the local
// snapshots, each with its as_of staleness. `pr open` keeps every flag and
// behavior of the top-level open; issue and thread take only the generic
// flags (--max, --print, --json, --include-hidden). See
// docs/behavior/pg-desk/open.md for what is and is not yet designed.
func newTypedOpenCmd(entityType string) *cobra.Command {
	var f openFlags
	c := &cobra.Command{
		Use:   "open [<id>]",
		Short: fmt.Sprintf("Open the %ss from the local snapshots in a new browser window", entityType),
		Long: fmt.Sprintf(`Open %[1]ss selected from the local snapshots as ONE new browser window, one tab
per %[1]s. With an <id>, open (or with --print/--json, list) just that one
entity, whatever the selection flags say. Every listed entity carries its
as_of staleness: how long ago its snapshot was taken.

Reads the store only. Refuses a store not yet cut over to the new schema; the
top-level 'pg-desk open' keeps serving the old one.`, entityType),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			return runTypedOpen(cmd, entityType, ref, f)
		},
	}
	if entityType == entityTypePR {
		bindOpenFlags(c, &f)
		return c
	}
	noun := entityType + "s"
	c.Flags().BoolVar(&f.includeHidden, "include-hidden", false,
		"Include "+noun+" hidden via `pg-desk "+entityType+" hide` (excluded by default)")
	c.Flags().IntVar(&f.max, "max", 0, "Cap how many "+noun+" are opened; 0 (the default) opens every match")
	c.Flags().BoolVar(&f.printOnly, "print", false, "List the selected "+noun+" instead of opening a browser window")
	c.Flags().BoolVar(&f.jsonOutput, "json", false,
		"Emit the selection as a bare JSON array instead of opening a browser (also selected by PG_DESK_OUTPUT=json)")
	return c
}

func runTypedOpen(cmd *cobra.Command, entityType, ref string, f openFlags) error {
	if err := validateOpenFlags(f); err != nil {
		return err
	}
	jsonMode := resolveJSONOutput(f.jsonOutput)
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("open: load config: %w", err)
	}
	st, err := openNewSchemaStore("open")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	var rows []openRow
	if ref != "" {
		repo, id, rerr := resolveTypedRef(cfg, entityType, ref)
		if rerr != nil {
			return fmt.Errorf("open: %w", rerr)
		}
		row, rerr := typedOpenOne(st, entityType, repo, id)
		if rerr != nil {
			return rerr
		}
		rows = []openRow{row}
	} else if rows, err = typedOpenList(st, cfg, entityType, f); err != nil {
		return err
	}

	now := typedOpenNow()
	noun := entityType + "s"
	if entityType == entityTypePR {
		noun = "PRs"
	}
	if len(rows) == 0 {
		if jsonMode {
			return writeTypedOpenJSON(cmd.OutOrStdout(), entityType, rows, false, now)
		}
		_, werr := fmt.Fprintf(cmd.OutOrStdout(), "(no %s match)\n", noun)
		return werr
	}
	truncated := f.max > 0 && len(rows) > f.max
	if truncated {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d %s matched, showing the first %d (--max)\n", len(rows), noun, f.max)
		rows = rows[:f.max]
	}
	switch {
	case jsonMode:
		return writeTypedOpenJSON(cmd.OutOrStdout(), entityType, rows, truncated, now)
	case f.printOnly:
		return renderTypedOpenRows(cmd.OutOrStdout(), entityType, rows, useHyperlinks(cmd.OutOrStdout(), f), now)
	}
	urls := make([]string, 0, len(rows))
	for _, u := range urlsOf(rows) {
		if u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return fmt.Errorf("open: none of the selected %s has a URL to open", noun)
	}
	return typedOpenWindow(urls, cfg.Open.ChromeBin)
}

// typedOpenOne builds the single row of the id form. It ignores the selection
// flags: naming the entity is the selection.
func typedOpenOne(st *store.Store, entityType, repo, id string) (openRow, error) {
	if entityType == entityTypePR {
		interp, found, err := st.GetInterpretation(repo, entityTypePR, id)
		if err != nil {
			return openRow{}, fmt.Errorf("open: %w", err)
		}
		if !found {
			return openRow{}, fmt.Errorf("open: %s %s does not resolve to a stored entity", entityType, id)
		}
		row, err := openRowFrom(st, interp, true)
		if err != nil {
			return openRow{}, fmt.Errorf("open: %w", err)
		}
		actNow := panelTeamAwaitingMe
		if actsAsMine(interp.Ownership) {
			row.Owner = ""
			actNow = panelMineAwaitingMe
		}
		row.NeedsAttention = interp.Panel == actNow
		return row, nil
	}
	ent, found, err := st.GetEntity(repo, entityType, id)
	if err != nil {
		return openRow{}, fmt.Errorf("open: %w", err)
	}
	if !found {
		return openRow{}, fmt.Errorf("open: %s %s does not resolve to a stored entity", entityType, id)
	}
	return genericOpenRow(st, ent)
}

// typedOpenList builds the list form's rows. A PR list is the old open's
// selection (same halves, same filters) read through the new schema; an issue
// or thread list is every active stored entity of the type, hidden ones
// excluded unless --include-hidden.
func typedOpenList(st *store.Store, cfg *config.Config, entityType string, f openFlags) ([]openRow, error) {
	if entityType == entityTypePR {
		mine, team, err := buildOpenRowsFor(st, cfg, true)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}
		candidates := team
		if f.mine {
			candidates = mine
		}
		return selectRows(candidates, f), nil
	}
	ents, err := st.ListEntities()
	if err != nil {
		return nil, fmt.Errorf("open: list entities: %w", err)
	}
	var rows []openRow
	for _, e := range ents {
		if e.EntityType != entityType || e.Inactive {
			continue
		}
		row, rerr := genericOpenRow(st, e)
		if rerr != nil {
			return nil, rerr
		}
		if row.Hidden && !f.includeHidden {
			continue
		}
		rows = append(rows, row)
	}
	sortOpenRowsByID(rows)
	return rows, nil
}

func genericOpenRow(st *store.Store, ent store.Entity) (openRow, error) {
	h, err := readHiddenAnnotation(st, ent.Repo, ent.EntityType, ent.EntityID)
	if err != nil {
		return openRow{}, fmt.Errorf("open: %w", err)
	}
	return openRow{
		Title:        changes.TitleFromFacts(ent.EntityType, ent.Facts),
		URL:          links.SnapshotURL(ent.EntityType, ent.Facts),
		Hidden:       h.Value,
		HiddenReason: h.Reason,
		EntityID:     ent.EntityID,
		AsOf:         ent.AsOf,
	}, nil
}

// ---- rendering ----

// ageOf is the as_of staleness: a short "2m ago" text, and the age in whole
// seconds (nil when asOf is empty or unparsable).
func ageOf(asOf string, now time.Time) (string, *int64) {
	t, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return "-", nil
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	secs := int64(d / time.Second)
	var text string
	switch {
	case secs < 60:
		text = strconv.FormatInt(secs, 10) + "s ago"
	case secs < 3600:
		text = strconv.FormatInt(secs/60, 10) + "m ago"
	case secs < 86400:
		text = strconv.FormatInt(secs/3600, 10) + "h ago"
	default:
		text = strconv.FormatInt(secs/86400, 10) + "d ago"
	}
	return text, &secs
}

// typedPROpenJSON is `pr open --json`'s row: the old open's row plus the
// entity id and its as_of staleness.
type typedPROpenJSON struct {
	openJSONRow
	ID         string `json:"id"`
	AsOf       string `json:"as_of"`
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
}

// typedEntityOpenJSON is `issue|thread open --json`'s row.
type typedEntityOpenJSON struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Hidden       bool   `json:"hidden"`
	HiddenReason string `json:"hidden_reason"`
	AsOf         string `json:"as_of"`
	AgeSeconds   *int64 `json:"age_seconds,omitempty"`
	Truncated    bool   `json:"truncated"`
}

func writeTypedOpenJSON(w io.Writer, entityType string, rows []openRow, truncated bool, now time.Time) error {
	var out any
	if entityType == entityTypePR {
		base := openJSONRows(rows, truncated)
		res := make([]typedPROpenJSON, 0, len(rows))
		for i, r := range rows {
			_, age := ageOf(r.AsOf, now)
			res = append(res, typedPROpenJSON{openJSONRow: base[i], ID: r.EntityID, AsOf: r.AsOf, AgeSeconds: age})
		}
		out = res
	} else {
		res := make([]typedEntityOpenJSON, 0, len(rows))
		for _, r := range rows {
			_, age := ageOf(r.AsOf, now)
			res = append(res, typedEntityOpenJSON{
				ID: r.EntityID, Title: r.Title, URL: r.URL, Hidden: r.Hidden,
				HiddenReason: r.HiddenReason, AsOf: r.AsOf, AgeSeconds: age, Truncated: truncated,
			})
		}
		out = res
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("open: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// renderTypedOpenRows writes the human listing: the old open's columns for a
// PR plus AS_OF, and ID/HIDDEN/AS_OF/URL/TITLE for an issue or thread.
func renderTypedOpenRows(w io.Writer, entityType string, rows []openRow, link bool, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	pr := entityType == entityTypePR
	var header string
	switch {
	case pr && link:
		header = "PR\tOWNER\tCI\tAPPROVED\tSIZE\tHIDDEN\tAS_OF\tTITLE\n"
	case pr:
		header = "PR\tOWNER\tCI\tAPPROVED\tSIZE\tHIDDEN\tAS_OF\tURL\tTITLE\n"
	case link:
		header = "ID\tHIDDEN\tAS_OF\tTITLE\n"
	default:
		header = "ID\tHIDDEN\tAS_OF\tURL\tTITLE\n"
	}
	if _, err := io.WriteString(tw, header); err != nil {
		return err
	}
	for _, r := range rows {
		age, _ := ageOf(r.AsOf, now)
		var cells []string
		if pr {
			cells = []string{"#" + strconv.Itoa(r.Number), orDash(r.Owner), orDash(r.CIStatus), approvedCell(r), sizeCell(r), hiddenCell(r), age}
		} else {
			cells = []string{r.EntityID, hiddenCell(r), age}
		}
		switch {
		case link && r.URL != "":
			cells = append(cells, hyperlink(r.URL, r.Title))
		case link:
			cells = append(cells, r.Title)
		default:
			cells = append(cells, orDash(r.URL), r.Title)
		}
		if _, err := io.WriteString(tw, strings.Join(cells, "\t")+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// sortOpenRowsByID orders generic rows by entity id for a stable listing.
func sortOpenRowsByID(rows []openRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].EntityID < rows[j].EntityID })
}
