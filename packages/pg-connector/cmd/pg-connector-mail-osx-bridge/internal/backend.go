// backend.go: Backend implements pkg/provider/mail.Provider by talking ONLY
// to pg-osx-bridge-api's local Unix-domain socket mail service via a
// Transport (client.go) — no os/exec import and no osascript invocation
// anywhere in this package (that lives inside the bridge daemon; a test in
// this package pins both, along with the absence of any delete-shaped op,
// INV-MAIL-1). It additionally implements the OPTIONAL cross-cutting
// attention.Provider (asserted via type-check on Backend) and
// search.Provider (served by the searchAdapter view returned by
// Backend.SearchProvider, because mail.Provider's own Search(ctx, query,
// mailbox, limit) and search.Provider's Search(ctx, query, fields) cannot
// share one Go type: same method name, different signatures). It implements NO
// pkg/provider.AuthChecker: pg-osx-bridge-api requires no credential from
// its own client — the Automation grant for Mail.app is the daemon's own
// concern.
//
// This backend reads no config file of its own: like every other Tier-2
// backend it receives its own opaque config block per request, as raw JSON,
// via scriptout.ConfigFromContext — see decodeBackendConfig below. The
// attachment destination directory is NOT in this config: it is the
// bridge-side service's configuration, and the bridge's fetch-attachment
// result carries the full saved path (operator ruling, Phillip, 2026-10-05).
//
// Binding decisions this file makes explicit (freedom boundaries the
// design leaves open):
//
//   - Mailbox scope: with no mailbox argument, List and Search query EACH
//     configured mailbox (config.mailboxes, in order) with one bridge call
//     apiece, because the bridge's own empty mailbox means "the inbox"
//     (list) or "every mailbox" (search), never "the configured set". A
//     backend with NO configured mailboxes therefore answers an empty
//     result, exactly like the calendar backend with no calendars. A
//     non-empty mailbox argument pins to exactly that mailbox, configured
//     or not; the bridge answering not_found for that explicitly named
//     mailbox is passed through as not_found.
//
//   - A CONFIGURED mailbox that does not exist in Mail.app (the bridge
//     answers not_found for it) is SKIPPED: it MUST NOT fail the whole
//     call, and the other configured mailboxes' messages are still served
//     (CalendarListResult-style silent best-effort skip; the reply carries
//     no field for a partial failure).
//
//   - Duplicates: the same Message-ID filed under several configured
//     mailboxes (Gmail-label mailboxes) is merged to ONE entry — Message-ID
//     is the identity. The occurrence under the EARLIEST-configured mailbox
//     wins, so that entry's Mailbox and MailboxPriority are that mailbox's.
//
//   - Ordering and limit: the merged result is newest first
//     (DateReceived descending). limit 0 means defaultListLimit; a limit
//     above the bridge's own cap is clamped to maxListLimit. Truncated is
//     true when any mailbox's bridge reply filled its per-call limit (more
//     messages may exist) or the merge itself cut entries to honor the
//     limit; otherwise false, because the backend then holds the complete
//     match set for the configured mailboxes.
//
//   - MailboxPriority: stamped from the matching configured mailbox's
//     optional priority, matched by exact mailbox name; empty when the
//     mailbox is unconfigured or has no priority (the expected steady state
//     until tiers are decided). It is a pass-through tag only, never an
//     attention input (INV-MAIL-3).
//
//   - important_people is accepted in the config (a shared value defined
//     once in nix) but NOTHING in this backend consumes it: mail attention
//     is not derived from it (INV-MAIL-3, INV-MAIL-4).
//
//   - Attention: ListAttention answers an EMPTY list. Mail attention is
//     time-only for now and an email has no time-driven signal, while the
//     operator ruled that email attention is decided outside pg-connector
//     (INV-MAIL-3, INV-MAIL-4). The optional attention capability is still
//     advertised so a deployment MAY register this backend under
//     attention.sources without an unknown-op failure.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/mail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// searchSourceName is this backend's own SearchResult.Source value
// (mirrors the calendar backend's "<this binary's own name>" convention).
const searchSourceName = "pg-connector-mail-osx-bridge"

// searchResultType is the SearchResult.Type this backend reports.
const searchResultType = "mail_message"

// List limits. The bridge rejects a limit above its own cap
// (mailapi.MaxLimit) with invalid_argument and applies mailapi.DefaultLimit
// to 0; this backend mirrors both by value so it can reason about
// truncation.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Backend is pg-connector-mail-osx-bridge's concrete mail.Provider
// implementation.
type Backend struct {
	transport Transport
	// now is the clock; tests inject a fixed one. Production uses time.Now.
	now func() time.Time
}

// New returns a Backend wrapping the given Transport. Production wiring
// passes NewSocketClient(); tests inject a stub.
func New(t Transport) *Backend {
	return &Backend{transport: t, now: time.Now}
}

// Compile-time check that Backend satisfies the mail capability's Provider
// interface.
var _ mail.Provider = (*Backend)(nil)

// Compile-time check that Backend also satisfies the attention
// capability's own Provider interface.
var _ attention.Provider = (*Backend)(nil)

// Deliberately NO `var _ provider.AuthChecker = (*Backend)(nil)`: this
// backend resolves no credential of its own at all (see this file's
// package doc comment).

// mailboxConfig is one element of this backend's own "mailboxes" config
// key. Priority is OPTIONAL: left unset until mailbox tiers are decided.
type mailboxConfig struct {
	Name     string `json:"name"`
	Priority string `json:"priority,omitempty"`
}

// backendConfig is the {"mailboxes": [...], "important_people": [...]}
// shape this backend decodes from its own opaque per-backend config block
// on every call, via scriptout.ConfigFromContext — never a file read, never
// an env var, never a package-level cache.
type backendConfig struct {
	Mailboxes []mailboxConfig `json:"mailboxes,omitempty"`
	// ImportantPeople is accepted but unused (see this file's package doc
	// comment; INV-MAIL-3).
	ImportantPeople []string `json:"important_people,omitempty"`
}

// decodeBackendConfig decodes raw (scriptout.ConfigFromContext's return
// value) into a backendConfig. An empty/nil raw (no config block at all)
// decodes to the zero value rather than an error.
func decodeBackendConfig(raw json.RawMessage) (backendConfig, error) {
	var cfg backendConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := scriptout.Decode(raw, &cfg); err != nil {
		return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "mail: decode config: "+err.Error())
	}
	return cfg, nil
}

// priorityFor returns the configured priority of the mailbox named name
// (exact match), or "" when it is unconfigured or has no priority.
func (cfg backendConfig) priorityFor(name string) string {
	for _, mb := range cfg.Mailboxes {
		if mb.Name == name {
			return mb.Priority
		}
	}
	return ""
}

// scope returns the mailbox names a List/Search call queries: exactly
// pinned when non-empty, else every configured mailbox in order. skipMissing
// reports whether a not_found answer for a mailbox is skipped (configured
// scope) rather than passed through (explicitly pinned).
func (cfg backendConfig) scope(pinned string) (names []string, skipMissing bool) {
	if pinned != "" {
		return []string{pinned}, false
	}
	names = make([]string, 0, len(cfg.Mailboxes))
	for _, mb := range cfg.Mailboxes {
		names = append(names, mb.Name)
	}
	return names, true
}

// effectiveLimit resolves a caller limit (>= 0, validated by dispatch.go)
// to the per-bridge-call limit.
func effectiveLimit(limit int) int {
	switch {
	case limit == 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

// toSchemaMessage maps one bridge message onto the mail capability's shared
// wire shape. asOf is this call's own completion time — every call fetches
// fresh via the socket with no local cache, so Stale is always false.
func toSchemaMessage(m apiMessage, priority, asOf string) schema.MailMessage {
	var atts []schema.MailAttachment
	if len(m.Attachments) > 0 {
		atts = make([]schema.MailAttachment, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			atts = append(atts, schema.MailAttachment{
				ID:       a.ID,
				Filename: a.Name,
				MimeType: a.MimeType, // unchanged from the wire; empty when the bridge sends none
				Size:     a.Size,
			})
		}
	}
	return schema.MailMessage{
		ID:              m.ID,
		Subject:         m.Subject,
		Sender:          m.Sender,
		DateReceived:    m.DateReceived.Format(time.RFC3339),
		Read:            m.Read,
		Flagged:         m.Flagged,
		Mailbox:         m.Mailbox,
		Attachments:     atts,
		MailboxPriority: priority,
		AsOf:            asOf,
		Stale:           false,
	}
}

// gather runs fetch once per mailbox in scope, skipping (when skipMissing)
// a mailbox the bridge answers not_found for, merges the replies into one
// de-duplicated (by Message-ID, earliest-configured mailbox wins), newest
// first list capped at limit, and reports whether the reply may be
// incomplete (see this file's package doc comment on Truncated).
func (b *Backend) gather(ctx context.Context, cfg backendConfig, pinned string, limit int, fetch func(mailbox string, limit int) ([]apiMessage, error)) (msgs []apiMessage, truncated bool, err error) {
	names, skipMissing := cfg.scope(pinned)
	eff := effectiveLimit(limit)

	seen := make(map[string]struct{})
	for _, name := range names {
		got, ferr := fetch(name, eff)
		if ferr != nil {
			if skipMissing && errors.Is(ferr, scriptout.ErrNotFound) {
				continue
			}
			return nil, false, ferr
		}
		if len(got) >= eff {
			truncated = true // this mailbox may hold more than it returned
		}
		for _, m := range got {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			msgs = append(msgs, m)
		}
	}

	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].DateReceived.After(msgs[j].DateReceived) })
	if len(msgs) > eff {
		msgs = msgs[:eff]
		truncated = true
	}
	return msgs, truncated, nil
}

// listResult translates gathered bridge messages to a MailListResult.
func (b *Backend) listResult(cfg backendConfig, msgs []apiMessage, truncated, idsOnly bool) *schema.MailListResult {
	asOf := b.now().UTC().Format(time.RFC3339)
	entities := make([]schema.MailMessage, 0, len(msgs))
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		entities = append(entities, toSchemaMessage(m, cfg.priorityFor(m.Mailbox), asOf))
		ids = append(ids, m.ID)
	}
	res := &schema.MailListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: truncated}
	if idsOnly {
		res.Entities = nil
	}
	return res
}

// List implements mail.Provider.List.
func (b *Backend) List(ctx context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	msgs, truncated, err := b.gather(ctx, cfg, mailbox, limit, func(name string, eff int) ([]apiMessage, error) {
		return b.transport.List(ctx, apiListQuery{Mailbox: name, UnreadOnly: unreadOnly, Limit: eff})
	})
	if err != nil {
		return nil, err
	}
	return b.listResult(cfg, msgs, truncated, idsOnly), nil
}

// Search implements mail.Provider.Search (the dedicated search_messages op).
func (b *Backend) Search(ctx context.Context, query, mailbox string, limit int) (*schema.MailListResult, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	msgs, truncated, err := b.searchMessages(ctx, cfg, query, mailbox, limit)
	if err != nil {
		return nil, err
	}
	return b.listResult(cfg, msgs, truncated, false), nil
}

// searchMessages is the shared core behind Search and searchAdapter.Search.
func (b *Backend) searchMessages(ctx context.Context, cfg backendConfig, query, mailbox string, limit int) ([]apiMessage, bool, error) {
	return b.gather(ctx, cfg, mailbox, limit, func(name string, eff int) ([]apiMessage, error) {
		return b.transport.Search(ctx, apiSearchQuery{Query: query, Mailbox: name, Limit: eff})
	})
}

// Show implements mail.Provider.Show. An unrecognized id is the bridge's
// not_found, passed through.
func (b *Backend) Show(ctx context.Context, id string) (*schema.MailMessage, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	d, err := b.transport.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	m := toSchemaMessage(d.apiMessage, cfg.priorityFor(d.Mailbox), b.now().UTC().Format(time.RFC3339))
	m.Body = d.Body
	return &m, nil
}

// MarkRead implements mail.Provider.MarkRead.
func (b *Backend) MarkRead(ctx context.Context, id string) error {
	return b.transport.SetRead(ctx, id, true)
}

// MarkUnread implements mail.Provider.MarkUnread.
func (b *Backend) MarkUnread(ctx context.Context, id string) error {
	return b.transport.SetRead(ctx, id, false)
}

// Archive implements mail.Provider.Archive.
func (b *Backend) Archive(ctx context.Context, id string) error {
	return b.transport.Archive(ctx, id)
}

// Unarchive implements mail.Provider.Unarchive.
func (b *Backend) Unarchive(ctx context.Context, id string) error {
	return b.transport.Unarchive(ctx, id)
}

// FetchAttachment implements mail.Provider.FetchAttachment. The returned
// Path is the bridge's full saved path, passed through opaque.
func (b *Backend) FetchAttachment(ctx context.Context, messageID, attachmentID string) (*schema.MailAttachmentFile, error) {
	f, err := b.transport.FetchAttachment(ctx, messageID, attachmentID)
	if err != nil {
		return nil, err
	}
	return &schema.MailAttachmentFile{MessageID: messageID, AttachmentID: attachmentID, Path: f.Path}, nil
}

// ListAttention implements attention.Provider. It answers an empty list
// (see this file's package doc comment and INV-MAIL-4).
func (b *Backend) ListAttention(context.Context) ([]schema.AttentionItem, error) {
	return []schema.AttentionItem{}, nil
}

// searchAdapter is the search.Provider view of a Backend: Go allows one
// Search method per type, and Backend's is mail.Provider's, so the
// cross-cutting search capability is served by this thin wrapper instead.
type searchAdapter struct{ b *Backend }

var _ search.Provider = searchAdapter{}

// Search implements search.Provider: fields is unused — this backend
// populates no schema.SearchResult.Attributes beyond the core set
// [freedom boundary].
func (a searchAdapter) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	b := a.b
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	msgs, _, err := b.searchMessages(ctx, cfg, query, "", 0)
	if err != nil {
		return nil, err
	}
	results := make([]schema.SearchResult, 0, len(msgs))
	for _, m := range msgs {
		results = append(results, schema.SearchResult{
			Type:   searchResultType,
			ID:     m.ID,
			Title:  m.Subject,
			Source: searchSourceName,
		})
	}
	return results, nil
}

// SearchProvider returns the search.Provider view of this backend, for the
// cross-cutting search capability's dispatch table.
func (b *Backend) SearchProvider() search.Provider { return searchAdapter{b: b} }
