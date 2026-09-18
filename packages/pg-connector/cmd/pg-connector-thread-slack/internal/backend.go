// backend.go: Backend implements pkg/provider/thread.Provider by exec'ing
// `claude -p` as pure transport to the Slack MCP already configured on
// this machine (this bead's own Objective/Binding decisions) — never
// deriving a field from the model's own judgment (the compute-only rule,
// [design: 8]). Every field this backend reports is either a plain fact
// the Slack MCP's own tool response carries, or (AsOf/Stale) computed
// deterministically in Go from this call's own completion time.
//
// The exact prompt wording (showPrompt/listPrompt below) is a freedom
// boundary this bead's own Contract explicitly leaves to the implementer,
// not independently verified against a live claude/Slack-MCP call (no
// Slack token is provisioned to this packet's implementer — see the "no
// Slack token" binding decision). Both prompts instruct the model to
// reply with ONLY one JSON object in a fixed shape carrying plain facts;
// this file validates that shape strictly and answers
// scriptout.ErrUnavailable on any mismatch [design: 8's acceptance
// criteria: "a malformed or schema-invalid reply yields unavailable"] —
// never a panic, never a best-effort partial schema.Thread.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/thread"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-thread-slack's concrete thread.Provider
// implementation.
type Backend struct {
	runner Runner
}

// New returns a Backend wrapping the given Runner. Production wiring
// passes NewCLIRunner(); tests inject a fake Runner.
func New(r Runner) *Backend {
	return &Backend{runner: r}
}

// Compile-time check that Backend satisfies the thread capability's
// Provider interface.
var _ thread.Provider = (*Backend)(nil)

// Deliberately NO `var _ provider.AuthChecker = (*Backend)(nil)`: this
// backend resolves no credential of its own at all (claude/the Slack MCP
// resolve their own ambient configuration; "no Slack token is
// provisioned" is this bead's own binding decision), so it has nothing an
// auth_status op could meaningfully check — mirroring how
// pg-connector-issue-beads (no external credential of its own either)
// also implements no AuthChecker.

// claudeEnvelope is the `claude -p --output-format json` result object —
// only the fields this package needs are decoded, mirroring
// packages/pg-ccaudit/internal/classify.envelope's identical convention.
type claudeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// decodeClaudeEnvelope decodes claude -p's own stdout (the outer
// `--output-format json` envelope, NOT this backend's own Thread shape —
// see decodeShowReply/decodeListReply for that inner decode).
func decodeClaudeEnvelope(raw string) (*claudeEnvelope, error) {
	var env claudeEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// slackThreadFields is the plain-fact field set both showPrompt and
// listPrompt instruct the model to report, one-to-one with
// schema.Thread's own field set (minus AsOf/Stale, which this backend
// computes itself — see toSchemaThread).
type slackThreadFields struct {
	ID           string   `json:"id"`
	Channel      string   `json:"channel"`
	Permalink    string   `json:"permalink"`
	StartedBy    string   `json:"started_by,omitempty"`
	Participants []string `json:"participants,omitempty"`
	LastReplyAt  string   `json:"last_reply_at,omitempty"`
	ReplyCount   int      `json:"reply_count"`
	Text         string   `json:"text"`
	MentionsMe   bool     `json:"mentions_me"`
}

// slackShowReply is showPrompt's own instructed reply shape: Found
// distinguishes "this id resolves to a real thread" from "it does not" —
// a well-formed {"found":false} answers scriptout.ErrNotFound (this
// backend's own reachable, well-formed outcome, distinct from a decode
// failure), while a missing/absent found key defaults to false (the safe
// reading: a model that forgets the key is treated as "not found," never
// as a fabricated found-with-empty-fields result).
type slackShowReply struct {
	Found bool `json:"found"`
	slackThreadFields
}

// decodeShowReply decodes showPrompt's own instructed JSON reply.
func decodeShowReply(raw string) (*slackShowReply, error) {
	var reply slackShowReply
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// slackThreadListReply is listPrompt's own instructed reply shape: a
// single JSON object carrying an items array, mirroring
// cmd/pg-connector-issue-jira/internal.pjiraSearchResult's identical
// "single object, not a bare array" convention (easier to distinguish
// "zero matches" from "malformed reply" than a bare top-level array
// would be).
type slackThreadListReply struct {
	Items []slackThreadFields `json:"items"`
}

// decodeListReply decodes listPrompt's own instructed JSON reply.
func decodeListReply(raw string) (*slackThreadListReply, error) {
	var reply slackThreadListReply
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// toSchemaThread maps a decoded slackThreadFields onto the thread
// capability's shared wire shape. asOf is this call's own completion time
// — every call site below execs claude -p fresh with no local cache of
// Slack's own facts, so Stale is always false, mirroring every other
// current pg-connector backend's identical AsOf/Stale precedent
// (cmd/pg-connector-issue-jira/internal.toSchemaIssue's own doc comment).
func toSchemaThread(f *slackThreadFields, asOf time.Time) *schema.Thread {
	return &schema.Thread{
		ID:           f.ID,
		Channel:      f.Channel,
		Permalink:    f.Permalink,
		StartedBy:    f.StartedBy,
		Participants: f.Participants,
		LastReplyAt:  f.LastReplyAt,
		ReplyCount:   f.ReplyCount,
		Text:         f.Text,
		MentionsMe:   f.MentionsMe,
		AsOf:         asOf.Format(time.RFC3339),
		Stale:        false,
	}
}

// showPrompt builds the claude -p prompt for Show(id) — carries the
// requested id and requests JSON [design: 4.5, 8, D23's "carries the
// configured search modifiers and the oldest cursor, requests JSON";
// Show has no search modifier of its own to carry, only id]. The model is
// explicitly told to report only plain facts, never a derived judgment
// (the compute-only rule) and to answer {"found":false} rather than
// guessing when id does not resolve.
func showPrompt(id string) string {
	return fmt.Sprintf(`Using the Slack MCP tools already available to you, look up the Slack thread identified by %q (a Slack message/thread timestamp, permalink, or equivalent identifier this MCP's own tools accept).

Respond with ONLY one JSON object, no prose, no markdown code fence, matching exactly this shape:
{"found": <bool>, "id": <string>, "channel": <string>, "permalink": <string>, "started_by": <string>, "participants": [<string>, ...], "last_reply_at": <string>, "reply_count": <int>, "text": <string>, "mentions_me": <bool>}

If %q does not resolve to a real thread, respond with exactly {"found": false}.

Report every field as a plain fact taken directly from the Slack MCP tool's own response. Never infer, summarize, paraphrase, or guess a value — if the MCP's response does not carry a field, omit it (except "found" and "id"/"channel"/"permalink", which are always required when found is true).`, id, id)
}

// listPrompt builds the claude -p prompt for List's one search-modifier
// expression — carries the configured search modifier and instructs a
// full, from-the-oldest search rather than an incremental one [design:
// 4.5, 8, D23's "carries the configured search modifiers and the oldest
// cursor, requests JSON": this capability has no incremental-listing
// backend at this phase (schema.ThreadListResult.Cursor is always nil,
// mirroring pkg/provider/issue.Provider.List's own no-cursor shape), so
// "the oldest cursor" is realized here as an explicit instruction to
// always return the complete current match set starting from the oldest
// matching thread, never a delta against some remembered position].
func listPrompt(modifier string) string {
	return fmt.Sprintf(`Using the Slack MCP tools already available to you, search Slack threads matching this modifier: %q.

Search from the oldest matching thread forward, and return every currently-matching thread — this backend keeps no incremental cursor between calls, so always answer with the full current match set for this modifier, never a delta since some earlier call.

Respond with ONLY one JSON object, no prose, no markdown code fence, matching exactly this shape:
{"items": [{"id": <string>, "channel": <string>, "permalink": <string>, "started_by": <string>, "participants": [<string>, ...], "last_reply_at": <string>, "reply_count": <int>, "text": <string>, "mentions_me": <bool>}, ...]}

If nothing matches, respond with exactly {"items": []}.

Report every field of every item as a plain fact taken directly from the Slack MCP tool's own response for that thread. Never infer, summarize, paraphrase, or guess a value.`, modifier)
}

// runClaude execs prompt via b.runner and decodes the outer
// `--output-format json` envelope, folding a transport-level Run failure
// and an envelope-reported is_error:true into the same ErrUnavailable
// outcome (both mean "the claude -p/MCP call itself did not produce a
// usable reply," distinct from this backend's OWN reply-shape validation,
// which each caller does itself against the envelope's Result field).
func (b *Backend) runClaude(ctx context.Context, prompt string) (string, error) {
	out, err := b.runner.Run(ctx, prompt)
	if err != nil {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "claude -p: "+err.Error())
	}
	env, decodeErr := decodeClaudeEnvelope(out)
	if decodeErr != nil {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "claude -p: decode envelope: "+decodeErr.Error())
	}
	if env.IsError {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "claude -p reported is_error: "+env.Result)
	}
	return env.Result, nil
}

// Show implements thread.Provider.Show via claude -p (see this file's own
// package doc comment).
func (b *Backend) Show(ctx context.Context, id string) (*schema.Thread, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "thread: id required")
	}
	result, err := b.runClaude(ctx, showPrompt(id))
	if err != nil {
		return nil, err
	}
	reply, decodeErr := decodeShowReply(result)
	if decodeErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "claude -p: decode thread reply: "+decodeErr.Error())
	}
	if !reply.Found {
		return nil, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("thread %q not found", id))
	}
	if reply.ID == "" || reply.Channel == "" || reply.Permalink == "" {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "claude -p: thread reply missing one of id/channel/permalink")
	}
	return toSchemaThread(&reply.slackThreadFields, time.Now().UTC()), nil
}

// List implements thread.Provider.List via claude -p (see this file's own
// package doc comment). Every reply unconditionally carries
// Truncated:true [design: 4.5, 8, D23] — this backend has no reliable way
// to confirm the Slack MCP returned the complete match set, so it never
// computes a conditional value the way
// cmd/pg-connector-issue-jira/internal.Backend.List does. Cursor is
// always nil (no incremental-listing backend at this phase, mirroring
// issue.Provider.List's own no-cursor shape). Each expression's matches
// are unioned, deduplicated by thread id (design's "run each, union
// results deduplicated by id" rule) — an item with an empty id is
// silently skipped rather than failing the whole call, mirroring
// cmd/pg-connector-issue-jira/internal.Backend.List's identical
// leniency for a malformed individual search-result element.
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error) {
	seen := make(map[string]bool)
	entities := make([]schema.Thread, 0)
	for _, modifier := range query {
		modifier = strings.TrimSpace(modifier)
		if modifier == "" {
			continue
		}
		result, err := b.runClaude(ctx, listPrompt(modifier))
		if err != nil {
			return nil, err
		}
		reply, decodeErr := decodeListReply(result)
		if decodeErr != nil {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "claude -p: decode thread list reply: "+decodeErr.Error())
		}
		asOf := time.Now().UTC()
		for i := range reply.Items {
			item := reply.Items[i]
			if item.ID == "" || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			entities = append(entities, *toSchemaThread(&item, asOf))
		}
	}
	ids := make([]string, 0, len(entities))
	for _, e := range entities {
		ids = append(ids, e.ID)
	}
	res := &schema.ThreadListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: true}
	if idsOnly {
		res.Entities = nil
	}
	return res, nil
}
