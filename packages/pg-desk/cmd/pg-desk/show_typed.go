package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// viewContract names the composite view's JSON contract [design 9.5].
const viewContract = "pg-desk.view/v1"

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newTypedShowCmd(t))
	}
}

// typedShowFlags are the flags of one `pg-desk <type> show` command.
type typedShowFlags struct {
	refresh bool
	jsonOut bool
}

// newTypedShowCmd builds `pg-desk <type> show <id> [--json] [--refresh]`: the
// typed composite view [design 6.7, 9.5, 6.9]. It has no old-schema
// counterpart under this name (the top-level `show` keeps serving the old
// schema), so it refuses an old-schema store.
func newTypedShowCmd(entityType string) *cobra.Command {
	var f typedShowFlags
	c := &cobra.Command{
		Use:   "show <id>",
		Short: fmt.Sprintf("Print the composite view of one %s", entityType),
		Long: fmt.Sprintf(`Print the stored composite view of one %[1]s: its snapshot, decorations,
annotations and linked entities (one hop, each with its origins, state and key
metadata). --json prints the pg-desk.view/v1 contract.

--refresh first hydrates the %[1]s and then each directly linked entity that has a
stored row; a linked entity that fails to hydrate does not fail the command.
If the %[1]s's own hydration fails or degrades, the stored view is still printed
(marked stale) and the command then exits as refresh does: 2 when degraded, 3
when it failed entirely.`, entityType),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTypedShow(cmd, entityType, args[0], f)
		},
	}
	c.Flags().BoolVar(&f.refresh, "refresh", false, "Hydrate the entity and its links before printing")
	c.Flags().BoolVar(&f.jsonOut, "json", false, "Print the pg-desk.view/v1 JSON contract (also selected by PG_DESK_OUTPUT=json)")
	return c
}

// ---- JSON contract (schema 9.5) ----

type viewJSON struct {
	Contract    string          `json:"contract"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Version     int64           `json:"version"`
	AsOf        string          `json:"as_of"`
	Stale       bool            `json:"stale"`
	Snapshot    json.RawMessage `json:"snapshot"`
	Decorations viewDecorations `json:"decorations"`
	Annotations viewAnnotations `json:"annotations"`
	// Review is a PR's pending-agent-review state (pr only; omitted for the
	// other types).
	Review *viewReview `json:"review,omitempty"`
	Links  []viewLink  `json:"links"`
	// LinksAsOf is the newest time any of the entity's links was last
	// confirmed; null when it has no links.
	LinksAsOf *string `json:"links_as_of"`
	// CI is a PR's CI runs for its head commit (pr only; omitted for the
	// other types). It is the last member: an additive extension of
	// pg-desk.view/v1.
	CI *viewCI `json:"ci,omitempty"`
}

// viewCI is the ci section: raw stored CI run data with no computed verdict.
// A build id is a run's id plus its attempt.
type viewCI struct {
	Runs []viewCIRun `json:"runs"`
}

// viewCIRun is one run, named after pg-connector's schema.CIRun fields
// (which pg-desk does not import). Attempt is 0 when the backend reported
// none.
type viewCIRun struct {
	ID         string `json:"id"`
	Attempt    int    `json:"attempt"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type viewDecorations struct {
	Relationship string            `json:"relationship"`
	Dispositions []viewDisposition `json:"dispositions"`
	// Urgency is the stored urgency level ("" when the entity has no stored
	// interpretation).
	Urgency  string `json:"urgency"`
	Category string `json:"category"`
}

type viewDisposition struct {
	CommentID string  `json:"comment_id"`
	Computed  string  `json:"computed"`
	Override  *string `json:"override"`
}

type viewHidden struct {
	Value  bool    `json:"value"`
	Reason *string `json:"reason"`
}

type viewAnnotations struct {
	Hidden      viewHidden                   `json:"hidden"`
	WIP         bool                         `json:"wip"`
	Suppress    []string                     `json:"suppress"`
	ForceReview bool                         `json:"force_review"`
	Decider     map[string]map[string]string `json:"decider"`
}

// viewLink is one links[] entry. State, Labels, Metadata and Assignee are
// present only when the linked entity has a stored row; Assignee is "" when
// it is unclaimed. There is deliberately no closed_by [design 9.5, S26].
type viewLink struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Relation string             `json:"relation"`
	URL      string             `json:"url,omitempty"`
	State    string             `json:"state,omitempty"`
	Labels   *[]string          `json:"labels,omitempty"`
	Metadata *map[string]string `json:"metadata,omitempty"`
	Assignee *string            `json:"assignee,omitempty"`
	Origins  []viewOrigin       `json:"origins"`
}

type viewOrigin struct {
	Origin string `json:"origin"`
	At     string `json:"at,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// hiddenState is the decoded hidden annotation.
type hiddenState struct {
	Value  bool
	Reason string
}

// readHiddenAnnotation reads the new schema's hidden key/value annotation;
// an unset key is not hidden.
func readHiddenAnnotation(st *store.Store, repo, entityType, id string) (hiddenState, error) {
	a, found, err := st.GetKVAnnotation(repo, entityType, id, store.AnnotationHidden)
	if err != nil || !found {
		return hiddenState{}, err
	}
	var h struct {
		Value  bool    `json:"value"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(a.Value), &h); err != nil {
		return hiddenState{}, fmt.Errorf("decode hidden annotation: %w", err)
	}
	out := hiddenState{Value: h.Value}
	if h.Reason != nil {
		out.Reason = *h.Reason
	}
	return out, nil
}

// snapshotField is the facts key holding each type's pg-connector snapshot.
var snapshotField = map[string]string{
	entityTypePR:     "pr_show",
	entityTypeIssue:  "issue_show",
	entityTypeThread: "thread_show",
}

// viewData is a built view plus what the human rendering needs beyond it.
type viewData struct {
	view   viewJSON
	entity store.Entity
	hidden hiddenState
}

// buildView assembles the composite view of one stored entity from the store
// alone (no network). stale forces stale: true (a failed or degraded refresh).
func buildView(cfg *config.Config, st *store.Store, repo, entityType, id string, stale bool) (viewData, error) {
	ent, found, err := st.GetEntity(repo, entityType, id)
	if err != nil {
		return viewData{}, err
	}
	if !found {
		return viewData{}, fmt.Errorf("%s %s does not resolve to a stored entity", entityType, id)
	}
	v := viewJSON{
		Contract: viewContract,
		Type:     entityType,
		ID:       id,
		Version:  ent.Version,
		AsOf:     ent.AsOf,
		Stale:    ent.Stale || stale,
	}
	var facts map[string]json.RawMessage
	if json.Unmarshal([]byte(ent.Facts), &facts) == nil {
		if raw, ok := facts[snapshotField[entityType]]; ok {
			v.Snapshot = raw
		}
	}

	if entityType == entityTypePR {
		v.Review = buildReview(ent.Facts)
		v.CI = buildCI(ent, v.Snapshot)
	}

	anns, err := st.ListKVAnnotations(repo, entityType, id)
	if err != nil {
		return viewData{}, err
	}
	byKey := make(map[string]string, len(anns))
	v.Annotations = viewAnnotations{Suppress: []string{}, Decider: map[string]map[string]string{}}
	for _, a := range anns {
		byKey[a.Key] = a.Value
		switch {
		case strings.HasPrefix(a.Key, "suppress."):
			if a.Value == "true" {
				v.Annotations.Suppress = append(v.Annotations.Suppress, strings.TrimPrefix(a.Key, "suppress."))
			}
		case strings.HasPrefix(a.Key, "decider."):
			name, k, ok := strings.Cut(strings.TrimPrefix(a.Key, "decider."), ".")
			if !ok {
				continue
			}
			if v.Annotations.Decider[name] == nil {
				v.Annotations.Decider[name] = map[string]string{}
			}
			v.Annotations.Decider[name][k] = a.Value
		}
	}
	sort.Strings(v.Annotations.Suppress)
	hidden, err := readHiddenAnnotation(st, repo, entityType, id)
	if err != nil {
		return viewData{}, err
	}
	v.Annotations.Hidden = viewHidden{Value: hidden.Value}
	if hidden.Reason != "" {
		r := hidden.Reason
		v.Annotations.Hidden.Reason = &r
	}
	v.Annotations.WIP = byKey[store.AnnotationWIP] == "true"
	_, v.Annotations.ForceReview = byKey[store.AnnotationForceReview]

	v.Decorations = viewDecorations{Dispositions: []viewDisposition{}}
	interp, ifound, err := st.GetInterpretation(repo, entityType, id)
	if err != nil {
		return viewData{}, err
	}
	if ifound {
		v.Decorations.Relationship = interp.Ownership
		v.Decorations.Category = interp.Category
		var u struct {
			Level string `json:"level"`
		}
		_ = json.Unmarshal([]byte(interp.Urgency), &u)
		v.Decorations.Urgency = u.Level
		for _, d := range dispositionsFromInterpretation(interp.Dispositions) {
			vd := viewDisposition{CommentID: d.CommentID, Computed: d.Verdict}
			if o, has := byKey[store.KeyDisposition(d.CommentID)]; has && o != "" {
				vd.Override = &o
			}
			v.Decorations.Dispositions = append(v.Decorations.Dispositions, vd)
		}
	}

	det, err := links.ReadDetailed(links.Deps{
		Store:            st,
		Repo:             repo,
		Patterns:         cfg.TicketPatterns,
		IssueURLTemplate: cfg.Links.IssueURLTemplate,
	}, entityType, id)
	if err != nil {
		return viewData{}, err
	}
	v.Links = []viewLink{}
	for _, l := range det.Links {
		vl := viewLink{Type: l.Type, ID: l.ID, Relation: l.Relation, URL: l.URL, State: l.State, Origins: []viewOrigin{}}
		if l.Stored {
			labels := l.Labels
			if labels == nil {
				labels = []string{}
			}
			meta := l.Metadata
			if meta == nil {
				meta = map[string]string{}
			}
			assignee := l.Assignee
			vl.Labels, vl.Metadata, vl.Assignee = &labels, &meta, &assignee
		}
		for _, o := range l.Origins {
			vl.Origins = append(vl.Origins, viewOrigin{Origin: o.Origin, At: o.At, Reason: o.Reason})
		}
		v.Links = append(v.Links, vl)
	}
	if det.LinksAsOf != "" {
		a := det.LinksAsOf
		v.LinksAsOf = &a
	}
	return viewData{view: v, entity: ent, hidden: hidden}, nil
}

// buildCI reads the PR's stored CI facts into the ci section: the runs for
// the entity's current head. A run whose recorded head_sha differs from the
// head is excluded; one with no head_sha is kept (the stored listing is
// already the head commit's). With no CI data the runs array is empty, never
// null.
func buildCI(ent store.Entity, snapshot json.RawMessage) *viewCI {
	out := &viewCI{Runs: []viewCIRun{}}
	var facts struct {
		CI struct {
			Runs []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"url"`
				HeadSHA    string `json:"head_sha"`
				Attempt    int    `json:"attempt"`
			} `json:"runs"`
		} `json:"ci"`
		HeadSHA string `json:"head_sha"`
	}
	if json.Unmarshal([]byte(ent.Facts), &facts) != nil {
		return out
	}
	var snap struct {
		HeadSHA string `json:"head_sha"`
	}
	_ = json.Unmarshal(snapshot, &snap)
	head := snap.HeadSHA
	if head == "" {
		head = facts.HeadSHA
	}
	if head == "" {
		head = ent.HeadSHA
	}
	for _, r := range facts.CI.Runs {
		if r.HeadSHA != "" && head != "" && r.HeadSHA != head {
			continue
		}
		out.Runs = append(out.Runs, viewCIRun{ID: r.ID, Attempt: r.Attempt, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL})
	}
	return out
}

// ---- command ----

func runTypedShow(cmd *cobra.Command, entityType, ref string, f typedShowFlags) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("show: load config: %w", err)
	}
	repo, id, err := resolveTypedRef(cfg, entityType, ref)
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}
	st, err := openNewSchemaStore("show")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	var exitErr error
	stale := false
	if f.refresh {
		exitErr = refreshForShow(cmd, cfg, st, repo, entityType, id)
		stale = exitErr != nil
	}

	vd, err := buildView(cfg, st, repo, entityType, id, stale)
	if err != nil {
		if exitErr != nil {
			return exitErr
		}
		return fmt.Errorf("show: %w", err)
	}
	if resolveJSONOutput(f.jsonOut) {
		b, merr := json.MarshalIndent(vd.view, "", "  ")
		if merr != nil {
			return fmt.Errorf("show: marshal json: %w", merr)
		}
		if _, werr := fmt.Fprintln(cmd.OutOrStdout(), string(b)); werr != nil {
			return werr
		}
	} else if rerr := renderTypedShow(cmd.OutOrStdout(), vd); rerr != nil {
		return rerr
	}
	return exitErr
}

// refreshForShow hydrates the entity and then each directly linked entity
// that has a stored row, all through refreshEntity. Only the primary entity's
// outcome is returned (as an exit error carrying refresh's codes: 2 degraded,
// 3 failed); a linked entity that fails is reported on stderr and ignored.
func refreshForShow(cmd *cobra.Command, cfg *config.Config, st *store.Store, repo, entityType, id string) error {
	var primary error
	res, err := refreshEntity(cmd.Context(), cfg, st, entityType, id)
	switch {
	case err != nil:
		primary = newExitError(exitTotal, fmt.Errorf("show: refresh %s %s: %w", entityType, id, notFoundLoud(err)))
	case res.Degraded != "":
		primary = newExitError(exitPartial, fmt.Errorf("show: refresh %s %s: hydration degraded (previous detail kept): %s", entityType, id, res.Degraded))
	}
	det, derr := links.ReadDetailed(links.Deps{Store: st, Repo: repo, Patterns: cfg.TicketPatterns, IssueURLTemplate: cfg.Links.IssueURLTemplate}, entityType, id)
	if derr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: show: read links of %s %s: %v\n", entityType, id, derr)
		return primary
	}
	for _, l := range det.Links {
		if !l.Stored {
			continue
		}
		if _, lerr := refreshEntity(cmd.Context(), cfg, st, l.Type, l.ID); lerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: show: refresh linked %s %s: %v\n", l.Type, l.ID, lerr)
		}
	}
	return primary
}

// ---- human rendering [design 6.9] ----

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func renderTypedShow(w io.Writer, vd viewData) error {
	v := vd.view
	title := changes.TitleFromFacts(v.Type, vd.entity.Facts)
	line1 := fmt.Sprintf("%s %s", v.Type, v.ID)
	if title != "" {
		line1 += "  " + title
	}
	freshness := "fresh"
	if v.Stale {
		freshness = "stale"
	}
	parts := []string{orDash(v.Decorations.Relationship)}
	var snap struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Draft  bool   `json:"draft"`
		Head   string `json:"head_sha"`
	}
	_ = json.Unmarshal(v.Snapshot, &snap)
	state := snap.State
	if snap.Merged {
		state = "merged"
	}
	parts = append(parts, orDash(state))
	if v.Type == entityTypePR {
		ready := "ready"
		if snap.Draft {
			ready = "draft"
		}
		head := snap.Head
		if head == "" {
			head = vd.entity.HeadSHA
		}
		if len(head) > 7 {
			head = head[:7]
		}
		var facts struct {
			CI json.RawMessage `json:"ci"`
		}
		_ = json.Unmarshal([]byte(vd.entity.Facts), &facts)
		parts = append(parts, ready, "head="+orDash(head), "ci="+deskCIStatus(facts.CI))
	}
	parts = append(parts, fmt.Sprintf("as_of=%s (%s)", orDash(v.AsOf), freshness))

	hidden := yesNo(v.Annotations.Hidden.Value)
	if v.Annotations.Hidden.Reason != nil {
		hidden += " (" + *v.Annotations.Hidden.Reason + ")"
	}
	anns := fmt.Sprintf("annotations: hidden=%s  wip=%s  suppress=[%s]",
		hidden, yesNo(v.Annotations.WIP), strings.Join(v.Annotations.Suppress, ","))

	reviewLine := ""
	if v.Review != nil {
		reviewLine = renderReviewLine(v.Review) + "\n"
	}

	linkTexts := make([]string, 0, len(v.Links))
	for _, l := range v.Links {
		detail := l.Relation
		if l.State != "" {
			detail += ", " + l.State
		}
		linkTexts = append(linkTexts, fmt.Sprintf("%s (%s)", l.ID, detail))
	}
	linksLine := "links: none"
	if len(linkTexts) > 0 {
		linksLine = "links: " + strings.Join(linkTexts, "  ")
	}
	_, err := fmt.Fprintf(w, "%s\n%s\n%s\n%s%s\n", line1, strings.Join(parts, "  "), anns, reviewLine, linksLine)
	return err
}
