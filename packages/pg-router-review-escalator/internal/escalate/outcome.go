// Package escalate is the functional core of pg-router-review-escalator: given
// the outcome of one `pg-connector pr review submit` call, it keeps exactly one
// open escalation per PR while the stale pending review is unremovable, and
// closes it when the PR's review next resolves.
//
// The package holds no state of its own. The tracker is the source of truth
// (the open escalation bead carries its own dedupe key, reason, head and
// last-notified time as metadata), and every side effect goes through the
// Tracker and Notifier ports, so the whole policy is testable with fakes and a
// fake clock.
package escalate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The statuses of the pg-connector `pr review submit` contract (entity change
// flow design 9.1, bead pg2-kftf9.13). Only StatusBlocked escalates; every
// other status resolves an escalation. StatusAppend and StatusNoChange are the
// create-or-append statuses (pending-review reuse design, rollout step 1):
// they are tolerated exactly like StatusPosted, so the create-or-append tool
// can ship without every review failing here.
const (
	StatusPosted   = "posted"
	StatusSkipped  = "skipped"
	StatusReplaced = "replaced"
	StatusAppend   = "append"
	StatusNoChange = "no_change"
	StatusBlocked  = "blocked_human_pending"
)

// The reasons that accompany StatusBlocked (design 9.1).
const (
	ReasonDetectionFailed = "detection_failed"
	ReasonHumanEdited     = "human_edited"
	ReasonArchiveFailed   = "archive_failed"
	ReasonDeleteRefused   = "delete_refused"
)

// Outcome is the part of one review-submit result that escalation needs.
type Outcome struct {
	// PR is the pg-connector PR id the submit was keyed on; it is the stable
	// per-PR dedupe key.
	PR      string
	Status  string
	Reason  string
	Message string
	// HeadSHA is the PR head the submit was posted against.
	HeadSHA string
	// ReviewURL is the web URL of the pending review that could not be removed;
	// empty when no URL is known (reason detection_failed).
	ReviewURL string
}

// ErrNotAnOutcome is returned by ParseOutcome for input that is a well-formed
// JSON document but not a successful review-submit result: a wire error
// envelope, or a result with no recognizable status. The caller MUST NOT treat
// it as "nothing to do": a submit whose status cannot be read is not an
// outcome this tool can act on, and silence would hide a contract drift.
var ErrNotAnOutcome = errors.New("not a review submit outcome")

// wireEnvelope is pg-connector's targeted-op envelope: {"result": ...} on
// success or {"error": {"code", "message"}} on failure.
type wireEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type submitResult struct {
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	HeadSHA       string `json:"head_sha"`
	PendingReview *struct {
		URL string `json:"url"`
	} `json:"pending_review"`
}

// ParseOutcome decodes the stdout of `pg-connector pr review submit` (the wire
// envelope) or a bare result object, for the given PR id.
func ParseOutcome(pr string, raw []byte) (Outcome, error) {
	if strings.TrimSpace(pr) == "" {
		return Outcome{}, errors.New("PR id is empty")
	}
	if strings.ContainsAny(pr, ";\n") {
		return Outcome{}, errors.New("PR id must not contain ';' or a newline (it is stored in a ';'-joined roll-up list)")
	}
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Outcome{}, fmt.Errorf("decode submit output: %w", err)
	}
	if env.Error != nil {
		return Outcome{}, fmt.Errorf("%w: submit answered with error %q: %s", ErrNotAnOutcome, env.Error.Code, env.Error.Message)
	}
	body := raw
	if len(env.Result) > 0 {
		body = env.Result
	}
	var res submitResult
	if err := json.Unmarshal(body, &res); err != nil {
		return Outcome{}, fmt.Errorf("decode submit result: %w", err)
	}
	switch res.Status {
	case StatusPosted, StatusSkipped, StatusReplaced, StatusAppend, StatusNoChange, StatusBlocked:
	case "":
		return Outcome{}, fmt.Errorf("%w: the result carries no status", ErrNotAnOutcome)
	default:
		return Outcome{}, fmt.Errorf("%w: unknown status %q", ErrNotAnOutcome, res.Status)
	}
	o := Outcome{PR: pr, Status: res.Status, Reason: res.Reason, Message: res.Message, HeadSHA: res.HeadSHA}
	if res.PendingReview != nil {
		o.ReviewURL = res.PendingReview.URL
	}
	if o.Status == StatusBlocked && o.Reason == "" {
		return Outcome{}, fmt.Errorf("%w: blocked_human_pending carries no reason", ErrNotAnOutcome)
	}
	return o, nil
}
