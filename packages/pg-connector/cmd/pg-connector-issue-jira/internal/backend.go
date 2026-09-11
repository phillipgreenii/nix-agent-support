// backend.go: Backend implements pkg/provider/issue.Provider against a real
// external Jira integration, the issue capability's second Tier-2 backend.
// Show's exec-and-decode shape carries over
// packages/pg-pr/pkg/provider/issues/jira/jira.go's real, landed precedent
// [carry-over basis: packages/pg-pr/pkg/provider/issues/jira/jira.go],
// adapted to schema.Issue's wider field set and to the ACTUAL binary/JSON
// shape verified live on this machine (2026-09-08):
//
//   - `pjira issue <KEY>` (NOT `jira issue <KEY>` — see runner.go's
//     defaultBinary doc comment) prints an indented JSON object with fields
//     key/summary/status/issuetype/labels/url/priority/project/assignee
//     [verified against phillipg-nix-repo-base's
//     modules/jira/pkg/pjira/model.go and modules/jira/cmd/pjira/main.go's
//     newIssueCmd, not assumed from jira.go's older, narrower decode].
//   - On a not-found key, pjira's own client.go returns
//     "pjira: issue <KEY> not found" and its main.go prints that to stderr,
//     exiting 1 (a plain, unwrapped Go error — main.go's exitCodeError path
//     is only used for auth-status's specific states, not `issue`) — see
//     classifyPJIRAErrorMessage.
//
// Create/Comment/Transition were escalated (pg2-2j5ac.17.1's bd comment)
// when pjira had no write op at all — that gap is now closed: pg2-7p4mr's
// children landed `pjira create`/`pjira transition`/`pjira comment` (see
// each method's own doc comment below for the verified CLI/JSON shape),
// and this file implements all three for real rather than the documented
// scriptout.ErrUnavailable stub the escalation originally left in place.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-issue-jira's concrete issue.Provider
// implementation.
type Backend struct {
	runner Runner
	// getenv resolves EnvProject for Create. Optional — nil means
	// os.Getenv (the production default via New); tests inject a fixed
	// lookup so resolution never depends on this process's real
	// environment, mirroring CLIRunner's own Getenv field.
	getenv func(string) string
}

// New returns a Backend wrapping the given Runner. Production wiring passes
// NewCLIRunner(); tests inject a fake Runner.
func New(r Runner) *Backend {
	return &Backend{runner: r}
}

// getenvFunc returns b.getenv, defaulting to os.Getenv when unset.
func (b *Backend) getenvFunc() func(string) string {
	if b.getenv != nil {
		return b.getenv
	}
	return os.Getenv
}

// Compile-time check that Backend satisfies the issue capability's
// Provider interface.
var _ issue.Provider = (*Backend)(nil)

// Compile-time check that Backend also satisfies the search capability's
// Provider interface (bead pg2-8hcnx — this backend's own `pjira search
// --jql` call already existed for List; it was simply never wired to the
// cross-capability "search" op before this).
var _ search.Provider = (*Backend)(nil)

// Compile-time check that Backend also implements the optional AuthChecker
// capability (see CheckAuth's doc comment for why this backend, unlike
// pg-connector-issue-beads, can implement it).
var _ provider.AuthChecker = (*Backend)(nil)

// Compile-time check that Backend also satisfies the attention
// capability's own Provider interface (bead pg2-7wqkr — see attention.go's
// own ListAttention).
var _ attention.Provider = (*Backend)(nil)

// Vocabulary is this backend's declared, non-empty state vocabulary —
// Jira's own classic default workflow status names ("To Do"/"In
// Progress"/"Done"), the out-of-box status set Atlassian ships on every
// unmodified Jira Software project template. This is deliberately NOT any
// customized (or ZR-specific) workflow's own status names — this repo's
// public-repo hygiene forbids committing those, and pjira itself exposes no
// op to enumerate a live project's actual workflow — but these three are
// real, documented Jira defaults, not invented: the same "declare this
// backend's own real, non-empty vocabulary" precedent
// cmd/pg-connector-issue-beads/internal/backend.go's Vocabulary sets for
// bd's built-in statuses, applied to Jira's own default rather than a
// per-tenant customization this backend cannot discover. A project using a
// customized workflow may accept different values — inherent to Jira not
// sharing one fixed state vocabulary across trackers (the reason every
// backend declares its OWN vocabulary via its capabilities response
// rather than a shared cross-backend enum — see
// pkg/provider/issue/iface.go's Transition doc comment).
var Vocabulary = []string{"To Do", "In Progress", "Done"}

// resolvingState is the Jira workflow state Close transitions to (bead
// pg2-2j5ac.28.3's own Contract: "Jira maps [close] to a resolving
// transition") — Vocabulary's own "Done" above, the same classic-default
// state this backend already declares. A project on a customized workflow
// with a differently-named terminal state hits the same limitation
// Vocabulary's own doc comment already names for Transition generally.
const resolvingState = "Done"

// PriorityVocabulary is this backend's declared, non-empty priority
// vocabulary — Jira's own classic default priority scheme (Highest/High/
// Medium/Low/Lowest), the same "real tracker-native values, not an
// invented High/Medium/Low scale" precedent
// cmd/pg-connector-issue-beads/internal/backend.go's PriorityVocabulary
// sets for bd (review finding A-33). As with Vocabulary above, a
// customized priority scheme may differ; pjira exposes no op to enumerate
// one, so this backend declares Jira's own documented default.
var PriorityVocabulary = []string{"Highest", "High", "Medium", "Low", "Lowest"}

// pjiraIssue is the subset of pjira's own Issue JSON shape (`pjira issue
// <KEY>`) this backend needs — verified against
// phillipg-nix-repo-base/modules/jira/pkg/pjira/model.go (the real tool's
// own wire shape), not assumed from
// packages/pg-pr/pkg/provider/issues/jira/jira.go's older, narrower decode
// (which predates pjira's rename from "jira" and does not carry
// project/assignee).
type pjiraIssue struct {
	Key       string     `json:"key"`
	Summary   string     `json:"summary"`
	Status    string     `json:"status"`
	IssueType string     `json:"issuetype"`
	Labels    []string   `json:"labels"`
	URL       string     `json:"url"`
	Priority  string     `json:"priority,omitempty"`
	Project   string     `json:"project,omitempty"`
	Assignee  *pjiraUser `json:"assignee,omitempty"`
	// Duedate is Jira's own standard duedate field (bead pg2-7wqkr's
	// design: "deadline source is Jira's standard duedate field"),
	// verified against phillipg-nix-repo-base's
	// modules/jira/pkg/pjira/model.go's own Issue.Duedate
	// (`*string json:"duedate,omitempty"`) and client.go's field-request
	// list (both GetIssue and Search explicitly request "duedate" from
	// Jira's API) — pjira DOES carry this field; toSchemaIssue's own
	// former doc comment claiming DueDate "stays empty ... pjiraIssue
	// carries none of those today" predates pjira gaining it and is
	// corrected below. A pointer (like pjiraUser's own fields) since an
	// issue with no due date omits the key entirely.
	Duedate *string `json:"duedate,omitempty"`
}

// pjiraUser is pjira's own nested user shape (model.go's User), used here
// only for Assignee.
type pjiraUser struct {
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// pjiraSearchResult is `pjira search --jql <JQL>`'s own stdout shape
// (verified live, 2026-09-10: `pjira search --help` — "JQL search; writes
// {items,truncated,next_page_token?} JSON"). Items is assumed to share
// pjiraIssue's own field shape (key/summary/status/issuetype/labels/url/
// priority/project/assignee): both come from the same underlying pjira
// tool's own Jira-issue representation, and pjira exposes no separate,
// narrower "search result item" shape of its own [freedom boundary — not
// independently verified against a live search response, since doing so
// would require live Jira credentials/network this packet's own
// implementer does not have; a future discrepancy here is a decode
// failure (ErrUnavailable), not a silent wrong-field read].
type pjiraSearchResult struct {
	Items         []pjiraIssue `json:"items"`
	Truncated     bool         `json:"truncated"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

// decodePJIRASearchResult decodes `pjira search`'s stdout.
func decodePJIRASearchResult(raw string) (*pjiraSearchResult, error) {
	var res pjiraSearchResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// decodePJIRAIssue decodes `pjira issue <KEY>`'s stdout into a pjiraIssue.
// A response with an empty key is treated as a decode failure (never a
// silently-successful zero-value issue) — mirroring
// cmd/pg-connector-issue-beads/internal/bd.go's bdIssueFromArray/
// bdIssueFromObject's identical guard.
func decodePJIRAIssue(raw string) (*pjiraIssue, error) {
	var iss pjiraIssue
	if err := json.Unmarshal([]byte(raw), &iss); err != nil {
		return nil, err
	}
	if iss.Key == "" {
		return nil, errors.New("pjira: decoded issue has empty key")
	}
	return &iss, nil
}

// toSchemaIssue maps a decoded pjiraIssue onto the issue capability's
// shared wire shape. Tracker carries the issue's own Jira project key, per
// pkg/schema/issue.go's own Tracker field doc comment ("e.g. a bd
// workspace directory, a Jira base URL, a GitHub host") — this backend has
// no single ambient base URL/tenant value of its own to
// report generically (pjira resolves that internally, per-call, from its
// own config), so the per-issue project key is the concrete, real value
// available here. Assignee prefers DisplayName, falling back to Email when
// a Jira instance's API returns one but not the other.
//
// asOf is this call's own completion time (bead pg2-2j5ac.28.3, mirroring
// cmd/pg-connector-issue-beads/internal/backend.go's identical toSchemaIssue
// pattern): every call site below execs pjira fresh with no local cache
// of Jira's own facts, so Stale is always false. DueDate is now carried
// straight through from pjiraIssue.Duedate (bead pg2-7wqkr added that
// field — see its own doc comment for why this corrects an earlier,
// now-stale claim that pjira carried no due date at all).
// UpdatedAt/Metadata/ExternalRefs still stay empty: pjiraIssue carries
// none of those today (verified against pjira's own `issue`/`search` JSON
// shapes) — a future pjira addition of any of them is this backend's own
// follow-up, not fabricated here.
func toSchemaIssue(iss *pjiraIssue, asOf time.Time) *schema.Issue {
	var assignee string
	if iss.Assignee != nil {
		assignee = iss.Assignee.DisplayName
		if assignee == "" {
			assignee = iss.Assignee.Email
		}
	}
	var dueDate string
	if iss.Duedate != nil {
		dueDate = *iss.Duedate
	}
	return &schema.Issue{
		ID:        iss.Key,
		Title:     iss.Summary,
		State:     iss.Status,
		URL:       iss.URL,
		Priority:  iss.Priority,
		Labels:    iss.Labels,
		IssueType: iss.IssueType,
		Tracker:   iss.Project,
		Assignee:  assignee,
		AsOf:      asOf.Format(time.RFC3339),
		Stale:     false,
		DueDate:   dueDate,
	}
}

// classifyPJIRAErrorMessage maps pjira's own free-text error message onto
// scriptout's closed error taxonomy. pjira's not-found phrasing is
// consistently "issue <KEY> not found" [verified against
// modules/jira/pkg/pjira/client.go's GetIssue: `fmt.Errorf("pjira: issue
// %s not found", key)` on a 404] — matched on "not found" while explicitly
// excluding "executable" so exec's own "executable file not found in
// $PATH" (pjira missing from PATH entirely) is never misclassified as "this
// one issue doesn't exist" [mirrors
// cmd/pg-connector-issue-beads/internal/bd.go's classifyBDErrorMessage's
// identical care]. A transport-level 401/403 (pjira's GetIssue folds both
// into a generic "status <code>" message, unlike its dedicated
// AuthStatus check) is classified as unauthenticated — the taxonomy's
// closest fit, since scriptout has no separate "forbidden" sentinel.
// Anything else falls back to ErrUnavailable.
//
// One case is deliberately distinguished from that fallback:
// Client.Transition's own "no transition to state %q available" message
// [verified against phillipg-nix-repo-base's modules/jira/pkg/pjira/
// client.go] means the issue exists and pjira/Jira itself are reachable,
// but the CALLER supplied a targetState this issue's current workflow has
// no transition to — a caller-input problem, not a backend-availability
// one, matching pkg/provider/issue/iface.go's own Transition doc comment
// ("A well-formed rejection of an unrecognized targetState is this
// method's own error to report") and this docket's own multi-instance
// targeted-op resolution policy, which enumerates
// not_found/unauthenticated/unavailable/unknown_op/version_mismatch/
// invalid_argument as its short-circuit set: a target state that matches
// no available transition should be a distinct, clearly-classified error,
// not folded into unavailable's catch-all.
func classifyPJIRAErrorMessage(msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no transition to state") && strings.Contains(lower, "available"):
		return scriptout.WrapError(scriptout.ErrInvalidArgument, msg)
	case strings.Contains(lower, "not found") && !strings.Contains(lower, "executable"):
		return scriptout.WrapError(scriptout.ErrNotFound, msg)
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "403") ||
		strings.Contains(lower, "forbidden"):
		return scriptout.WrapError(scriptout.ErrUnauthenticated, msg)
	default:
		return scriptout.WrapError(scriptout.ErrUnavailable, msg)
	}
}

// Show implements issue.Provider.Show via `pjira issue <KEY>`. id is placed
// after a literal "--" terminator, ahead of pjira's own persistent --config
// flag [mirrors cmd/pg-connector-issue-beads/internal/backend.go's
// identical defense-in-depth for `bd show`, per bead pg2-usu5b/review
// finding A-8] — the `issue` subcommand itself defines no other flags today,
// but this keeps a caller-supplied id that happens to start with "--" from
// ever being parsed as a flag by pjira's own cobra/pflag layer.
func (b *Backend) Show(ctx context.Context, id string) (*schema.Issue, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	out, err := b.runner.Run(ctx, "issue", "--", id)
	if err != nil {
		return nil, classifyPJIRAErrorMessage(err.Error())
	}
	iss, decodeErr := decodePJIRAIssue(out)
	if decodeErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: decode issue: "+decodeErr.Error())
	}
	return toSchemaIssue(iss, time.Now().UTC()), nil
}

// pjiraCreateResult is `pjira create`'s own stdout shape (verified against
// phillipg-nix-repo-base's modules/jira/cmd/pjira/main.go's newCreateCmd:
// "writes {key,url} JSON"). Unlike pjiraIssue, this is deliberately NOT
// the full issue shape — Jira's own create endpoint
// (Client.CreateIssue/POST /rest/api/3/issue) returns only the new key
// (URL is derived client-side from it), never the resolved
// status/issuetype/labels a caller would need for a well-formed
// schema.Issue — so Create fetches the created issue's actual state via a
// follow-up Show(ctx, key) call rather than fabricating Title/State/etc.
// from input (which could drift from what Jira actually stored, e.g. a
// project-specific default workflow status).
type pjiraCreateResult struct {
	Key string `json:"key"`
}

// decodePJIRACreateResult decodes `pjira create`'s stdout, applying the
// same "empty key is a decode failure, never a silent zero-value success"
// guard as decodePJIRAIssue.
func decodePJIRACreateResult(raw string) (*pjiraCreateResult, error) {
	var res pjiraCreateResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, err
	}
	if res.Key == "" {
		return nil, errors.New("pjira: decoded create result has empty key")
	}
	return &res, nil
}

// Create implements issue.Provider.Create via `pjira create --project
// <PROJECT> --type <TYPE> --summary <TITLE> [--description <DESC>]`,
// followed by Show(ctx, key) to return the created issue's actual state
// [see pjiraCreateResult's doc comment for why the create call alone
// cannot answer issue.Provider.Create's full *schema.Issue return type].
//
// Title is this method's own required field (issue.IssueInput's doc
// comment: "all omitempty except Title"); Jira's create endpoint ALSO
// requires an issue type and a target project on every call, with no
// ambient default for either pjira itself resolves — IssueType is this
// backend's own additional required field (a caller-input problem, hence
// ErrInvalidArgument: distinct Jira project schemes have no single safe
// default type this backend could invent), while Project has no
// caller-supplied field at all on issue.IssueInput (it carries no Project
// field — only bd's Tracker/workspace concept does, per
// cmd/pg-connector-issue-beads), so it is resolved from this backend's own
// configuration (ResolveProject/EnvProject) instead — a configuration
// problem the caller cannot fix by changing their input, hence
// ErrUnavailable, mirroring ErrWorkspaceNotConfigured's identical
// classification in the sibling beads backend.
func (b *Backend) Create(ctx context.Context, input issue.IssueInput) (*schema.Issue, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: title required")
	}
	issueType := strings.TrimSpace(input.IssueType)
	if issueType == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: issue_type required (Jira requires an issue type per project, with no safe cross-project default)")
	}
	project, err := ResolveProject(b.getenvFunc())
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
	}

	args := []string{"create", "--project", project, "--type", issueType, "--summary", title}
	if desc := strings.TrimSpace(input.Description); desc != "" {
		args = append(args, "--description", desc)
	}
	out, runErr := b.runner.Run(ctx, args...)
	if runErr != nil {
		return nil, classifyPJIRAErrorMessage(runErr.Error())
	}
	created, decodeErr := decodePJIRACreateResult(out)
	if decodeErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: decode create result: "+decodeErr.Error())
	}
	return b.Show(ctx, created.Key)
}

// Comment implements issue.Provider.Comment via `pjira comment <KEY>
// <BODY>` (verified against phillipg-nix-repo-base's
// modules/jira/cmd/pjira/main.go's newCommentCmd: "writes {key,id} JSON").
// Like cmd/pg-connector-issue-beads/internal/backend.go's own
// Comment/Transition [carry-over basis], this discards the echoed
// payload — success is decided from the exec's own exit code (via
// b.runner.Run's error return), never from parsing stdout, mirroring
// bd.go's b.run doc comment on why Comment/Transition need no positive
// payload confirmation.
func (b *Backend) Comment(ctx context.Context, id, body string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: comment body required")
	}
	if _, err := b.runner.Run(ctx, "comment", "--", id, body); err != nil {
		return classifyPJIRAErrorMessage(err.Error())
	}
	return nil
}

// Transition implements issue.Provider.Transition via `pjira transition
// <KEY> <TO>` (verified against phillipg-nix-repo-base's
// modules/jira/cmd/pjira/main.go's newTransitionCmd: "writes {key,to}
// JSON"). An unrecognized targetState is a distinct, clearly-classified
// error — see classifyPJIRAErrorMessage's doc comment for the "no
// transition to state" case Client.Transition returns for exactly this.
func (b *Backend) Transition(ctx context.Context, id, targetState string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	targetState = strings.TrimSpace(targetState)
	if targetState == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: target_state required")
	}
	if _, err := b.runner.Run(ctx, "transition", "--", id, targetState); err != nil {
		return classifyPJIRAErrorMessage(err.Error())
	}
	return nil
}

// CheckAuth implements the optional pkg/provider.AuthChecker capability via
// `pjira auth-status` — unlike pg-connector-issue-beads (whose bd/dolt
// setup has no per-caller credential concept at all), this backend's
// credential resolution CAN be health-checked independently of a real op
// (per pkg/provider.AuthChecker's own doc comment: "an optional capability
// a backend's concrete provider may implement to support a one-shot auth
// preflight"), because pjira itself ships exactly that op (verified live:
// `pjira auth-status --help`,
// and its own main.go/client.go — a GET to Jira's /rest/api/3/myself with
// no side effects). A non-"OK" state (MISSING/UNAUTHENTICATED/FORBIDDEN/
// ERROR, or a transport failure) is reported as a plain error carrying
// pjira's own state string; this method's caller
// (pkg/provider/issue.NewDispatchTable) only needs CheckAuth to be nil vs
// non-nil and reads .Error() for Detail, so no scriptout sentinel wrapping
// is needed here (mirroring cmd/pg-connector-pr-github's own
// CheckAuth-delegates-to-gh precedent).
func (b *Backend) CheckAuth(ctx context.Context) error {
	out, err := b.runner.Run(ctx, "auth-status")
	if err != nil {
		return err
	}
	if state := strings.TrimSpace(out); state != "OK" {
		return errors.New("pjira auth-status: " + state)
	}
	return nil
}

// List implements issue.Provider.List via `pjira search --jql <JQL>
// --all` (bead pg2-2j5ac.28.1, design's own "implement List against
// Jira JQL" Files-section note). Each element of query is ALREADY the
// full JQL text a caller's config.queries value carries — unlike the
// sibling beads backend's own bd-argv-vector grammar, JQL is Jira's own
// query language, so there is nothing further for this backend to parse
// or restrict (no equivalent "only ready/list as the first token" rule
// applies here). --all fetches every page up front rather than one page
// at a time, so PresentIDs reflects "the complete id set the query
// matches right now" even though this packet's own list op
// never itself paginates (cursor is always null). Every expression's
// matches are unioned, deduplicated by issue key (design's "run
// each, union results deduplicated by id" rule); Truncated is true if ANY
// expression's own search reported truncated (pjira's own --limit
// default, or a config-set limit lower than the match count, per its
// --help: "max results per page").
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.IssueListResult, error) {
	seen := make(map[string]bool)
	entities := make([]schema.Issue, 0)
	truncated := false
	for _, jql := range query {
		jql = strings.TrimSpace(jql)
		if jql == "" {
			continue
		}
		out, runErr := b.runner.Run(ctx, "search", "--jql", jql, "--all")
		if runErr != nil {
			return nil, classifyPJIRAErrorMessage(runErr.Error())
		}
		result, decodeErr := decodePJIRASearchResult(out)
		if decodeErr != nil {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: decode search result: "+decodeErr.Error())
		}
		if result.Truncated {
			truncated = true
		}
		asOf := time.Now().UTC()
		for i := range result.Items {
			item := result.Items[i]
			if item.Key == "" || seen[item.Key] {
				continue
			}
			seen[item.Key] = true
			entities = append(entities, *toSchemaIssue(&item, asOf))
		}
	}
	ids := make([]string, 0, len(entities))
	for _, e := range entities {
		ids = append(ids, e.ID)
	}
	res := &schema.IssueListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: truncated}
	if idsOnly {
		res.Entities = nil
	}
	return res, nil
}

// Search implements the search capability's search.Provider via `pjira
// search --jql <QUERY> --all` (bead pg2-8hcnx) — query is passed straight
// through as JQL text, exactly the same "config.queries entries are
// already raw JQL, nothing further to parse" convention List's own doc
// comment above already establishes: unlike List, the search wire op
// receives its query argument directly from the caller with no
// config.queries resolution (pkg/provider/search/dispatch.go's own
// "search" handler), so there is only ever one JQL expression to run per
// call. fields is unused: this backend populates no Attributes beyond
// SearchResult's own core set [freedom boundary — pjira's own search
// response carries nothing this backend maps to Attributes today].
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	out, runErr := b.runner.Run(ctx, "search", "--jql", query, "--all")
	if runErr != nil {
		return nil, classifyPJIRAErrorMessage(runErr.Error())
	}
	result, decodeErr := decodePJIRASearchResult(out)
	if decodeErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: decode search result: "+decodeErr.Error())
	}
	results := make([]schema.SearchResult, 0, len(result.Items))
	for i := range result.Items {
		item := result.Items[i]
		results = append(results, schema.SearchResult{
			Type:   "issue",
			ID:     item.Key,
			Title:  item.Summary,
			URL:    item.URL,
			Source: "pg-connector-issue-jira",
		})
	}
	return results, nil
}

// Update implements issue.Provider.Update. pjira exposes NO op to change
// an existing issue's fields at all (verified live: `pjira --help` lists
// only issue/search/create/comment/transition/auth-status) — this is the
// SAME kind of gap Create/Comment/Transition were once stubbed against
// before pg2-7p4mr's children landed pjira create/transition/comment
// (see this file's header comment); no equivalent pjira op has landed for
// a field-level update yet, so this method returns the same documented
// ErrUnavailable stub that earlier gap left in place for those three,
// rather than silently no-opping or fabricating a partial write. Adding
// an update op to pjira itself (phillipg-nix-repo-base) is out of this
// bead's own scope [freedom boundary].
func (b *Backend) Update(ctx context.Context, id string, fields issue.IssueUpdateFields) (*schema.Issue, error) {
	if strings.TrimSpace(id) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: no update op available (issue field updates are not yet supported against Jira)")
}

// Close implements issue.Provider.Close by mapping to a resolving
// transition (bead pg2-2j5ac.28.3's own Contract), reusing Transition's
// own `pjira transition` plumbing. When reason is non-empty, it is
// recorded as a comment BEFORE the transition — pjira's transition op
// itself takes no comment/resolution-note parameter of its own [freedom
// boundary: the implementer's own choice for how to carry reason through
// to Jira, within this backend's existing comment/transition
// conventions], so a comment is the closest available carrier. A comment
// failure short-circuits before ever attempting the transition, so a
// caller never sees a "closed" result while its own reason silently
// failed to record.
func (b *Backend) Close(ctx context.Context, id, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		if err := b.Comment(ctx, id, reason); err != nil {
			return err
		}
	}
	return b.Transition(ctx, id, resolvingState)
}

// Deps implements issue.Provider.Deps. pjira exposes no dependency/
// issue-link query op at all, so this backend has no dependency concept
// of its own — issue.Provider.Deps' own doc comment: "a backend with no
// dependency concept ... answers an empty result, never an error."
func (b *Backend) Deps(ctx context.Context, id string, full bool) (*schema.IssueDepsResult, error) {
	if strings.TrimSpace(id) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	return &schema.IssueDepsResult{IDs: []string{}}, nil
}
