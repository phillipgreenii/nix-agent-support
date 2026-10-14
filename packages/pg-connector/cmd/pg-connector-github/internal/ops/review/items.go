package review

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// submitItem is one distinct comment of a request, normalized.
type submitItem struct {
	fp       string
	reply    bool
	threadID string
	path     string
	line     int
	side     string // "LEFT" or "RIGHT"; points only
	body     string // normalized request text, without marker or attribution
}

// buildSubmitItems validates a request's comments and merges items that are
// identical after normalization into one.
func buildSubmitItems(comments []pr.ReviewComment) ([]submitItem, error) {
	if len(comments) > maxRequestComments {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: %d comments exceed the limit of %d per request", len(comments), maxRequestComments))
	}
	seen := map[string]bool{}
	out := make([]submitItem, 0, len(comments))
	for i, c := range comments {
		var it submitItem
		if thread := strings.TrimSpace(c.ThreadID); thread != "" {
			if c.Path != "" || c.Line != 0 {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d has a thread_id together with path or line; a reply carries only thread_id and body", i))
			}
			it = submitItem{
				reply: true, threadID: thread, body: pgposted.NormalizeBody(c.Body),
				fp: pgposted.ReplyFingerprint(thread, c.Body),
			}
		} else {
			if c.Path == "" || c.Line <= 0 {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d needs a path and a positive line (or a thread_id to reply)", i))
			}
			side := strings.ToUpper(strings.TrimSpace(c.Side))
			if side == "" {
				side = "RIGHT"
			}
			if side != "LEFT" && side != "RIGHT" {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d has unsupported side %q (want LEFT or RIGHT)", i, c.Side))
			}
			it = submitItem{
				path: c.Path, line: c.Line, side: side, body: pgposted.NormalizeBody(c.Body),
				fp: pgposted.PointFingerprint(c.Path, side, c.Line, c.Body),
			}
		}
		if seen[it.fp] {
			continue
		}
		seen[it.fp] = true
		out = append(out, it)
	}
	return out, nil
}

// hostFingerprints returns every comment fingerprint marker found on comments
// the acting identity authored on the PR, in its pending reviews and its
// submitted reviews.
func hostFingerprints(data *github.PendingReviewData) map[string]bool {
	out := map[string]bool{}
	add := func(cs []github.PendingReviewComment) {
		for _, c := range cs {
			for _, fp := range pgposted.ExtractFingerprints(c.Body) {
				out[fp] = true
			}
		}
	}
	for _, r := range data.Reviews {
		add(r.Comments)
	}
	for _, r := range data.Submitted {
		add(r.Comments)
	}
	return out
}

// reviewByID finds a pending review of data by its node id.
func reviewByID(data *github.PendingReviewData, id string) *github.PendingReviewNode {
	for i := range data.Reviews {
		if data.Reviews[i].ID == id {
			return &data.Reviews[i]
		}
	}
	return nil
}

// buildSection renders the body section for head.
func buildSection(head, text string) string {
	return pgposted.SectionOpen(head) + "\n" + text + "\n" + pgposted.SectionClose
}

// appendSection appends section to a review body, leaving the existing text
// untouched.
func appendSection(existing, section string) string {
	if strings.TrimSpace(existing) == "" {
		return section
	}
	return existing + "\n\n" + section
}

// bodyHeadRecorded reports whether the sidecar records a body section as
// written for head. Heads match on their section key, as in the body.
func bodyHeadRecorded(st pgposted.State, head string) bool {
	open := pgposted.SectionOpen(head)
	for _, h := range st.BodyHeads {
		if pgposted.SectionOpen(h) == open {
			return true
		}
	}
	return false
}

// attribution is the visible line every written comment ends with.
func attribution(head string) string {
	return "*Posted by pg-connector at " + short7(head) + ".*"
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// savedAtLine is the plain line a review saved at an earlier head carries in
// its body, so an operator submitting it can tell a mix of comments anchored
// at different commits apart (INV-REVHEAD-2).
func savedAtLine(saved, live string) string {
	return fmt.Sprintf("Saved at %s; PR head was %s when saved.", short7(saved), short7(live))
}

// lastAppendOf renders the sidecar's last append for the result, nil for none.
func lastAppendOf(st pgposted.State) *pr.LastAppend {
	if st.LastAppend == nil {
		return nil
	}
	return &pr.LastAppend{At: st.LastAppend.At, Added: st.LastAppend.Added, Head: st.LastAppend.Head}
}

// submitFailure is one comment that did not land.
type submitFailure struct {
	reason github.WriteFailReason
	item   submitItem
}

// failedError builds the error for a run in which some comments did not land.
// The error envelope carries only code and message, so the message has a
// stable shape:
//
//	<n> of <m> comments landed; failed: <reason>:<fingerprint>[:<path>:<line>] ...
//
// It lists at most maxFailuresListed failures and ends with "(+<k> more)" when
// there were more. The code is unavailable when any failure could succeed on
// retry (rate_limited, unconfirmed) and invalid_argument when every failure is
// permanent (anchor_rejected, thread_not_found).
func failedError(landed, total int, failures []submitFailure) error {
	listed := failures
	if len(listed) > maxFailuresListed {
		listed = listed[:maxFailuresListed]
	}
	parts := make([]string, 0, len(listed))
	retryable := false
	for _, f := range failures {
		if f.reason == github.ReasonRateLimited || f.reason == github.ReasonUnconfirmed {
			retryable = true
		}
	}
	for _, f := range listed {
		p := string(f.reason) + ":" + f.item.fp
		if !f.item.reply {
			p += fmt.Sprintf(":%s:%d", f.item.path, f.item.line)
		}
		parts = append(parts, p)
	}
	msg := fmt.Sprintf("%d of %d comments landed; failed: %s", landed, total, strings.Join(parts, " "))
	if extra := len(failures) - len(listed); extra > 0 {
		msg += fmt.Sprintf(" (+%d more)", extra)
	}
	if retryable {
		return scriptout.WrapError(scriptout.ErrUnavailable, msg)
	}
	return scriptout.WrapError(scriptout.ErrInvalidArgument, msg)
}

// wrapAfterWrite notes on err that a write already reached the host, so a
// caller knows the identical request converges.
func wrapAfterWrite(err error, what string) error {
	return fmt.Errorf("%w; %s, so retry the identical request", err, what)
}
