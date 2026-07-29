// Package reviewinput defines and decodes the JSON review payload accepted by
// `pg-pr review draft` and `pg-pr review submit`.
//
// # Authority
//
// This package is the SINGLE AUTHORITATIVE definition of that schema (bead
// pg2-cns7a). Three consumers MUST agree with it and nothing else:
//
//   - the pg-pr reviewer agent assets
//     (`claude-marketplace/pg-pr/agents/pg-pr-review-*.md`), which emit it;
//   - `pg-pr review --help`, which renders [SchemaDoc] verbatim (the
//     orchestrator asset and the pr-pool review role both tell their agent to
//     "see pg-pr review --help", so that text is load-bearing, not decoration);
//   - [reviewstage.Draft], the domain type Decode adapts the payload into.
//
// # Why an adapter and not a shared struct
//
// Adapter pattern (agent-facing DTO → domain model). [api.Comment]'s
// `body`/`path`/`line` mirrors GitHub's review-comment API, which is the right
// shape at the VCS boundary, and `severity` — which agents genuinely reason in —
// has no target field there. So a translation step must exist regardless of what
// the assets emit; putting it here (rather than only fixing the assets) also
// makes the verb loud for every OTHER caller that composes this JSON by hand:
// the pr-pool review role, the daemon spawn path, and humans.
//
// # Fail loud, never drop
//
// Decode is a STRICT boundary: unknown keys are rejected with an error naming
// the key, and a comment that maps to an empty body is rejected. The original
// defect (pg2-cns7a) was invisible precisely because encoding/json silently
// discards unknown fields: every agent finding decoded to
// `{Path: "…", Line: 0, Body: ""}` and the review posted blank.
package reviewinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// Severity values accepted on a comment. Rendered as a bold prefix on the
// comment body (see toAPIComment) because [api.Comment] has no severity
// field; the `**<severity>**:` rendering is also the form the feedback-store
// ingestion classifier recognises.
const (
	SeverityError      = "error"
	SeverityWarning    = "warning"
	SeveritySuggestion = "suggestion"
)

// ExampleJSON is a complete, VALID review payload. It is the example rendered
// into `pg-pr review --help` and it is decoded by the package's tests, so the
// documented example cannot drift from the accepted schema.
const ExampleJSON = `{
  "body": "Reviewed 3 files. Two correctness issues and one nit; see inline comments.",
  "head_sha": "9f1c0b2e5d4a37c8b6e9f0a1d2c3b4a5e6f70819",
  "comments": [
    {
      "path": "packages/pg-pr/cmd/pg-pr/review.go",
      "line": 42,
      "severity": "error",
      "body": "readDraftInput ignores the decode error, so a malformed payload stages an empty review."
    },
    {
      "path": null,
      "line": null,
      "severity": "suggestion",
      "body": "The three commits could be squashed; commits 2 and 3 only fix commit 1."
    }
  ],
  "warnings": ["pg-pr-review-jira-alignment: JIRA access unavailable; alignment not verified."]
}`

// SchemaDoc is the human/agent-facing schema reference. `pg-pr review --help`
// renders it verbatim (acceptance criterion 4 of pg2-cns7a).
var SchemaDoc = `Review payload (read from stdin, or from --from-file) — JSON object.

TOP-LEVEL KEYS
  body       string    optional  Overall review summary, markdown. Posted as the
                                 review body. When omitted and "warnings" is
                                 non-empty, the warnings block becomes the body
                                 so a failed reviewer is never silently lost.
  comments   array     optional  The findings. See COMMENT KEYS.
  warnings   [string]  optional  Reviewer subagents that did NOT complete.
                                 Rendered into the review body; never dropped.
  head_sha   string    optional  Commit the review was produced against. Sent as
                                 commit_id so inline comments do not 422 when
                                 the PR head has advanced.
  ownership  string    optional  "mine" | "team" (advisory provenance).
  bead_id    string    optional  The draft-review bead this satisfies.
  verdict    string    optional  approve | request-changes | comment (advisory).

COMMENT KEYS
  path       string|null  File path relative to the repo root. null or omitted
                          means a PR-level comment.
  line       int|null     1-based line in the NEW file. null or omitted means a
                          file-level (or, with a null path, PR-level) comment.
                          Such comments are folded into the review body, because
                          GitHub's review-comments API requires path AND line.
  body       string       REQUIRED, non-empty. The finding text. Do NOT include
                          the robot marker — pg-pr stamps it on post.
  severity   string       optional  error | warning | suggestion. Rendered as a
                          "**<severity>**: " prefix on the body.

  Deprecated aliases, accepted and mapped: "message" -> body, "lines": [N] -> line.

ANY OTHER KEY IS REJECTED with a non-zero exit naming the key. pg-pr never
silently discards review content: an unmappable key, an empty body, or a
"lines" array with more than one entry is an error, not a blank comment.

EXAMPLE
` + indentBlock(ExampleJSON, "  ")

// indentBlock prefixes every line of s with indent.
func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = indent + l
	}
	return strings.Join(lines, "\n")
}

// Payload is the wire shape accepted from stdin/--from-file. Every field the
// decoder tolerates is declared here: json.Decoder.DisallowUnknownFields turns
// anything else into an error, so this struct IS the accepted key set.
type Payload struct {
	// Repo and PR are ignored on input — the verb sets them from its
	// arguments. They are accepted (not rejected) so a staged draft file,
	// which carries them, can be hand-edited and re-fed via --from-file.
	Repo *string `json:"repo"`
	PR   *int    `json:"pr"`

	Body     string    `json:"body"`
	Warnings []string  `json:"warnings"`
	Comments []Comment `json:"comments"`

	Ownership string `json:"ownership"`
	HeadSHA   string `json:"head_sha"`
	BeadID    string `json:"bead_id"`
	Verdict   string `json:"verdict"`
}

// Comment is the wire shape of one finding.
//
// Path/Line/Lines/Resolved are pointers or slices so an explicit JSON null (the
// PR-structure and JIRA-alignment agents emit `"path": null, "lines": null`) is
// distinguishable from a zero value and from an absent key.
type Comment struct {
	Path     *string `json:"path"`
	Line     *int    `json:"line"`
	Body     string  `json:"body"`
	Severity string  `json:"severity"`

	// Deprecated aliases. Kept accepted-and-mapped rather than rejected so an
	// already-deployed agent instance emitting the older documented shape
	// still produces a usable review instead of failing the whole run — the
	// tolerance that makes the verb robust to asset drift.
	Message string `json:"message"`
	Lines   []int  `json:"lines"`

	// Echo-only fields of api.Comment. reviewstage.Save marshals api.Comment,
	// which writes these four unconditionally, so a staged draft file carries
	// them. Accepted and ignored (they describe an EXISTING upstream comment
	// and are meaningless as review input) so the hand-edit-and-re-feed flow
	// works; never posted.
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorRole string `json:"author_role"`
	Resolved   *bool  `json:"resolved"`
}

// acceptedKeysHint is appended to an unsupported-key error so an agent that
// composed the payload itself can self-correct on retry without another round
// trip through `--help`.
const acceptedKeysHint = `accepted top-level keys: body, comments, warnings, head_sha, ownership, bead_id, verdict; ` +
	`accepted comment keys: path, line, body, severity (deprecated aliases: message, lines). ` +
	`Run 'pg-pr review --help' for the full schema`

// unknownFieldRe extracts the offending key from encoding/json's
// DisallowUnknownFields error ("json: unknown field \"x\"").
var unknownFieldRe = regexp.MustCompile(`unknown field "([^"]*)"`)

// Decode parses a review payload and adapts it to a [reviewstage.Draft].
//
// Repo and PR are left unset: the calling verb owns them (it resolved them from
// its flags/arguments) and MUST overwrite whatever the payload claimed.
func Decode(raw []byte) (*reviewstage.Draft, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var p Payload
	if err := dec.Decode(&p); err != nil {
		if m := unknownFieldRe.FindStringSubmatch(err.Error()); m != nil {
			return nil, fmt.Errorf("review payload: unsupported key %q; %s", m[1], acceptedKeysHint)
		}
		return nil, fmt.Errorf("parse review JSON: %w", err)
	}
	// A second top-level value means two payloads were concatenated (e.g. two
	// subagent outputs cat'd together). Taking the first and dropping the rest
	// is the same silent-loss failure this package exists to prevent.
	if dec.More() {
		return nil, errors.New("review payload: trailing JSON after the review object; " +
			"send exactly one object with all findings in its \"comments\" array")
	}
	return p.ToDraft()
}

// ToDraft validates the payload and maps it onto the domain type.
func (p *Payload) ToDraft() (*reviewstage.Draft, error) {
	d := &reviewstage.Draft{
		Body:      p.Body,
		Ownership: p.Ownership,
		HeadSHA:   p.HeadSHA,
		BeadID:    p.BeadID,
		Verdict:   p.Verdict,
	}
	for i := range p.Comments {
		c, err := p.Comments[i].toAPIComment()
		if err != nil {
			return nil, fmt.Errorf("review payload: comments[%d]: %w", i, err)
		}
		d.Comments = append(d.Comments, c)
	}
	if block := renderWarnings(p.Warnings); block != "" {
		if d.Body == "" {
			d.Body = block
		} else {
			d.Body += "\n\n" + block
		}
	}
	return d, nil
}

// renderWarnings turns the warnings list into a markdown block for the review
// body. Returns "" when there is nothing to say, so a findings-free review keeps
// an empty body (the provider treats a body-less, comment-less review as a
// no-op, and reviewstage feeds Body to the self-review feedback store as a
// PR-level finding — neither should see a synthetic summary).
func renderWarnings(warnings []string) string {
	var b strings.Builder
	for _, w := range warnings {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("**Review warnings** — a reviewer did not complete, so this review may be incomplete:\n")
		}
		b.WriteString("\n- " + w)
	}
	return b.String()
}

// toAPIComment maps one wire comment onto an api.Comment, folding severity into
// the body. Every rejection path here is a payload the old decoder accepted and
// silently blanked.
func (c *Comment) toAPIComment() (api.Comment, error) {
	body, err := c.resolveBody()
	if err != nil {
		return api.Comment{}, err
	}
	line, err := c.resolveLine()
	if err != nil {
		return api.Comment{}, err
	}
	severity, err := normalizeSeverity(c.Severity)
	if err != nil {
		return api.Comment{}, err
	}
	if severity != "" {
		body = "**" + severity + "**: " + body
	}
	path := ""
	if c.Path != nil {
		path = *c.Path
	}
	return api.Comment{Path: path, Line: line, Body: body}, nil
}

// resolveBody picks the comment text from "body" or its "message" alias.
func (c *Comment) resolveBody() (string, error) {
	body, message := strings.TrimSpace(c.Body), strings.TrimSpace(c.Message)
	switch {
	case body != "" && message != "" && body != message:
		return "", errors.New(`both "body" and "message" are set and differ; ` +
			`"message" is a deprecated alias for "body" — set exactly one`)
	case body != "":
		return body, nil
	case message != "":
		return message, nil
	default:
		return "", errors.New(`empty comment text; set "body" to the finding ` +
			`(a comment with no body would post as a blank review comment)`)
	}
}

// resolveLine picks the anchor line from "line" or its "lines" alias. 0 means
// "not anchored to a diff line" — a file-level or PR-level comment.
func (c *Comment) resolveLine() (int, error) {
	var fromLines *int
	switch {
	case len(c.Lines) > 1:
		return 0, fmt.Errorf(`"lines" carries %d entries; pg-pr anchors one comment to one line — `+
			`emit one comment per line, or use "line"`, len(c.Lines))
	case len(c.Lines) == 1:
		fromLines = &c.Lines[0]
	}
	line := 0
	switch {
	case c.Line != nil && fromLines != nil && *c.Line != *fromLines:
		return 0, fmt.Errorf(`"line" (%d) and its alias "lines" (%d) disagree; set exactly one`, *c.Line, *fromLines)
	case c.Line != nil:
		line = *c.Line
	case fromLines != nil:
		line = *fromLines
	}
	if line < 0 {
		return 0, fmt.Errorf(`"line" must be >= 1, or null/omitted for a file- or PR-level comment; got %d`, line)
	}
	return line, nil
}

// normalizeSeverity lower-cases and validates severity. An unrecognised value is
// an error rather than a verbatim prefix: it would otherwise render as a bogus
// "**blocker**: " label and mis-classify at feedback-store ingestion.
func normalizeSeverity(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return "", nil
	case SeverityError, SeverityWarning, SeveritySuggestion:
		return v, nil
	default:
		return "", fmt.Errorf(`unknown "severity" %q; accepted: %s, %s, %s (or omit the key)`,
			s, SeverityError, SeverityWarning, SeveritySuggestion)
	}
}
