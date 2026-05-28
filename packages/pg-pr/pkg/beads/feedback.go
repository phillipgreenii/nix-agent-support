// Package beads — feedback bead wrappers (custom bd type=feedback,
// status=hooked).
//
// Feedback beads are children of a processing-cycle bead. Each represents
// a single upstream event the LLM may address (a comment thread, a CI
// failure, a review thread, …) and carries a fingerprint so the sync
// engine can dedup across runs.
package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxBdTitleLen mirrors bd's enforced limit (`title must be 500
// characters or less`). Feedback bodies routinely exceed it (CodeRabbit
// auto-summaries, long review-thread first lines); truncating the
// derived title keeps creates succeeding while the body retains the
// full upstream content.
const maxBdTitleLen = 500

// maxBdDescriptionLen is bd's `description TEXT NOT NULL` column cap
// (MySQL TEXT == 65,535 bytes). Bodies above this throw a `too large
// for column 'description'` error from dolt; truncate before passing
// the body to bd. Callers should strip known no-information blobs
// (e.g. CodeRabbit's internal-state base64) upstream so the cap is
// almost never hit; this is the defensive last line of defense.
const maxBdDescriptionLen = 65_535

// truncateBytes returns s shortened to at most maxLen bytes, suffixed
// with a fixed marker when truncation happens. Used for both bd's
// title and description column caps; the byte cap is what dolt
// enforces, so a rune-aware cap that still exceeds it would overflow.
// truncateBytes snaps to a valid UTF-8 rune boundary so the result is
// still a well-formed string.
func truncateBytes(s, marker string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < len(marker) {
		return s[:maxLen]
	}
	budget := maxLen - len(marker)
	end := 0
	for i, r := range s {
		next := i + utf8.RuneLen(r)
		if next > budget {
			break
		}
		end = next
	}
	return s[:end] + marker
}

// truncateTitle is the title-specific wrapper around truncateBytes,
// using a horizontal ellipsis as the truncation marker.
func truncateTitle(title string, maxLen int) string {
	return truncateBytes(title, "…", maxLen)
}

// truncateDescription caps a feedback body at bd's TEXT column limit.
// The marker is verbose so the bead reader can tell the content was
// shortened (vs the upstream simply being terse).
func truncateDescription(body string, maxLen int) string {
	return truncateBytes(body, "\n\n[truncated to fit bd description column]", maxLen)
}

// CreateFeedbackInput is the typed input for creating a feedback bead.
type CreateFeedbackInput struct {
	ProcessingCycleID string
	Kind              FeedbackKind
	ExternalID        string
	Fingerprint       string
	AuthorRole        AuthorRole
	Title             string
	Body              string
}

// FeedbackFields is the metadata schema persisted on each feedback bead.
type FeedbackFields struct {
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	AuthorRole  string `json:"author_role,omitempty"`
	// ReplyDraft holds the body the LLM wants the sync engine to post as a
	// reply to the upstream thread. The sync engine reads it on the next
	// pass, posts via vcs.ReplyToThread, and writes ResponseID back.
	ReplyDraft string `json:"reply_draft,omitempty"`
	// ResponseID is the upstream comment id returned by ReplyToThread; its
	// presence is the idempotency marker — sync skips posting again when set.
	ResponseID string `json:"response_id,omitempty"`
}

// Feedback is a parsed view of a feedback bead.
type Feedback struct {
	ID     string
	Title  string
	Status string
	Fields FeedbackFields
}

// CreateFeedback creates a feedback bead and wires the parent-child link
// to the given processing-cycle.
func (c *Client) CreateFeedback(ctx context.Context, in CreateFeedbackInput) (string, error) {
	if in.ProcessingCycleID == "" {
		return "", errors.New("feedback: processing-cycle id required")
	}
	if in.Kind == "" {
		return "", errors.New("feedback: kind required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return "", errors.New("feedback: title required")
	}
	fields := FeedbackFields{
		Kind:        string(in.Kind),
		ExternalID:  in.ExternalID,
		Fingerprint: in.Fingerprint,
		AuthorRole:  string(in.AuthorRole),
	}
	metaJSON, err := encodeFeedbackMetadata(fields)
	if err != nil {
		return "", err
	}
	body := in.Body
	if strings.TrimSpace(body) == "" {
		body = in.Title
	}
	// bd validates `title must be 500 characters or less`. Derived titles
	// (first line of an upstream comment) can blow past that on CodeRabbit
	// summaries and long review-thread bodies. Truncate to keep the bead
	// create succeeding; the body argument below retains the full content
	// (modulo description-column truncation immediately below).
	title := truncateTitle(in.Title, maxBdTitleLen)
	// bd's description column is TEXT (~64KB). Upstream callers should
	// strip known no-information blobs first (CodeRabbit internal-state,
	// etc.); this defensive cap catches anything that still overflows.
	body = truncateDescription(body, maxBdDescriptionLen)
	out, err := c.Runner.Run(ctx,
		"create",
		"--type=feedback",
		"--title", title,
		"-d", body,
		"--metadata", metaJSON,
		"--silent",
	)
	if err != nil {
		return "", fmt.Errorf("create feedback: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// bd's custom-type feedback creates issues with the default status
	// (open). Move to "hooked" so the lifecycle is explicit. bd warns
	// about unknown statuses on some versions; we swallow that.
	if _, err := c.Runner.Run(ctx, "update", id, "--status=hooked"); err != nil {
		// Some bd builds reject custom statuses; that's tolerable for now.
		_ = err
	}
	// Parent-child link to the processing-cycle.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, in.ProcessingCycleID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link feedback %s to cycle %s: %w", id, in.ProcessingCycleID, err)
	}
	return id, nil
}

// CloseFeedback closes a feedback bead with the given reason.
func (c *Client) CloseFeedback(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("feedback: id required")
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		if strings.Contains(err.Error(), "already closed") {
			return nil
		}
		return fmt.Errorf("close feedback: %w", err)
	}
	return nil
}

// MarkFeedbackResolvedUpstream closes a feedback bead with the
// canonical "resolved-upstream" reason.
func (c *Client) MarkFeedbackResolvedUpstream(ctx context.Context, id string) error {
	return c.CloseFeedback(ctx, id, "resolved-upstream")
}

// ListFeedback returns all feedback beads (open + closed if includeClosed).
// Pass cycleID to filter to feedback under a specific processing-cycle;
// empty cycleID returns all feedback beads in the workspace.
func (c *Client) ListFeedback(ctx context.Context, cycleID string, includeClosed bool) ([]Feedback, error) {
	args := []string{"list", "--type=feedback", "--json", "--limit=0"}
	if includeClosed {
		args = append(args, "--all")
	}
	out, err := c.Runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	out2 := make([]Feedback, 0, len(issues))
	for _, iss := range issues {
		fb := Feedback{
			ID:     iss.ID,
			Title:  iss.Title,
			Status: iss.Status,
			Fields: feedbackFieldsFromMetadata(iss.Metadata),
		}
		if cycleID != "" && !c.isChildOf(ctx, fb.ID, cycleID) {
			continue
		}
		out2 = append(out2, fb)
	}
	return out2, nil
}

// GetFeedback returns a single feedback bead by ID, or nil if not found
// (or if the bead is not a feedback issue). Includes closed beads.
func (c *Client) GetFeedback(ctx context.Context, id string) (*Feedback, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("feedback: id required")
	}
	out, err := c.Runner.Run(ctx, "list", "--all", "--id="+id, "--json")
	if err != nil {
		return nil, fmt.Errorf("get feedback: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	for _, iss := range issues {
		if iss.ID != id {
			continue
		}
		if iss.Type != "" && iss.Type != TypeFeedback {
			// Not a feedback bead.
			return nil, nil
		}
		fb := Feedback{
			ID:     iss.ID,
			Title:  iss.Title,
			Status: iss.Status,
			Fields: feedbackFieldsFromMetadata(iss.Metadata),
		}
		return &fb, nil
	}
	return nil, nil
}

// FindFeedbackByFingerprint returns the feedback bead under the given
// processing-cycle whose Fingerprint equals fingerprint, or nil if none
// match. Includes closed beads so we don't accidentally re-create a
// previously-resolved feedback.
func (c *Client) FindFeedbackByFingerprint(ctx context.Context, cycleID, fingerprint string) (*Feedback, error) {
	if fingerprint == "" {
		return nil, nil
	}
	all, err := c.ListFeedback(ctx, cycleID, true)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Fields.Fingerprint == fingerprint {
			return &all[i], nil
		}
	}
	return nil, nil
}

// encodeFeedbackMetadata serializes the non-zero fields of f as a JSON
// object that bd's --metadata flag accepts.
func encodeFeedbackMetadata(f FeedbackFields) (string, error) {
	m := map[string]any{}
	if f.Kind != "" {
		m["kind"] = f.Kind
	}
	if f.ExternalID != "" {
		m["external_id"] = f.ExternalID
	}
	if f.Fingerprint != "" {
		m["fingerprint"] = f.Fingerprint
	}
	if f.AuthorRole != "" {
		m["author_role"] = f.AuthorRole
	}
	if f.ReplyDraft != "" {
		m["reply_draft"] = f.ReplyDraft
	}
	if f.ResponseID != "" {
		m["response_id"] = f.ResponseID
	}
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode feedback metadata: %w", err)
	}
	return string(b), nil
}

func feedbackFieldsFromMetadata(m map[string]any) FeedbackFields {
	f := FeedbackFields{}
	for k, v := range m {
		switch k {
		case "kind":
			f.Kind = asString(v)
		case "external_id":
			f.ExternalID = asString(v)
		case "fingerprint":
			f.Fingerprint = asString(v)
		case "author_role":
			f.AuthorRole = asString(v)
		case "reply_draft":
			f.ReplyDraft = asString(v)
		case "response_id":
			f.ResponseID = asString(v)
		}
	}
	return f
}

// SetReplyDraft stores body on the feedback bead under the bd metadata key
// "reply_draft". The body is the text the LLM wants the sync engine to post
// to the upstream thread on its next pass.
//
// We use bd's `--set-metadata key=value` which merges into the existing
// metadata map (verified against bd CLI; the `--metadata <json>` flag also
// merges, but --set-metadata avoids round-tripping the full map).
func (c *Client) SetReplyDraft(ctx context.Context, id, body string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("feedback: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--set-metadata", "reply_draft="+body); err != nil {
		return fmt.Errorf("set reply_draft on %s: %w", id, err)
	}
	return nil
}

// GetReplyDraft reads the "reply_draft" metadata key from a feedback bead.
// Missing key returns ("", nil) — not an error — so callers can treat unset
// uniformly with empty.
func (c *Client) GetReplyDraft(ctx context.Context, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("feedback: id required")
	}
	fb, err := c.GetFeedback(ctx, id)
	if err != nil {
		return "", err
	}
	if fb == nil {
		return "", nil
	}
	return fb.Fields.ReplyDraft, nil
}

// SetResponseID stores the upstream response id on a feedback bead under
// the bd metadata key "response_id". Once set, the sync engine treats the
// reply as posted and will not re-post — this is the idempotency marker for
// the B3 reply pipeline.
func (c *Client) SetResponseID(ctx context.Context, id, responseID string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("feedback: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--set-metadata", "response_id="+responseID); err != nil {
		return fmt.Errorf("set response_id on %s: %w", id, err)
	}
	return nil
}

// GetResponseID reads the "response_id" metadata key from a feedback bead.
// Missing key returns ("", nil).
func (c *Client) GetResponseID(ctx context.Context, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("feedback: id required")
	}
	fb, err := c.GetFeedback(ctx, id)
	if err != nil {
		return "", err
	}
	if fb == nil {
		return "", nil
	}
	return fb.Fields.ResponseID, nil
}

// ListFeedbackPendingReply returns feedback beads where a reply_draft is
// queued (non-empty) AND no response_id has been recorded yet. Includes
// both open and closed feedback — a reply might be queued just before the
// LLM closes the bead, and the sync engine should still post it.
func (c *Client) ListFeedbackPendingReply(ctx context.Context) ([]Feedback, error) {
	all, err := c.ListFeedback(ctx, "" /* all cycles */, true /* includeClosed */)
	if err != nil {
		return nil, err
	}
	out := make([]Feedback, 0, len(all))
	for _, fb := range all {
		if fb.Fields.ReplyDraft == "" {
			continue
		}
		if fb.Fields.ResponseID != "" {
			continue
		}
		out = append(out, fb)
	}
	return out, nil
}

// Package-level convenience wrappers using the default Client.

// CreateFeedback creates a feedback bead using the default Client.
func CreateFeedback(ctx context.Context, in CreateFeedbackInput) (string, error) {
	return NewClient().CreateFeedback(ctx, in)
}

// CloseFeedback closes a feedback bead using the default Client.
func CloseFeedback(ctx context.Context, id, reason string) error {
	return NewClient().CloseFeedback(ctx, id, reason)
}

// MarkFeedbackResolvedUpstream closes a feedback bead with the
// "resolved-upstream" reason using the default Client.
func MarkFeedbackResolvedUpstream(ctx context.Context, id string) error {
	return NewClient().MarkFeedbackResolvedUpstream(ctx, id)
}
