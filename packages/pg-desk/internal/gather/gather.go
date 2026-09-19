// Package gather implements pg-desk's gather stage (stage 1 of the
// pipeline; docket pg2-2j5ac.32, Phase 9): it fetches facts about one
// triggering entity through pg-connector — never directly through a
// GitHub/Jira/beads API — and returns them as a JSON-serializable Facts
// value for the interpret stage (packet 5) to consume.
//
// pg-connector is execed as a subprocess, exactly once per verb call,
// under its ambient $PATH name — never a Go import of packages/pg-connector
// itself — matching the composition rule (D10, cmd/pg-desk/composition_test.go)
// this package's caller (packages/pg-desk's module) is held to: this
// package's own production code execs no literal binary name other than
// "pg-connector".
//
// # Phase 9 inputs
//
// Of the design's full input set, this phase gathers exactly six pg-connector
// reads for one triggering PR: `pr show`, `pr files`, `pr commits`, `ci list`,
// `issue list --query work-beads` (filtered down to the entries that match
// this PR, by metadata or title key), and `issue deps --full` (for
// waiting-on-me) [docs/behavior/pg-desk/gather.md "Phase 9 inputs"].
// entityType is always "pr" in this phase — an issue/thread-triggered pr-pool
// event re-runs only the interpret stage (design section 7.2), so gather
// itself never receives one; Gather rejects any other entityType outright
// rather than silently no-op'ing.
//
// # Per-run budget (Binding decisions > "Per-run budget")
//
// This package owns exactly one cache-key scheme, scoped to one Gatherer
// instance's lifetime (i.e. "per run" means "per process invocation that
// constructs one Gatherer and calls Gather one or more times on it" — the
// pipeline packet, 6, is expected to construct exactly one Gatherer per
// `pg-desk run` invocation): `pr files`/`pr commits` are cached by the
// triggering PR's own `head_sha` (from that call's own `pr show`), so a
// second Gather call in the same run for a PR whose head has not moved
// since an earlier call in this run reuses the cached files/commits rather
// than re-invoking pg-connector; and a `sweep` change additionally skips
// the rest of stage 1 entirely (files/commits/ci/work-beads/deps) when the
// entity's head_sha is unchanged from the head_sha this SAME Gatherer last
// observed for it. Neither cache persists across separate `pg-desk run`
// process invocations — that cross-run form of the same budget is the
// entity-table store's job (packet 3) and the pipeline's own job (packet
// 6) of deciding whether to call Gather at all for an unchanged sweep
// candidate; this package has no dependency on the store (not in this
// packet's Contract) and cannot see across process boundaries.
//
// # Degradation and the removed re-read
//
// A failure fetching anything other than the triggering PR (`pr show`)
// degrades the run: Facts.Degraded is set to the name of the first failing
// input and gather otherwise proceeds with whatever it already has. Only a
// failure to fetch the triggering PR itself is a hard error (feeds the
// pipeline's exit-code-1 case). On `--change removed`, `pr show` IS the
// re-read: its outcome decides RemovedState (open/merged/closed/not_found),
// and a not_found answer there is an expected outcome, not a hard error
// [docs/behavior/pg-desk/gather.md "--change removed handling"].
//
// # Phase 13's seventh input: Jira cross-references
//
// Phase 13 (docket pg2-2j5ac.40) adds a seventh input to the same PR-gather
// call above, never a new entityType: every Jira ticket key found in the
// triggering PR's branch/title/body (internal/ticketkey.Parse, config-driven
// via config.Config.TicketPatterns) is looked up with `issue show
// <ticket-key>` and recorded two ways — into Facts.JiraIssues (so
// interpret's scoreUrgencyWithHealth can read the PR's own cross-referenced
// Jira issue(s) without a second gather, including on a later
// interpret-only re-run over these SAME stored facts) and as an xref row
// (Store.UpsertXref, via the xrefUpserter this Gatherer now holds) so `run
// issue <jira-ticket-key>` can resolve back from the Jira side. This step
// runs only in the same place the other six do — never on a `removed`
// re-read, and never on a sweep this Gatherer's own head_sha cache has
// already decided to skip — since it depends on this same triggering PR's
// text, gathered together with everything else.
//
// # Phase 13's eighth input: linked threads
//
// Phase 13's Slack-half sibling packet ("pg-desk run thread") adds an
// eighth input, in the same place as the seventh (the `pr`-triggered path
// only, never on a `removed` re-read or a sweep-unchanged skip): a passive
// STORE READ of every thread already cross-referenced to the triggering PR
// (Store.ListXrefsByFrom(repo, "pr", <pr-id>, "thread")) — the reverse of
// `run thread`'s own from_type="pr"/to_type="thread" xref writes [Binding
// decisions > "xref row DIRECTION"]. This is a store read ONLY: gather
// never calls pg-connector-thread-slack itself — `run thread`'s own active
// cross-referencing (permalink/ticket-key scan, xref write), triggered by
// the thread-me feed, is what populates these links in the first place
// [Binding decisions]. Populated into Facts.LinkedThreads, at minimum each
// linked thread's id (from the xref row's own to_id); a fresh fetch of the
// full Thread entity for its permalink is deliberately NOT done here (this
// input stays a store read, never a live call) — see Facts.LinkedThreads'
// own doc comment for that gap. A read failure degrades this run exactly
// like every other non-triggering-entity input (named "linked threads");
// zero linked threads is a normal, expected outcome (most PRs have none),
// not a degradation.
package gather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
)

// ChangeKind is the pr-pool event's own change kind, reproduced verbatim
// from the design's section 7.2 `--change` flag values — exactly these four
// values, never a fifth.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeChanged ChangeKind = "changed"
	ChangeRemoved ChangeKind = "removed"
	ChangeSweep   ChangeKind = "sweep"
)

func (c ChangeKind) valid() bool {
	switch c {
	case ChangeAdded, ChangeChanged, ChangeRemoved, ChangeSweep:
		return true
	default:
		return false
	}
}

// Facts is everything gather produces for one entity: a JSON-serializable
// struct that lands byte-for-byte in the entity table's JSON column
// (packet 3). Every raw field is pg-connector's own "result" payload for
// that call, unwrapped from the wire envelope but otherwise untouched —
// packet 5 (interpret) decodes each into whatever shape it needs. The
// field set (names and meaning) is pinned by docket pg2-2j5ac.32's packet 4
// contract; do not add or remove a field without checking packets 5/6,
// which read this exact shape.
type Facts struct {
	// PRShow/PRFiles/PRCommits are the triggering PR's own `pr show`/`pr
	// files`/`pr commits` results (schema.PR/PRFilesResult/PRCommitsResult
	// shapes, decoded by packet 5 — this package never imports pkg/schema).
	PRShow    json.RawMessage `json:"pr_show,omitempty"`
	PRFiles   json.RawMessage `json:"pr_files,omitempty"`
	PRCommits json.RawMessage `json:"pr_commits,omitempty"`
	// CI is `ci list <pr-id>`'s fan-out result ({"runs":[...],"sources":[...]}),
	// stored as-is: it is not wrapped in a result/error envelope at the
	// wire level (fan-out ops print their outcome struct directly).
	CI json.RawMessage `json:"ci,omitempty"`
	// WorkBeads is `issue list --query work-beads`'s full fan-out result
	// ({"entities":[...],"present_ids":[...],"sources":[...]}) — the FULL
	// answer, not pre-filtered to this PR's own matches, so packet 5/6 can
	// see every source's health too; matchWorkBeads (this file) is only
	// used internally, to pick which matched entity's id feeds Deps below.
	WorkBeads json.RawMessage `json:"work_beads,omitempty"`
	// Deps is `issue deps --full <id>`'s result ({"ids":[...],"entities":[...]}),
	// for the "waiting-on-me" input — the id used is the FIRST work bead
	// this gather run matched to the triggering PR (see matchWorkBeads);
	// picking a single anchor id, rather than fetching deps for every
	// matched bead, mirrors pg-pr's own historical precedent
	// (pkg/beads.Client.DepTreeUp is called with exactly one root id, the
	// PR's merge-request bead) — distinguishing which matched KIND of work
	// bead (merge-request/review-pr/process-feedback) is Sync's own
	// concern (Phase 10, docket design section 7.5), not gather's, so this
	// phase picks the first match deterministically rather than
	// implementing that classification here. Empty when no work bead
	// matched this PR at all (no call is made — there is nothing to fetch
	// deps of).
	Deps json.RawMessage `json:"deps,omitempty"`

	// JiraIssues is Phase 13's seventh input (see the package doc comment):
	// `issue show <ticket-key>`'s raw result, keyed by the ticket key it
	// answered, for every Jira ticket key found in the triggering PR's
	// branch/title/body. interpret's scoreUrgencyWithHealth (urgency.go)
	// reads this — never a fresh gather of its own — including when this
	// SAME stored Facts value is later re-interpreted by `run issue
	// <jira-ticket-key>` (pipeline.RunInterpretOnly never re-gathers).
	// Empty when config.Config.TicketPatterns matches nothing in this PR's
	// text (the common Phase-9-unaware or non-Jira-linked case) — a safe
	// default, not a degradation.
	JiraIssues map[string]json.RawMessage `json:"jira_issues,omitempty"`

	// LinkedThreads is Phase 13's eighth input (see the package doc
	// comment): every thread already cross-referenced to the triggering PR
	// in the store, at the time of this gather run. Populated by a passive
	// store read (Store.ListXrefsByFrom) — never a pg-connector-thread-slack
	// call of gather's own. The design pins WHAT is gathered ("thread
	// entities already linked in the store") but not what interpret does
	// with it (unlike JiraIssues' own scoreUrgencyWithHealth consumer) — no
	// consumer exists yet; this field only makes the data available.
	//
	// KNOWN GAP: ThreadRef.Permalink is defined (the design names it
	// alongside the thread's id as the two fields "any xref row already
	// carries... once fetched") but this Gatherer never populates it: the
	// xref table's own rows (internal/store/xref.go) carry no permalink
	// column, and no packet through Phase 13 persists a thread's own facts
	// (schema.Thread) anywhere pg-desk's store could read one back from
	// without a fresh pg-connector-thread-slack call — which this input is
	// deliberately forbidden from making [Binding decisions: "gather
	// performs a STORE READ only for this half"]. Left empty rather than
	// guessed; a future phase that persists thread facts can backfill it
	// without changing this field's shape.
	LinkedThreads []ThreadRef `json:"linked_threads,omitempty"`

	// AsOf is the timestamp pg-connector's `pr show` reported for this read.
	AsOf string `json:"as_of,omitempty"`
	// HeadSHA is the triggering PR's current head commit, from `pr show` —
	// this run's own files/commits cache key (see this package's doc
	// comment).
	HeadSHA string `json:"head_sha,omitempty"`
	// RemovedState is populated only on ChangeRemoved: one of
	// open/merged/closed/not_found, read verbatim from the `pr show`
	// re-read's own state/merged fields (never re-derived by any other
	// means) [Binding decisions > "removed"].
	RemovedState string `json:"removed_state,omitempty"`
	// Degraded names the first non-critical input that failed this run;
	// empty means healthy. A single string field, not a list — the first
	// failure wins and is named, matching the design's exact phrasing
	// ("marks the interpretation degraded with the failing input named").
	Degraded string `json:"degraded,omitempty"`
}

// pgConnectorBinary is the ambient $PATH name this package execs — never a
// compile-time import of packages/pg-connector (D10).
const pgConnectorBinary = "pg-connector"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-connector.
// Production code uses exec.CommandContext; gather_test.go swaps this to
// spawn a reentrant test-helper process (this packet's own "fake
// pg-connector binary or a stubbed exec" wire-double requirement) —
// mirrors packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector's
// exec.go execCmdFactory pattern exactly, one layer up (pg-desk calling
// pg-connector rather than an adapter calling it).
var execCmdFactory = exec.CommandContext

// ThreadRef is one thread already cross-referenced to the triggering PR in
// the store — Facts.LinkedThreads' own element type (Phase 13's eighth
// input; see the package doc comment and Facts.LinkedThreads' own doc
// comment for the Permalink gap).
type ThreadRef struct {
	// ID is the linked thread's own id (the xref row's to_id).
	ID string `json:"id"`
	// Permalink is left empty by this Gatherer (see Facts.LinkedThreads'
	// own "KNOWN GAP" doc comment) — present in the shape for a future
	// phase to backfill without a field-shape change.
	Permalink string `json:"permalink,omitempty"`
}

// xrefUpserter is the subset of *store.Store's API Phase 13's ticket-key
// scan depends on to record its cross-reference rows (see the package doc
// comment) and Phase 13's Slack-half sibling packet's own gather addition
// depends on to read them back (ListXrefsByFrom, below) — defined locally
// — mirroring internal/pipeline/pipeline.go's own gatherer/syncer
// local-interface pattern, one layer down — so tests can inject a fake
// without a real SQLite store. *store.Store satisfies this by
// construction. Named xrefUpserter for its historical (Jira-half-only)
// first consumer; widened here rather than split into two interfaces,
// since both this Gatherer's own xref reads and writes go through the
// SAME *store.Store value (NewGatherer's own xrefs parameter, below).
type xrefUpserter interface {
	UpsertXref(x store.Xref) error
	ListXrefsByFrom(repo, fromType, fromID, toType string) ([]store.Xref, error)
}

// Gatherer holds this run's per-run budget cache (see the package doc
// comment), the config Gather needs (the beads workspace directory for
// every issue exec, and Phase 13's TicketPatterns), and the xref writer
// Phase 13's seventh input records to. Not safe for concurrent Gather
// calls — the pipeline (packet 6) is expected to call Gather sequentially,
// one entity at a time, exactly as pg-connector's own multi-instance
// resolution and this package's file/commits/sweep cache both assume.
type Gatherer struct {
	cfg   *config.Config
	xrefs xrefUpserter

	// filesCommitsCache is keyed by head_sha (Binding decisions > "Per-run
	// budget": "cache/skip by head_sha for files and commits").
	filesCommitsCache map[string]filesCommits
	// lastHeadSHA is keyed by entity id (the triggering PR's own id) — the
	// head_sha this Gatherer last observed for it, any change kind. Used
	// by the sweep-unchanged skip below.
	lastHeadSHA map[string]string
}

type filesCommits struct {
	files   json.RawMessage
	commits json.RawMessage
}

// NewGatherer constructs a Gatherer with a fresh, empty per-run cache.
// xrefs is Phase 13's cross-reference reader/writer (internal/pipeline's
// New passes its own *store.Store, which satisfies xrefUpserter); a nil
// xrefs is safe: the ticket-key scan's own writes are reached only when a
// configured TicketPatterns recognizes a ticket key (every Phase-9/10
// caller that never sets TicketPatterns — including this package's own
// pre-Phase-13 tests — never reaches that code path), and
// gatherLinkedThreads (Phase 13's eighth input, below) checks for a nil
// xrefs explicitly before calling ListXrefsByFrom.
func NewGatherer(cfg *config.Config, xrefs xrefUpserter) *Gatherer {
	return &Gatherer{
		cfg:               cfg,
		xrefs:             xrefs,
		filesCommitsCache: make(map[string]filesCommits),
		lastHeadSHA:       make(map[string]string),
	}
}

// Gather fetches Facts for one triggering entity. entityType must be "pr"
// (see the package doc comment); any other value is rejected outright.
func (g *Gatherer) Gather(ctx context.Context, entityType, entityID string, change ChangeKind) (Facts, error) {
	if entityType != "pr" {
		return Facts{}, fmt.Errorf("gather: entity type %q not supported in Phase 9 (pr-triggered gather only)", entityType)
	}
	if entityID == "" {
		return Facts{}, errors.New("gather: entity id is required")
	}
	if !change.valid() {
		return Facts{}, fmt.Errorf("gather: change kind %q is not one of added/changed/removed/sweep", change)
	}

	prShowRaw, notFound, err := g.targetedCall(ctx, []string{"pr", "show", entityID}, nil)

	if change == ChangeRemoved {
		return g.removedFacts(prShowRaw, notFound, err)
	}

	if notFound {
		return Facts{}, fmt.Errorf("gather: fetch triggering PR %s: pg-connector reports not_found", entityID)
	}
	if err != nil {
		return Facts{}, fmt.Errorf("gather: fetch triggering PR %s: %w", entityID, err)
	}
	show, decErr := decodePRShow(prShowRaw)
	if decErr != nil {
		return Facts{}, fmt.Errorf("gather: decode triggering PR %s pr show result: %w", entityID, decErr)
	}

	f := Facts{PRShow: prShowRaw, AsOf: show.AsOf, HeadSHA: show.HeadSHA}

	if change == ChangeSweep {
		if prev, seen := g.lastHeadSHA[entityID]; seen && prev == show.HeadSHA {
			// Per-run budget: a sweep skips the rest of stage 1 for an
			// entity whose head is unchanged since this Gatherer last saw
			// it this run [Binding decisions > "Per-run budget"].
			return f, nil
		}
	}

	g.gatherFilesAndCommits(ctx, entityID, show.HeadSHA, &f)
	g.lastHeadSHA[entityID] = show.HeadSHA

	g.gatherCI(ctx, entityID, &f)
	matched := g.gatherWorkBeads(ctx, show.Repo, show.Number, &f)
	g.gatherDeps(ctx, matched, &f)
	g.gatherJiraXrefs(ctx, entityID, show, &f)
	g.gatherLinkedThreads(entityID, &f)

	return f, nil
}

// removedFacts implements the `--change removed` re-read rule: the pr
// show re-read's own outcome decides RemovedState, and pg-connector's own
// not_found answer is an EXPECTED outcome here (RemovedState "not_found"),
// never a hard error — the one place in this package where that call's
// not_found branch is not folded into the generic failure path
// [docs/behavior/pg-desk/gather.md "--change removed handling"].
func (g *Gatherer) removedFacts(prShowRaw json.RawMessage, notFound bool, err error) (Facts, error) {
	if notFound {
		return Facts{RemovedState: "not_found"}, nil
	}
	if err != nil {
		return Facts{}, fmt.Errorf("gather: removed re-read pr show: %w", err)
	}
	show, decErr := decodePRShow(prShowRaw)
	if decErr != nil {
		return Facts{}, fmt.Errorf("gather: decode removed re-read pr show result: %w", decErr)
	}
	return Facts{
		PRShow:       prShowRaw,
		AsOf:         show.AsOf,
		HeadSHA:      show.HeadSHA,
		RemovedState: removedStateFrom(show.State, show.Merged),
	}, nil
}

// removedStateFrom maps a re-read pr show result's own state/merged fields
// onto exactly one of open/merged/closed — read verbatim from pg-connector's
// state vocabulary (its "state" string is "open"/"closed" from the live
// provider, "merged" folded in here from its own "merged" boolean exactly
// as this codebase's existing pg-pr precedent already combines them,
// e.g. packages/pg-pr/internal/sync/sync.go's
// `merged := pr.Merged || pr.State == "merged"` — never a NEW heuristic of
// gather's own invention) [Binding decisions > "removed"].
func removedStateFrom(state string, merged bool) string {
	switch {
	case state == "open":
		return "open"
	case merged || state == "merged":
		return "merged"
	default:
		return "closed"
	}
}

// gatherFilesAndCommits fetches pr files/pr commits, honoring the
// head_sha-keyed cache, and degrades f on failure (naming whichever of the
// two failed first).
func (g *Gatherer) gatherFilesAndCommits(ctx context.Context, entityID, headSHA string, f *Facts) {
	if cached, hit := g.filesCommitsCache[headSHA]; hit {
		f.PRFiles = cached.files
		f.PRCommits = cached.commits
		return
	}

	filesRaw, _, filesErr := g.targetedCall(ctx, []string{"pr", "files", entityID}, nil)
	if filesErr != nil {
		f.degrade("pr files")
	} else {
		f.PRFiles = filesRaw
	}

	commitsRaw, _, commitsErr := g.targetedCall(ctx, []string{"pr", "commits", entityID}, nil)
	if commitsErr != nil {
		f.degrade("pr commits")
	} else {
		f.PRCommits = commitsRaw
	}

	if filesErr == nil && commitsErr == nil {
		g.filesCommitsCache[headSHA] = filesCommits{files: filesRaw, commits: commitsRaw}
	}
}

func (g *Gatherer) gatherCI(ctx context.Context, entityID string, f *Facts) {
	raw, err := g.fanOutCall(ctx, []string{"ci", "list", entityID}, nil)
	if err != nil {
		f.degrade("ci list")
		return
	}
	f.CI = raw
}

// gatherWorkBeads fetches `issue list --query work-beads` (every issue exec
// carries the beads workspace env var, per docs/behavior/pg-desk/gather.md)
// and returns the entities matched to this PR by metadata or title key
// (design section 7.5's own dedup-key vocabulary: metadata.repo +
// metadata.pr_number, or an exact/prefixed title carrying "<repo>#<n>").
func (g *Gatherer) gatherWorkBeads(ctx context.Context, repo string, number int, f *Facts) []workBeadEntity {
	raw, err := g.fanOutCall(ctx, []string{"issue", "list", "--query", "work-beads"}, g.issueBeadsDirEnv())
	if err != nil {
		f.degrade("issue list --query work-beads")
		return nil
	}
	f.WorkBeads = raw
	return matchWorkBeads(raw, repo, number)
}

// gatherDeps fetches `issue deps --full <id>` for the first entry in
// matched (see Facts.Deps' own doc comment for why "first" and why no
// call at all when matched is empty).
func (g *Gatherer) gatherDeps(ctx context.Context, matched []workBeadEntity, f *Facts) {
	if len(matched) == 0 {
		return
	}
	raw, _, err := g.targetedCall(ctx, []string{"issue", "deps", matched[0].ID, "--full"}, g.issueBeadsDirEnv())
	if err != nil {
		f.degrade("issue deps --full")
		return
	}
	f.Deps = raw
}

// jiraTicketField is one field of the triggering PR's own text this
// packet scans for a Jira ticket key, paired with the xref "evidence"
// value that field's own name IS (Produces: "evidence set to the SINGLE
// field name the key was found in").
type jiraTicketField struct {
	evidence string
	text     string
}

// gatherJiraXrefs is Phase 13's seventh input (see the package doc
// comment): for every Jira ticket key found in the triggering PR's
// branch/title/body, it fetches `issue show <ticket-key>` at most ONCE per
// distinct key (reused across every field the key was found in — an
// efficiency choice the design does not pin either way) and records that
// key two ways:
//
//   - Into f.JiraIssues, keyed by ticket key, on a successful (non-
//     not_found) issue show — consumed by interpret's
//     scoreUrgencyWithHealth. A failure OTHER than not_found degrades this
//     run exactly like every other non-triggering-entity input; not_found
//     is a well-formed negative answer (mirrors removedFacts' own
//     not_found handling), not a degradation, and simply means no Jira
//     fact was fetched for that key this run.
//   - As an UpsertXref call, once per (key, field) pair — so a key found
//     in more than one field is upserted once per field it appears in,
//     per the Produces rule this packet documents on the xref table's own
//     ON CONFLICT semantics (the LAST-upserted field wins the stored
//     evidence value; the design does not ask for multi-evidence
//     tracking). This happens regardless of whether the paired issue show
//     call above succeeded: the xref records that the PR's text
//     REFERENCES this key, independent of whether Jira currently answers
//     for it (e.g. the ticket not yet existing when this PR was gathered).
//
// Uses show.AsOf (this same gather call's own temporal anchor, already
// stamped by pg-connector's `pr show`) for both first_seen and
// last_confirmed — this package has no injectable clock of its own (unlike
// interpret.Clock), and AsOf is already the "as of" time this whole gather
// run represents.
func (g *Gatherer) gatherJiraXrefs(ctx context.Context, entityID string, show prShowFields, f *Facts) {
	patterns := g.cfg.TicketPatterns
	fields := []jiraTicketField{
		{evidence: "branch", text: show.Branch},
		{evidence: "title", text: show.Title},
		{evidence: "body", text: show.Body},
	}

	fetched := make(map[string]json.RawMessage)
	attempted := make(map[string]bool)
	repo := g.repo()

	for _, fld := range fields {
		keys := ticketkey.Parse(fld.text, "", "", patterns)
		for _, key := range keys {
			if !attempted[key] {
				attempted[key] = true
				raw, notFound, err := g.targetedCall(ctx, []string{"issue", "show", key}, g.issueBeadsDirEnv())
				switch {
				case err != nil:
					f.degrade("issue show")
				case notFound:
					// Well-formed negative answer, not a degradation — see
					// this method's own doc comment.
				default:
					fetched[key] = raw
				}
			}
			if raw, ok := fetched[key]; ok {
				if f.JiraIssues == nil {
					f.JiraIssues = make(map[string]json.RawMessage)
				}
				f.JiraIssues[key] = raw
			}
			if g.xrefs != nil {
				_ = g.xrefs.UpsertXref(store.Xref{
					Repo:          repo,
					FromType:      "pr",
					FromID:        entityID,
					ToType:        "issue",
					ToID:          key,
					Evidence:      fld.evidence,
					FirstSeen:     show.AsOf,
					LastConfirmed: show.AsOf,
				})
			}
		}
	}
}

// gatherLinkedThreads is Phase 13's eighth input (see the package doc
// comment): a passive STORE READ of every thread already cross-referenced
// to the triggering PR (Store.ListXrefsByFrom(repo, "pr", entityID,
// "thread")) — never a pg-connector-thread-slack call of gather's own. A
// nil xrefs (see NewGatherer's own doc comment) skips this silently, same
// as gatherJiraXrefs' own nil check. A read failure degrades this run
// exactly like every other non-triggering-entity input; zero linked
// threads is a normal, expected outcome (most PRs have none), not a
// degradation.
func (g *Gatherer) gatherLinkedThreads(entityID string, f *Facts) {
	if g.xrefs == nil {
		return
	}
	xrefs, err := g.xrefs.ListXrefsByFrom(g.repo(), "pr", entityID, "thread")
	if err != nil {
		f.degrade("linked threads")
		return
	}
	for _, x := range xrefs {
		f.LinkedThreads = append(f.LinkedThreads, ThreadRef{ID: x.ToID})
	}
}

// repo returns the single Phase-9 configured repository's remote, or ""
// if none is configured — mirrors internal/pipeline's own identical
// p.repo() helper (each package keeps its own private copy rather than a
// shared one, per this package's established precedent for e.g.
// pgConnectorBinary), so the xref rows this method writes are keyed by the
// SAME repo value the entity/interpretation tables and the pipeline's own
// ListXrefsByTo lookup use.
func (g *Gatherer) repo() string {
	if g.cfg == nil || len(g.cfg.Repos) == 0 {
		return ""
	}
	return g.cfg.Repos[0].Remote
}

// degrade records name as the failing input IFF nothing has degraded this
// Facts yet — Degraded is a single string field, first failure wins
// (matches the design's singular "the failing input named").
func (f *Facts) degrade(name string) {
	if f.Degraded == "" {
		f.Degraded = name
	}
}

// issueBeadsDirEnv builds the extraEnv slice every issue exec carries:
// PG_CONNECTOR_ISSUE_BEADS_DIR=<the configured repo's beads_dir>, and
// nothing at all when it is unset — so the child falls back to whatever
// BEADS_DIR it inherited ambiently, mirroring
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/exec.go's
// beadsDirEnv convention exactly [Binding decisions;
// docs/behavior/pg-desk/gather.md "Phase 9 inputs"].
func (g *Gatherer) issueBeadsDirEnv() []string {
	if len(g.cfg.Repos) == 0 {
		return nil
	}
	dir := g.cfg.Repos[0].BeadsDir
	if dir == "" {
		return nil
	}
	return []string{"PG_CONNECTOR_ISSUE_BEADS_DIR=" + dir}
}

// prShowFields is the minimal subset of `pr show`'s result payload this
// package decodes for itself (AsOf/HeadSHA for Facts, Repo/Number/State/
// Merged for the removed re-read and the work-beads match key,
// Title/Branch/Body for Phase 13's ticket-key scan below) — this package
// imports no pkg/schema type (D10 is about exec, not import, but staying
// off pkg/schema too keeps this package decoupled from pg-connector's
// internal Go API, talking to it only over the CLI/wire surface, matching
// "Gather ONLY through pg-connector" read literally). Title/Branch/Body
// were added by Phase 13 (docket pg2-2j5ac.40): schema.PR already carries
// all three, but this struct did not decode them until this packet's own
// ticket-key scan needed them as input.
type prShowFields struct {
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Branch  string `json:"branch"`
	Body    string `json:"body"`
	Merged  bool   `json:"merged"`
	HeadSHA string `json:"head_sha"`
	AsOf    string `json:"as_of"`
}

func decodePRShow(raw json.RawMessage) (prShowFields, error) {
	var f prShowFields
	if len(raw) == 0 {
		return f, errors.New("empty result")
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, err
	}
	return f, nil
}

// workBeadEntity is the minimal subset of an `issue list` entity this
// package decodes to match it to a PR.
type workBeadEntity struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Metadata map[string]string `json:"metadata"`
}

type workBeadsFanOut struct {
	Entities []workBeadEntity `json:"entities"`
}

// matchWorkBeads filters raw (an `issue list` fan-out result) down to the
// entities that match (repo, number) by metadata or title key — design
// section 7.5's own dedup-key vocabulary: metadata "repo"+"pr_number", or a
// title carrying "<repo>#<n>" either as the exact `process-feedback:
// <repo>#<n>` cycle title or as a `<repo>#<n>:` prefix (the merge-request/
// review-pr bead shape). Returns nil (rather than erroring) on malformed
// JSON or zero matches — an empty match set is a normal, expected outcome
// (not every PR has a work bead yet).
func matchWorkBeads(raw json.RawMessage, repo string, number int) []workBeadEntity {
	var out workBeadsFanOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	prKey := repo + "#" + strconv.Itoa(number)
	titlePrefix := prKey + ":"
	processFeedbackTitle := "process-feedback: " + prKey

	var matched []workBeadEntity
	for _, e := range out.Entities {
		switch {
		case e.Metadata["repo"] == repo && e.Metadata["pr_number"] == strconv.Itoa(number):
			matched = append(matched, e)
		case strings.HasPrefix(e.Title, titlePrefix):
			matched = append(matched, e)
		case e.Title == processFeedbackTitle:
			matched = append(matched, e)
		}
	}
	return matched
}

// runResult is one pg-connector subprocess invocation's raw outcome:
// captured stdout plus its exit code.
type runResult struct {
	stdout   []byte
	exitCode int
}

// run execs pg-connector with args, inheriting this process's own
// environment plus any extraEnv entries appended. The returned error is
// non-nil only when the child could never even be started (binary missing,
// permission denied, context already canceled, ...) — there is then no
// exit code to classify.
func (g *Gatherer) run(ctx context.Context, args []string, extraEnv []string) (runResult, error) {
	cmd := execCmdFactory(ctx, pgConnectorBinary, args...)
	if len(extraEnv) > 0 {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return runResult{stdout: stdout.Bytes(), exitCode: 0}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return runResult{stdout: stdout.Bytes(), exitCode: exitErr.ExitCode()}, nil
	}
	return runResult{}, fmt.Errorf("exec pg-connector %v: %w", args, runErr)
}

// wireEnvelope is the subset of pkg/scriptout's Response envelope this
// package decodes for itself (see prShowFields' doc comment for why this
// package hand-decodes rather than importing pkg/scriptout).
type wireEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// targetedCall runs a targeted pg-connector verb (pr show/files/commits,
// issue deps) and classifies its outcome per pg-connector's own 0/4/1
// targeted exit-code scheme (cmd/pg-connector's outcome.go TargetedExitCode):
// ok is signaled by a nil error with result populated (exit 0); notFound is
// exit 4 (a well-formed negative answer — result is empty); any other
// outcome (exit 1, or a failure to even start pg-connector) returns err.
func (g *Gatherer) targetedCall(ctx context.Context, args []string, extraEnv []string) (result json.RawMessage, notFound bool, err error) {
	res, runErr := g.run(ctx, args, extraEnv)
	if runErr != nil {
		return nil, false, runErr
	}
	switch res.exitCode {
	case 0:
		var env wireEnvelope
		if decErr := json.Unmarshal(res.stdout, &env); decErr != nil {
			return nil, false, fmt.Errorf("decode pg-connector %v stdout: %w", args, decErr)
		}
		return env.Result, false, nil
	case 4:
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("pg-connector %v: exit %d: %s", args, res.exitCode, wireErrorMessage(res.stdout))
	}
}

// fanOutCall runs a fan-out pg-connector verb (ci list, issue list) and
// classifies per pg-connector's own 0/2/3 fan-out scheme
// (cmd/pg-connector's outcome.go FanOutOutcome.ExitCode): exit 0 or 2 both
// return the raw stdout as-is (it already carries its own per-source
// degradation detail in "sources[]" — gather does not re-inspect that
// detail, it only cares whether the CALL overall produced usable data);
// exit 3 (total failure), or any other outcome (e.g. exit 1 from
// issue list's own query_not_recognized CLI-level failure, or a failure to
// even start pg-connector), reports err.
func (g *Gatherer) fanOutCall(ctx context.Context, args []string, extraEnv []string) (json.RawMessage, error) {
	res, runErr := g.run(ctx, args, extraEnv)
	if runErr != nil {
		return nil, runErr
	}
	switch res.exitCode {
	case 0, 2:
		return json.RawMessage(res.stdout), nil
	default:
		return nil, fmt.Errorf("pg-connector %v: exit %d: %s", args, res.exitCode, wireErrorMessage(res.stdout))
	}
}

// wireErrorMessage best-effort extracts a human-readable message from a
// failed call's stdout (a wireEnvelope error branch), falling back to the
// raw stdout text when it does not decode as one.
func wireErrorMessage(stdout []byte) string {
	var env wireEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil && env.Error != nil {
		return env.Error.Code + ": " + env.Error.Message
	}
	return strings.TrimSpace(string(stdout))
}
