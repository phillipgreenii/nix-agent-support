// Package mail declares the mail capability's provider interface — a
// small, capability-scoped Go interface (never named after a
// backend/system, INV-CAP-1) that a Tier-2 mail backend's concrete provider
// implements. It mirrors pkg/provider/calendar's and pkg/provider/thread's
// own package shape (package named after the capability, interface named
// Provider — never e.g. mail.Source) as its STRUCTURAL template. The
// capability and its place in the architecture are recorded in
// phillipgreenii-nix-agent-support ADR 0062, "Decision" item 10, and the
// behavior-docs set under packages/pg-connector/docs/behavior/ ("Per-
// capability op catalog" in interfaces.md; invariants INV-MAIL-1..3).
//
// The capability is NEW, not a backend on an existing one: its registry
// key is the list-valued connector.mail (like pr/issue/ci/thread/calendar,
// never single-valued like scm). The registry wiring itself is the Tier-1
// umbrella's concern and is not built by this package.
//
// The op set is EXACTLY: list, show, search, mark read/unread,
// archive/unarchive, and attachment fetch. There is NO delete method on
// this interface, and none MUST EVER be added (invariant INV-MAIL-1, "Mail
// has no delete"): a mail backend MUST NOT expose any delete operation
// either. There is likewise no create/reply method at this phase (a later
// bead MAY add them).
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package mail

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the mail capability's provider interface: two listing reads
// (List and Search), one single-message read (Show), four id-keyed state
// mutations (MarkRead, MarkUnread, Archive, Unarchive) and one attachment
// fetch (FetchAttachment). A concrete backend MAY additionally implement
// pkg/provider.AuthChecker, asserted via a type-check rather than folded
// into this interface (INV-AUTH-1) — see NewDispatchTable in dispatch.go.
//
// A message is identified by its message id (schema.MailMessage.ID, the
// real, globally-unique Message-ID header value). Every id-keyed method
// MUST answer scriptout.ErrNotFound for an id its backend does not
// recognize, so DispatchTargeted's multi-instance try-each resolution
// policy (INV-REG-2) can fall through to the next registered backend.
//
// Wire op names and wire-envelope JSON args keys are pinned in
// dispatch.go's NewDispatchTable doc comment and in the behavior-docs op
// catalog (interfaces.md) — a later packet's scriptout.Invoke call needs no
// guesswork.
type Provider interface {
	// List returns messages in mailbox, newest first at the backend's
	// discretion [freedom boundary]. mailbox, when empty, means every
	// mailbox this backend is configured for; a non-empty value pins to
	// exactly the named mailbox. unreadOnly, when true, restricts the
	// result to messages not marked read. limit caps the number of returned
	// entities; 0 means the backend's own default cap. A negative limit is
	// rejected by dispatch.go before List is ever called. idsOnly, when
	// true, means the caller only wants MailListResult.PresentIDs
	// populated; a Provider MAY still choose to populate Entities anyway
	// (harmless, just wasted work) but need not.
	//
	// Unlike calendar/issue/pr, this op is NOT a named-query op: it takes
	// explicit parameters (mirroring ci.Provider.ListRuns's own
	// "fan-out-shaped, parameter-keyed, NOT a named query" precedent), so
	// no config.queries resolution happens for it. The reply's Truncated
	// is backend-determined (see schema.MailListResult).
	List(ctx context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error)

	// Show returns message id's current state, including its attachment
	// metadata (and its Body when the backend can supply one). An id the
	// backend does not recognize MUST answer scriptout.ErrNotFound.
	Show(ctx context.Context, id string) (*schema.MailMessage, error)

	// Search returns the messages matching query, optionally pinned to one
	// mailbox (empty means every configured mailbox) and capped at limit
	// (0 means the backend's own default cap; negative is rejected by
	// dispatch.go). query is a plain free-text string, NOT a named query
	// and NOT a JQL-style grammar: what it is matched against (subject,
	// sender, body) is the backend's concern [freedom boundary]. dispatch.go
	// rejects an empty query as scriptout.ErrInvalidArgument before Search
	// is called. The reply's Truncated is backend-determined.
	//
	// This is the capability's DEDICATED search op; the same backend is
	// also reachable through the cross-cutting pkg/provider/search
	// Provider, whose own wire op is named "search". To keep both
	// dispatchable from ONE binary without an op-name collision, this
	// op's wire name is "search_messages" (see NewDispatchTable).
	Search(ctx context.Context, query, mailbox string, limit int) (*schema.MailListResult, error)

	// MarkRead marks message id read. No result payload.
	MarkRead(ctx context.Context, id string) error

	// MarkUnread marks message id unread. No result payload.
	MarkUnread(ctx context.Context, id string) error

	// Archive moves message id out of its current mailbox into the
	// backend system's archive location. The message id is stable across
	// the move. Archiving is NOT deletion: the message MUST remain
	// retrievable (Show, Unarchive) after it. No result payload.
	Archive(ctx context.Context, id string) error

	// Unarchive reverses Archive, returning message id to the mailbox it
	// is restorable to — which mailbox that is, is the backend's concern
	// [freedom boundary]. No result payload.
	Unarchive(ctx context.Context, id string) error

	// FetchAttachment saves attachment attachmentID of message messageID
	// to a local file and returns where. It returns a filesystem PATH, not
	// bytes (schema.MailAttachmentFile.Path). The destination DIRECTORY is
	// part of the implementation's own CONFIG, never a request field; a
	// caller MUST treat the returned path as opaque — where it comes from
	// is the bridge-side service's and the backend's concern. An unknown
	// messageID or attachmentID MUST answer scriptout.ErrNotFound.
	FetchAttachment(ctx context.Context, messageID, attachmentID string) (*schema.MailAttachmentFile, error)
}
