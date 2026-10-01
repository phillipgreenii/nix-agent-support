package pipeline

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/threadref"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
)

// LinkExtractor derives links from one hydrated entity of a given type
// (Strategy per entity type). It returns links leaving that entity, each
// with Repo/FromType/FromID of the entity and ToType/ToID/Relation/Evidence
// set. Origin ("derived:<name>") and the first-seen/last-confirmed times are
// filled in by the hook from Name(), so an extractor cannot mislabel a link.
// Nothing here is shaped around PR-to-issue links: an extractor may link its
// entity to any type.
type LinkExtractor interface {
	Name() string
	Extract(entityType, entityID string, payload json.RawMessage) ([]store.XrefLink, error)
}

// ExtractorRegistry maps an entity type name to that type's extractors.
type ExtractorRegistry map[string][]LinkExtractor

// NewExtractorRegistry builds the minimum extractor set: work-item links and
// thread links for their types, and the Jira-key extractor for PRs. The
// thread extractor reads st at extraction time to resolve ticket keys.
func NewExtractorRegistry(cfg *config.Config, st *store.Store, repo string) ExtractorRegistry {
	return ExtractorRegistry{
		"pr":     {jiraKeyExtractor{patterns: cfg.TicketPatterns}},
		"issue":  {workItemExtractor{}},
		"thread": {threadExtractor{patterns: cfg.TicketPatterns, st: st, repo: repo}},
	}
}

// extractAndReplace runs the extractors of entityType and makes the union
// the entity's complete derived link set. A removed or empty payload passes
// an empty set, which drops the entity's derived links. Types without
// extractors are skipped. External and legacy rows are never touched.
func (p *Pipeline) extractAndReplace(entityType, entityID string, payload json.RawMessage, removed bool, now string) error {
	exts := p.extractors[entityType]
	if len(exts) == 0 {
		return nil
	}
	repo := p.repo()
	links := []store.XrefLink{}
	if !removed && len(payload) > 0 {
		for _, e := range exts {
			ls, err := e.Extract(entityType, entityID, payload)
			if err != nil {
				return fmt.Errorf("extractor %s: %w", e.Name(), err)
			}
			for _, l := range ls {
				l.Repo, l.FromType, l.FromID = repo, entityType, entityID
				l.Origin = "derived:" + e.Name()
				l.FirstSeen, l.LastConfirmed = now, now
				links = append(links, l)
			}
		}
	}
	return p.store.ReplaceDerivedXrefs(repo, entityType, entityID, links)
}

// workItemExtractor links a work item (type "issue") from its own 9.8
// metadata fields only (repo + pr_number) and its parent anchor; it never
// looks at titles or dedup keys. Direction: work item -> PR (relation
// "work") and child -> parent anchor (relation "parent").
type workItemExtractor struct{}

func (workItemExtractor) Name() string { return "work-item" }

func (workItemExtractor) Extract(_, _ string, payload json.RawMessage) ([]store.XrefLink, error) {
	var f struct {
		IssueShow json.RawMessage `json:"issue_show"`
	}
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, fmt.Errorf("decode issue payload: %w", err)
	}
	if len(f.IssueShow) == 0 {
		return nil, nil
	}
	var show struct {
		Parent   string            `json:"parent"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(f.IssueShow, &show); err != nil {
		return nil, fmt.Errorf("decode issue show: %w", err)
	}
	var out []store.XrefLink
	if r, n := show.Metadata["repo"], show.Metadata["pr_number"]; r != "" && n != "" {
		out = append(out, store.XrefLink{ToType: "pr", ToID: r + "#" + n, Relation: "work", Evidence: "metadata"})
	}
	if show.Parent != "" {
		out = append(out, store.XrefLink{ToType: "issue", ToID: show.Parent, Relation: "parent", Evidence: "parent"})
	}
	return out, nil
}

// jiraKeyExtractor links a PR to every Jira key in its branch, title or body
// (relation "jira"). Direction: PR -> issue, as the legacy scan wrote it.
type jiraKeyExtractor struct{ patterns []string }

func (jiraKeyExtractor) Name() string { return "jira-key" }

func (j jiraKeyExtractor) Extract(_, _ string, payload json.RawMessage) ([]store.XrefLink, error) {
	var f struct {
		PRShow json.RawMessage `json:"pr_show"`
	}
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, fmt.Errorf("decode pr payload: %w", err)
	}
	if len(f.PRShow) == 0 {
		return nil, nil
	}
	var show struct {
		Title  string `json:"title"`
		Branch string `json:"branch"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(f.PRShow, &show); err != nil {
		return nil, fmt.Errorf("decode pr show: %w", err)
	}
	var out []store.XrefLink
	for _, key := range ticketkey.Parse(show.Branch, show.Title, show.Body, j.patterns) {
		out = append(out, store.XrefLink{ToType: "issue", ToID: key, Relation: "jira", Evidence: "ticket-key"})
	}
	return out, nil
}

// threadExtractor links a thread to PRs named by permalink in its text, and
// to PRs already linked to a Jira key in its text (resolved from the store
// now; a key with no linked PR yet writes nothing). Direction: thread -> PR,
// relation "mentions".
type threadExtractor struct {
	patterns []string
	st       *store.Store
	repo     string
}

func (threadExtractor) Name() string { return "thread-refs" }

func (t threadExtractor) Extract(_, _ string, payload json.RawMessage) ([]store.XrefLink, error) {
	var f struct {
		ThreadShow json.RawMessage `json:"thread_show"`
	}
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, fmt.Errorf("decode thread payload: %w", err)
	}
	if len(f.ThreadShow) == 0 {
		return nil, nil
	}
	var show struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(f.ThreadShow, &show); err != nil {
		return nil, fmt.Errorf("decode thread show: %w", err)
	}
	var out []store.XrefLink
	for _, id := range threadref.ScanPermalinks(show.Text, t.repo) {
		out = append(out, store.XrefLink{ToType: "pr", ToID: id, Relation: "mentions", Evidence: "permalink"})
	}
	for _, key := range ticketkey.Parse(show.Text, "", "", t.patterns) {
		linked, err := t.st.ListXrefLinksTo(t.repo, "issue", key)
		if err != nil {
			return nil, fmt.Errorf("list links to ticket %s: %w", key, err)
		}
		for _, l := range linked {
			if l.FromType == "pr" {
				out = append(out, store.XrefLink{ToType: "pr", ToID: l.FromID, Relation: "mentions", Evidence: "ticket-key"})
			}
		}
	}
	return dedupLinks(out), nil
}

// dedupLinks drops repeated (to, relation) pairs, keeping the first.
func dedupLinks(in []store.XrefLink) []store.XrefLink {
	type k struct{ t, i, r string }
	seen := map[k]bool{}
	var out []store.XrefLink
	for _, l := range in {
		key := k{l.ToType, l.ToID, l.Relation}
		if !seen[key] {
			seen[key] = true
			out = append(out, l)
		}
	}
	return out
}
