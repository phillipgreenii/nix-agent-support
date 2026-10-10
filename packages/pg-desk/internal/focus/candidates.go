package focus

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Candidates computes the candidate set of the focus rank: every non-done
// item that is a SEED or is joined to a seed by one link, after the
// exclusions. It is pure: a deterministic function of in, with no tracker
// call, no clock read (in.Now is the caller's reading), no store access and
// no write. The set decides who may ENTER a plan, never who stays (the plan
// rows are not an input to the decision).
//
// Seeds:
//   - a PR that is open and either carries ownership mine or co-owned, or
//     lists the configured self login among its review requests;
//   - a Jira issue (an issue whose id does not match bead_id_pattern) whose
//     assignee, trimmed of surrounding whitespace, equals one of the
//     configured focus.operator_identities exactly and case-sensitively;
//   - a bead (an issue whose id matches bead_id_pattern) that carries
//     PlanableLabel and is open, in_progress or blocked.
//
// Linked candidates: an issue or pr entity joined to a seed by one stored
// link of relation jira, work, parent, mentions, a PR's depends_on or an
// external references, in either direction. The reach is one hop and never
// transitive. A ci or self link, a link of any other relation, and a link to
// an entity that is not an issue or pr in the store yield nothing.
//
// Exclusions, applied to seeds and linked candidates alike (a hidden or
// excluded seed seeds nothing): an inactive or hidden entity, an issue whose
// own metadata carries MetaSourceID (a minted focus bead) or MetaDedupKey (a
// work-item bead), a deferred bead, a terminal entity, and an absorbed key.
//
// Telemetry: Candidates emits no OpenTelemetry or Prometheus signal and logs
// nothing.
func Candidates(in Inputs) CandidateSet {
	a := analyze(in)
	var set CandidateSet
	for _, v := range a.sorted {
		if !v.candidate {
			continue
		}
		c := Candidate{Key: v.key, Kind: v.kind, Seed: v.seed, Via: v.via}
		set.Candidates = append(set.Candidates, c)
		switch v.kind {
		case KindPR:
			set.Counts.PR++
		case KindJira:
			set.Counts.Jira++
		case KindBead:
			set.Counts.Bead++
		}
		if !v.seed {
			set.Counts.Linked++
		}
	}
	beadSeeds := 0
	for _, v := range a.sorted {
		if v.kind != KindBead || !v.eligible || !beadStatusOpenish(v.state) {
			continue
		}
		set.Counts.OpenBeads++
		if !v.labelled {
			set.Counts.UnlabelledOpenBeads++
		}
		if v.seed {
			beadSeeds++
		}
	}
	if len(a.identities) == 0 {
		set.Notices = append(set.Notices, Notice{
			Code: NoticeEmptyIdentityList,
			Text: "focus.operator_identities is empty: no Jira issue is a candidate by assignment",
		})
	}
	if a.beadRe == nil {
		set.Notices = append(set.Notices, Notice{
			Code: NoticeNoBeadPattern,
			Text: "bead_id_pattern is not set: no issue is a bead, so no bead is a candidate",
		})
	} else if beadSeeds == 0 {
		set.Notices = append(set.Notices, Notice{
			Code: NoticeNoBeadCandidates,
			Text: "no open bead carries the label " + PlanableLabel,
		})
	}
	return set
}

// ExplainCandidate answers "why is this key (not) a candidate". It is
// computed by the same predicates Candidates uses, so the two cannot
// disagree. When the key is not a candidate, Reasons draws only from the
// closed Reason list: the exclusions that apply (in the order inactive,
// hidden, minted-bead, work-item-bead, deferred, terminal, absorbed), else
// not-seed-or-linked; a key with no stored entity is not-in-store.
func ExplainCandidate(in Inputs, key Key) Explanation {
	a := analyze(in)
	v, ok := a.views[key]
	if !ok {
		return Explanation{Reasons: []string{ReasonNotInStore}}
	}
	if v.candidate {
		return Explanation{IsCandidate: true, Seed: v.seed, Via: v.via}
	}
	if len(v.reasons) > 0 {
		return Explanation{Reasons: append([]string(nil), v.reasons...)}
	}
	return Explanation{Reasons: []string{ReasonNotSeedOrLinked}}
}

// keyLess orders keys by entity type, then id.
func keyLess(a, b Key) bool {
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	return a.ID < b.ID
}

// candLess is the candidate order: by kind (pr, jira, bead), then by key.
func candLess(a, b *view) bool {
	if a.kind.order() != b.kind.order() {
		return a.kind.order() < b.kind.order()
	}
	return keyLess(a.key, b.key)
}

func beadStatusOpenish(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case statusOpen, statusInProg, statusBlocked:
		return true
	}
	return false
}

// view is everything the predicates decide about one stored pr or issue
// entity.
type view struct {
	key  Key
	kind Kind

	// decoded facts
	state          string
	merged         bool
	labels         []string
	metadata       map[string]json.RawMessage
	assignee       string
	reviewRequests []string
	labelled       bool

	// issue-only facts the slot rule and the correlation groups read
	parent    string
	issueType string
	deps      []issueDep
	dueDate   string
	priority  string
	createdAt string

	reasons   []string // the exclusions that apply, in the documented order
	eligible  bool     // no exclusion applies
	seed      bool     // satisfies a seed rule and is eligible
	candidate bool     // seed, or eligible and joined to a seed
	via       *Via     // set for a linked-only candidate
}

// analysis is the shared evaluation Candidates and ExplainCandidate read.
type analysis struct {
	views      map[Key]*view
	sorted     []*view // pr and issue views in candidate order
	identities []string
	beadRe     *regexp.Regexp
}

func analyze(in Inputs) *analysis {
	a := &analysis{views: map[Key]*view{}}
	cfg := in.Config
	if cfg == nil {
		return a
	}
	a.identities = cfg.FocusOperatorIdentities()
	if re := cfg.BeadIDRegexp(); re != nil {
		a.beadRe = re
	}
	identity := map[string]bool{}
	for _, id := range a.identities {
		if id != "" {
			identity[id] = true
		}
	}

	for k, e := range in.Entities {
		if k.Type != entityTypePR && k.Type != entityTypeIssue {
			continue
		}
		v := &view{key: k}
		decodeFacts(e.Facts, v)
		switch {
		case k.Type == entityTypePR:
			v.kind = KindPR
		case a.beadRe != nil && a.beadRe.MatchString(k.ID):
			v.kind = KindBead
		default:
			v.kind = KindJira
		}
		for _, l := range v.labels {
			if l == PlanableLabel {
				v.labelled = true
			}
		}

		// Exclusions, in the documented order.
		if e.Inactive {
			v.reasons = append(v.reasons, ReasonInactive)
		}
		if hidden(in.Annotations[k]) {
			v.reasons = append(v.reasons, ReasonHidden)
		}
		if k.Type == entityTypeIssue {
			if _, ok := v.metadata[MetaSourceID]; ok {
				v.reasons = append(v.reasons, ReasonMintedBead)
			}
			if _, ok := v.metadata[MetaDedupKey]; ok {
				v.reasons = append(v.reasons, ReasonWorkItemBead)
			}
		}
		if v.kind == KindBead && strings.EqualFold(strings.TrimSpace(v.state), statusDeferred) {
			v.reasons = append(v.reasons, ReasonDeferred)
		}
		if v.merged || classify.SourceTerminal(k.Type, json.RawMessage(e.Facts), in.Now, 0) {
			v.reasons = append(v.reasons, ReasonTerminal)
		}
		if _, ok := in.Absorbed[k]; ok {
			v.reasons = append(v.reasons, ReasonAbsorbed)
		}
		v.eligible = len(v.reasons) == 0

		// Seed rules.
		if v.eligible {
			switch v.kind {
			case KindPR:
				ip := in.Interps[k]
				owned := ip.Ownership == ownershipMine || ip.Ownership == ownershipCoOwn
				v.seed = strings.EqualFold(strings.TrimSpace(v.state), prStateOpen) &&
					(owned || requested(v.reviewRequests, cfg.SelfLogin))
			case KindJira:
				v.seed = identity[strings.TrimSpace(v.assignee)]
			case KindBead:
				v.seed = v.labelled && beadStatusOpenish(v.state)
			}
		}
		a.views[k] = v
		a.sorted = append(a.sorted, v)
	}
	sort.Slice(a.sorted, func(i, j int) bool { return candLess(a.sorted[i], a.sorted[j]) })

	// One hop over the yielding links, in either direction.
	seedsOf := map[Key]map[Key]string{} // candidate -> seed -> relation (smallest by name)
	join := func(cand, seed *view, relation string) {
		if cand == seed || !cand.eligible || cand.seed || !seed.seed {
			return
		}
		m := seedsOf[cand.key]
		if m == nil {
			m = map[Key]string{}
			seedsOf[cand.key] = m
		}
		if cur, ok := m[seed.key]; !ok || relation < cur {
			m[seed.key] = relation
		}
	}
	for _, l := range in.Links {
		if !yields(l) {
			continue
		}
		from, to := a.views[Key{l.FromType, l.FromID}], a.views[Key{l.ToType, l.ToID}]
		if from == nil || to == nil {
			continue
		}
		join(to, from, l.Relation)
		join(from, to, l.Relation)
	}
	for _, v := range a.sorted {
		if v.seed {
			v.candidate = true
			continue
		}
		seeds := seedsOf[v.key]
		if len(seeds) == 0 {
			continue
		}
		order := make([]*view, 0, len(seeds))
		for k := range seeds {
			order = append(order, a.views[k])
		}
		sort.Slice(order, func(i, j int) bool { return candLess(order[i], order[j]) })
		v.candidate = true
		v.via = &Via{SeedKey: order[0].key, Relation: seeds[order[0].key], More: len(order) - 1}
	}
	return a
}

// yields reports whether a link is one of the relations that join a
// candidate to a seed: exactly jira, work, parent and mentions between an
// issue or pr entity and another, a depends_on that leaves a pr entity, and
// an external references (a legacy references row carried over by the cutover
// has origin derived:legacy and yields nothing). The ci and self relations
// are not in the list.
func yields(l store.XrefLink) bool {
	if !entityKind(l.FromType) || !entityKind(l.ToType) {
		return false
	}
	switch l.Relation {
	case relationJira, relationWork, relationParent, relationMentions:
		return true
	case relationDependsOn:
		return l.FromType == entityTypePR
	case relationReferences:
		return strings.HasPrefix(l.Origin, externalOriginPrefix)
	}
	return false
}

func entityKind(t string) bool { return t == entityTypePR || t == entityTypeIssue }

func requested(requests []string, self string) bool {
	if self == "" {
		return false
	}
	for _, r := range requests {
		if r == self {
			return true
		}
	}
	return false
}

// hidden reads the reserved hidden annotation; an absent or undecodable one
// is not hidden (Load rejects an undecodable one, so a decoded Inputs never
// carries it).
func hidden(anns map[string]string) bool {
	raw, ok := anns[store.AnnotationHidden]
	if !ok {
		return false
	}
	h, err := decodeHidden(raw)
	return err == nil && h
}

// decodeFacts reads the stored facts of an entity into v: a PR's pr_show
// (state, merged, review_requests) or an issue's issue_show (state, labels,
// metadata, assignee). Undecodable facts leave v at its zero value, which no
// seed rule accepts.
func decodeFacts(facts string, v *view) {
	if facts == "" {
		return
	}
	switch v.key.Type {
	case entityTypePR:
		var f struct {
			PRShow struct {
				State          string   `json:"state"`
				Merged         bool     `json:"merged"`
				ReviewRequests []string `json:"review_requests"`
			} `json:"pr_show"`
		}
		if json.Unmarshal([]byte(facts), &f) != nil {
			return
		}
		v.state, v.merged, v.reviewRequests = f.PRShow.State, f.PRShow.Merged, f.PRShow.ReviewRequests
	case entityTypeIssue:
		var f struct {
			IssueShow struct {
				State     string                     `json:"state"`
				Labels    []string                   `json:"labels"`
				Metadata  map[string]json.RawMessage `json:"metadata"`
				Assignee  string                     `json:"assignee"`
				Parent    string                     `json:"parent"`
				IssueType string                     `json:"issue_type"`
				Deps      []issueDep                 `json:"deps"`
				DueDate   string                     `json:"due_date"`
				Priority  string                     `json:"priority"`
				CreatedAt string                     `json:"created_at"`
			} `json:"issue_show"`
		}
		if json.Unmarshal([]byte(facts), &f) != nil {
			return
		}
		v.state, v.labels, v.metadata, v.assignee = f.IssueShow.State, f.IssueShow.Labels, f.IssueShow.Metadata, f.IssueShow.Assignee
		v.parent, v.issueType, v.deps = f.IssueShow.Parent, f.IssueShow.IssueType, f.IssueShow.Deps
		v.dueDate, v.priority = f.IssueShow.DueDate, f.IssueShow.Priority
		v.createdAt = f.IssueShow.CreatedAt
	}
}

// issueDep is one dependency edge of a stored issue_show.deps.
type issueDep struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}
