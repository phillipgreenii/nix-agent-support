package interpret

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// Status categories an issue's status maps to. The set is deliberately tiny:
// the first time-based issue rule only needs to know whether work is in
// progress, and the connector exposes no tracker-native status category.
const (
	IssueStatusCategoryInProgress = "in_progress"
	IssueStatusCategoryDone       = "done"
	IssueStatusCategoryOther      = "other"
)

// IssueAttentionFacts is the narrow set of facts an attention rule over a
// stored issue entity needs, the issue counterpart of PRAttentionFacts. They
// are derived from the issue's stored facts and the configuration, never from
// the clock, so a rule combines them with the injected clock itself.
type IssueAttentionFacts struct {
	// Status is the tracker's status name, as stored.
	Status string
	// StatusCategory is IssueStatusCategoryInProgress when Status is one of
	// config jira.in_progress_statuses (case-insensitive), else
	// IssueStatusCategoryOther.
	StatusCategory string
	// Assignee is the issue's assignee as the tracker names them; empty when
	// unassigned.
	Assignee string
	// InProgressSince is when the issue entered its current status, set only
	// when StatusCategory is in progress and the time is known; the zero time
	// otherwise.
	InProgressSince time.Time
	// Done is true when Status is one of config jira.done_statuses
	// (case-insensitive): the issue needs nothing more, so a due date no
	// longer matters. StatusCategory is then IssueStatusCategoryDone, unless
	// the status is also listed as in progress (in progress wins).
	Done bool
	// DueDateKnown is true when the connector supplied a due date that
	// parses; DueDate is then valid. When false, DueDate being zero means
	// "no due date, or one we cannot read", and a rule MUST NOT raise on it
	// (INV-ATTNEVAL-6).
	DueDateKnown bool
	// DueDate is the issue's due date. For DueDateOnly it is midnight UTC of
	// the due calendar day (only its year, month and day are meaningful);
	// otherwise it is the due instant.
	DueDate time.Time
	// DueDateOnly is true when the tracker supplied a date with no time of
	// day (Jira's duedate, "2026-10-09"): the issue is then due at the END of
	// that calendar day, in the evaluating clock's zone.
	DueDateOnly bool
	// OperatorFactsKnown is true when the connector supplied the operator
	// facts (it does so for an issue assigned to the operator). When false,
	// OperatorUpdatedAt being zero means "unknown", NOT "never updated", and a
	// rule MUST NOT raise on it (INV-ATTNEVAL-6).
	OperatorFactsKnown bool
	// OperatorUpdatedAt is the latest operator comment or operator status
	// transition (other users and bots excluded); the zero time when the
	// operator has not updated the issue or the facts are unknown.
	OperatorUpdatedAt time.Time
}

// issueAttentionShow is the hand-decoded subset of the issue show payload the
// attention facts read.
type issueAttentionShow struct {
	State             string `json:"state"`
	Assignee          string `json:"assignee"`
	StatusChangedAt   string `json:"status_changed_at"`
	OperatorUpdatedAt string `json:"operator_updated_at"`
	DueDate           string `json:"due_date"`
}

// DeriveIssueAttentionFacts decodes a stored entity's facts JSON (a
// gather.IssueFacts document) and derives IssueAttentionFacts under cfg. It
// is pure and reads no clock. A document with no issue_show, or one that does
// not decode, is an error: the caller treats such an entity as not evaluable.
// An unparsable timestamp is treated as unknown rather than an error.
func DeriveIssueAttentionFacts(factsJSON string, cfg *config.Config) (IssueAttentionFacts, error) {
	var facts gather.IssueFacts
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return IssueAttentionFacts{}, fmt.Errorf("interpret: decode stored issue facts: %w", err)
	}
	if len(facts.IssueShow) == 0 {
		return IssueAttentionFacts{}, errors.New("interpret: stored facts carry no issue_show")
	}
	var show issueAttentionShow
	if err := json.Unmarshal(facts.IssueShow, &show); err != nil {
		return IssueAttentionFacts{}, fmt.Errorf("interpret: decode issue show: %w", err)
	}

	out := IssueAttentionFacts{
		Status:         show.State,
		StatusCategory: IssueStatusCategoryOther,
		Assignee:       show.Assignee,
	}
	for _, s := range cfg.InProgressStatuses() {
		if show.State != "" && strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(show.State)) {
			out.StatusCategory = IssueStatusCategoryInProgress
			break
		}
	}
	for _, d := range cfg.DoneStatuses() {
		if show.State != "" && strings.EqualFold(strings.TrimSpace(d), strings.TrimSpace(show.State)) {
			out.Done = true
			if out.StatusCategory != IssueStatusCategoryInProgress {
				out.StatusCategory = IssueStatusCategoryDone
			}
			break
		}
	}
	out.DueDate, out.DueDateOnly, out.DueDateKnown = parseDueDate(show.DueDate)
	changed, changedOK := parseFactTime(show.StatusChangedAt)
	operator, _ := parseFactTime(show.OperatorUpdatedAt)
	out.OperatorFactsKnown = changedOK
	out.OperatorUpdatedAt = operator
	if out.StatusCategory == IssueStatusCategoryInProgress && changedOK {
		out.InProgressSince = changed
	}
	return out, nil
}

// parseFactTime parses an RFC3339 timestamp; ok is false for "" or garbage.
func parseFactTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// parseDueDate parses an issue's due date: either a bare calendar date
// ("2026-10-09", Jira's duedate; dateOnly is then true and the time is
// midnight UTC) or an RFC3339 instant (beads' due_at). ok is false for ""
// or anything else.
func parseDueDate(s string) (t time.Time, dateOnly, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, false
	}
	if d, err := time.Parse("2006-01-02", s); err == nil {
		return d.UTC(), true, true
	}
	if d, err := time.Parse(time.RFC3339, s); err == nil {
		return d.UTC(), false, true
	}
	return time.Time{}, false, false
}
