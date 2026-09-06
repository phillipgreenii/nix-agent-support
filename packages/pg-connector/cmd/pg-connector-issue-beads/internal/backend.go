// backend.go: Backend implements pkg/provider/issue.Provider against this
// workspace's own bd tracker — the first Tier-2 Issue backend [design: §2,
// §5.1]. Show's read side and Create's write side follow the concrete
// bd-CLI-wrapper precedent packages/pg-pr/pkg/beads/mergerequest.go and
// action.go already establish (exec bd, parse `--json`/parse the returned
// id); Comment and Transition are new code with no existing pg-pr wrapper
// to port, using that same exec-and-parse STYLE as their only precedent
// [bead: Carry-over basis].
//
// AuthChecker (pkg/provider.AuthChecker, §4.6) is deliberately NOT
// implemented: this workspace's bd/dolt setup is a local embedded or
// shared-launchd-agent Dolt server with no remote, per-caller credential
// concept — there is no token/session for CheckAuth to validate, the same
// reasoning §4.6 gives for pg-connector-scm-git's local git backend.
// pg-connector's own auth_status fan-out (cmd/pg-connector/auth.go)
// already recognizes a provider with no auth_status dispatch-table entry
// generically (via the wire-level unknown_op sentinel) and reports it as
// disabled/"not applicable" — no forced or meaningless answer is needed
// here.
package internal

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-issue-beads' concrete issue.Provider
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

// Vocabulary is this backend's declared, non-empty state vocabulary — the
// concrete backing for the sibling "generic issue entity/capability"
// packet's vocabulary.state check [design: §4.3 AC]. These are bd's own
// BUILT-IN statuses, read directly from `bd statuses --json` (bd v1.2.2) —
// open/in_progress/blocked/deferred/closed/pinned/hooked — rather than
// guessed from memory of how bd is used elsewhere [bead: Produces]. A
// workspace MAY additionally configure custom statuses via
// `bd config set status.custom ...`; those are not reflected here since
// they are per-workspace configuration, not a fixed part of bd itself, and
// discovering them would require this backend to shell out to
// `bd statuses` on every capabilities call for no benefit any known
// consumer needs today.
var Vocabulary = []string{
	"open",
	"in_progress",
	"blocked",
	"deferred",
	"closed",
	"pinned",
	"hooked",
}

// run execs `bd args... --json` and returns the decoded payload, or a
// scriptout-taxonomy error. It handles bd's two observed failure shapes
// uniformly (see bd.go's doc comment): a well-formed
// {"data":{"error":...}} envelope still written on a non-zero exit, and a
// stderr-only failure with empty stdout (falls back to classifying the
// wrapped exec error's own message, which still carries bd's stderr tail).
func (b *Backend) run(ctx context.Context, args ...string) (jsonData []byte, err error) {
	out, runErr := b.runner.Run(ctx, args...)
	data, bdErrMsg, parseErr := decodeBDEnvelope(out)
	if parseErr != nil {
		if runErr != nil {
			return nil, classifyBDErrorMessage(runErr.Error())
		}
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, parseErr.Error())
	}
	if bdErrMsg != "" {
		return nil, classifyBDErrorMessage(bdErrMsg)
	}
	return data, nil
}

// formatPriority renders bd's integer priority (0-4, 0=highest) as the
// "P<n>" form bd's own `-p`/`--priority` flag documents as an accepted
// alias ("Priority (0-4 or P0-P4, 0=highest)") — a faithful round-trippable
// rendering of bd's actual model rather than an invented High/Medium/Low
// scale (pg-pr's carried-over api.Issue.Priority convention has no fixed
// scale of its own; each backend's Show renders whatever its tracker
// returns as a string).
func formatPriority(p int) string {
	return fmt.Sprintf("P%d", p)
}

// toSchemaIssue maps a decoded bdIssue onto the issue capability's shared
// wire shape. URL is left empty: bd is a local CLI tool with no bd-native
// hosted web URL convention in this workspace (unlike a GitHub/Jira issue),
// and schema.Issue's own doc already treats an empty URL as: "empty when
// the backend does not supply one."
func toSchemaIssue(iss *bdIssue) *schema.Issue {
	return &schema.Issue{
		ID:        iss.ID,
		Title:     iss.Title,
		State:     iss.Status,
		Priority:  formatPriority(iss.Priority),
		Labels:    iss.Labels,
		IssueType: iss.IssueType,
	}
}

// Show implements issue.Provider.Show via `bd show <id> --json`.
func (b *Backend) Show(ctx context.Context, id string) (*schema.Issue, error) {
	if strings.TrimSpace(id) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	data, err := b.run(ctx, "show", id, "--json")
	if err != nil {
		return nil, err
	}
	iss, err := bdIssueFromArray(data)
	if err != nil {
		return nil, err
	}
	return toSchemaIssue(iss), nil
}

// Create implements issue.Provider.Create via `bd create ... --json`. It
// carries over action.go's CreateAction "exec bd create ..., parse the
// returned id" plumbing [bead: Carry-over basis], adapted to a plain bd
// issue with none of CreateAction's parent-child/discovered-from wiring —
// that wiring is specific to pg-pr's review workflow and does not carry
// over.
func (b *Backend) Create(ctx context.Context, input issue.IssueInput) (*schema.Issue, error) {
	if strings.TrimSpace(input.Title) == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: title required")
	}
	args := []string{"create", "--title", input.Title, "--json"}
	if input.Priority != "" {
		args = append(args, "-p", input.Priority)
	}
	if input.IssueType != "" {
		args = append(args, "--type", input.IssueType)
	}
	if len(input.Labels) > 0 {
		args = append(args, "--labels", strings.Join(input.Labels, ","))
	}
	data, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	iss, err := bdIssueFromObject(data)
	if err != nil {
		return nil, err
	}
	return toSchemaIssue(iss), nil
}

// Comment implements issue.Provider.Comment via `bd comment <id> <body>
// --json`. New code (no existing pg-pr wrapper to port), using
// action.go/mergerequest.go's exec-and-parse pattern as its style precedent
// only.
func (b *Backend) Comment(ctx context.Context, id, body string) error {
	if strings.TrimSpace(id) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	if strings.TrimSpace(body) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: comment body required")
	}
	_, err := b.run(ctx, "comment", id, body, "--json")
	return err
}

// Transition implements issue.Provider.Transition via `bd update <id>
// --status <targetState> --json`. New code (no existing pg-pr wrapper to
// port). Freedom-boundary choice: this method does NOT validate
// targetState against Vocabulary client-side — it defers entirely to bd
// update's own rejection (bd's `invalid status "..."` message, classified
// generically as ErrUnavailable by classifyBDErrorMessage — a well-formed
// rejection of an unrecognized target state per issue.Provider.Transition's
// own doc comment, not this method's job to pre-validate).
func (b *Backend) Transition(ctx context.Context, id, targetState string) error {
	if strings.TrimSpace(id) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	}
	if strings.TrimSpace(targetState) == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: target_state required")
	}
	_, err := b.run(ctx, "update", id, "--status", targetState, "--json")
	return err
}
