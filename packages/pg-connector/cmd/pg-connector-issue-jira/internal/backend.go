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
// Create/Comment/Transition are NOT implemented against a real op: this is
// a documented, escalated gap, not new code with a style precedent to
// follow — see writeNotSupportedErr's own doc comment below for why, and
// pg2-2j5ac.17.1's bd comment for the full escalation record.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-issue-jira's concrete issue.Provider
// implementation.
type Backend struct {
	runner Runner
}

// New returns a Backend wrapping the given Runner. Production wiring passes
// NewCLIRunner(); tests inject a fake Runner.
func New(r Runner) *Backend {
	return &Backend{runner: r}
}

// Compile-time check that Backend satisfies the issue capability's
// Provider interface.
var _ issue.Provider = (*Backend)(nil)

// Compile-time check that Backend also implements the optional AuthChecker
// capability (see CheckAuth's doc comment for why this backend, unlike
// pg-connector-issue-beads, can implement it).
var _ provider.AuthChecker = (*Backend)(nil)

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
}

// pjiraUser is pjira's own nested user shape (model.go's User), used here
// only for Assignee.
type pjiraUser struct {
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
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
func toSchemaIssue(iss *pjiraIssue) *schema.Issue {
	var assignee string
	if iss.Assignee != nil {
		assignee = iss.Assignee.DisplayName
		if assignee == "" {
			assignee = iss.Assignee.Email
		}
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
func classifyPJIRAErrorMessage(msg string) error {
	lower := strings.ToLower(msg)
	switch {
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
	return toSchemaIssue(iss), nil
}

// writeNotSupportedErr is returned by every write method below (Create/
// Comment/Transition).
//
// This is a DOCUMENTED, ESCALATED GAP, not a bug in this backend's own
// code and not something a later reader should "fix" by inventing a
// plausible-looking pjira subcommand that does not exist. Verified live
// against the real pjira binary on this machine's PATH (2026-09-08,
// `pjira --help` and each subcommand's own --help) and independently
// confirmed by its own source
// (phillipg-nix-repo-base/modules/jira/cmd/pjira/main.go's NewRootCmd
// registers exactly `issue`, `search`, and `auth-status` — no create/
// comment/transition command exists anywhere in that binary): the generic
// Jira CLI this packet's own binding decisions commit to exec'ing for
// every issue.Provider method (mirroring
// cmd/pg-connector-issue-beads/internal/runner.go's identical CLI-exec-seam
// convention) has NO write capability today, and phillipg-nix-repo-base's
// own maintainers have not built one yet — this is a real, current state
// of that external tool, not an oversight in this backend's own code.
//
// Resolving this requires an operator decision this packet's implementer
// must not make unilaterally (extend pjira with write support first;
// explicitly authorize a first-party direct-REST write path for this
// backend only, which would deviate from this packet's own CLI-exec
// binding decision; or accept this backend as read-only for now) — see the
// escalation note recorded as a comment on bead pg2-2j5ac.17.1. Until that
// lands, these methods validate their inputs (matching every sibling
// backend's own convention) and then report the gap through the wire
// taxonomy's closest fit (unavailable) rather than silently pretending to
// succeed or fabricating a CLI invocation that would misclassify a genuine
// future failure.
func writeNotSupportedErr(op string) error {
	return scriptout.WrapError(scriptout.ErrUnavailable,
		"issue-jira: "+op+": not supported — the resolved Jira CLI (pjira) has no write "+
			"operation as of 2026-09-08 (design non-goal; see pg2-2j5ac.17.1 escalation note)")
}

// Create implements issue.Provider.Create. NOT implemented against a real
// op — see writeNotSupportedErr's doc comment.
func (b *Backend) Create(ctx context.Context, input issue.IssueInput) (*schema.Issue, error) {
	if strings.TrimSpace(input.Title) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: title required")
	}
	return nil, writeNotSupportedErr("create")
}

// Comment implements issue.Provider.Comment. NOT implemented against a
// real op — see writeNotSupportedErr's doc comment.
func (b *Backend) Comment(ctx context.Context, id, body string) error {
	if strings.TrimSpace(id) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	if strings.TrimSpace(body) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: comment body required")
	}
	return writeNotSupportedErr("comment")
}

// Transition implements issue.Provider.Transition. NOT implemented against
// a real op — see writeNotSupportedErr's doc comment.
func (b *Backend) Transition(ctx context.Context, id, targetState string) error {
	if strings.TrimSpace(id) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	if strings.TrimSpace(targetState) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: target_state required")
	}
	return writeNotSupportedErr("transition")
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
