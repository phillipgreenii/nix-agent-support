// mail.go: the mail entity/capability's shared JSON wire shape — a NEW
// entity-type capability, not a new backend on an existing one (phillipgreenii-
// nix-agent-support ADR 0062, "Decision" item 10), built using
// pkg/schema/calendar.go's own field/doc-comment conventions as its
// structural precedent.
//
// schema.MailMessage is an INDEPENDENTLY-defined type: it is not a
// re-export or wrapper of any bridge-side wire type. It carries every
// property of a Mail.app message that has a mail-domain meaning and that
// the capability's read/mutate/attachment ops need, plus an OPTIONAL static
// mailbox-priority classification tag and the standard AsOf/Stale pair.
//
// No delete concept exists anywhere in this schema or its capability: see
// invariant INV-MAIL-1 (packages/pg-connector/docs/behavior/invariants.md,
// "Mail has no delete").
package schema

// MailSchemaVersion is the mail capability's own schema version,
// populated into the wire envelope's schemaVersion field by the mail
// capability's dispatch-table entries (pkg/provider/mail.NewDispatchTable)
// — independent of every other capability's own schema version (each entity
// type/capability versions its own schema separately, INV-VER-1) and of
// pkg/scriptout.ProtocolVersion. This is the mail capability's first
// version: it has no prior shape to have bumped from. It is also registered
// into CurrentSchemaVersions in versions.go, so a future mail-capability
// schema-version mismatch is detectable rather than silently skipped by
// cmd/pg-connector/config_validate.go's checkSchemaVersions — mirrors
// CalendarSchemaVersion's identical precedent.
const MailSchemaVersion = 1

// MailAttachment is the metadata of one attachment of a MailMessage. It
// carries no attachment content: a caller obtains the bytes (as a local
// file) only through the capability's separate fetch_attachment op.
type MailAttachment struct {
	// ID is the attachment's identity within its message, usable as the
	// attachment_id arg of the fetch_attachment op. It is opaque to the
	// caller; only the backend (and the bridge-side service behind it)
	// interprets it. Scoped to the owning message: two messages MAY carry
	// attachments with the same ID.
	ID string `json:"id"`

	// Filename is the attachment's own file name as reported by Mail.app.
	Filename string `json:"filename"`

	// MimeType is the attachment's MIME type, BEST-EFFORT: empty when the
	// underlying system does not supply one.
	MimeType string `json:"mime_type,omitempty"`

	// Size is the attachment's size in bytes; 0 means the underlying
	// system did not supply one.
	Size int64 `json:"size,omitempty"`
}

// MailMessage is the mail capability's shared JSON wire shape, returned by
// "show" and one element of "list"'s / "search_messages"'s
// MailListResult.Entities.
type MailMessage struct {
	// ID is the message's identity: the real, globally-unique Message-ID
	// header value (the correct primary key — NOT a Mail.app-internal
	// integer id), in whatever surrounding-bracket form the bridge reports
	// it. A string, per every other capability's own "ID is a string"
	// convention (Issue.ID's doc comment). Opaque to the caller.
	ID string `json:"id"`

	// Subject is the message's subject line.
	Subject string `json:"subject"`

	// Sender is the message's sender as ONE "Name <email>" string, exactly
	// as Mail.app reports it (never split into separate name/email wire
	// fields). A sender with no display name MAY be a bare address. How a
	// consumer splits this string for identity matching is fixed by
	// invariant INV-MAIL-2 (packages/pg-connector/docs/behavior/
	// invariants.md, "Mail sender identity matching").
	Sender string `json:"sender"`

	// DateReceived is when the message was received (RFC3339, in whatever
	// timezone the backend's own system reports it).
	DateReceived string `json:"date_received"`

	// Read reports whether the message is marked read.
	Read bool `json:"read"`

	// Flagged reports whether the message is flagged.
	Flagged bool `json:"flagged"`

	// Mailbox is the name of the mailbox the message is currently filed
	// under, as reported by the backend's own system.
	Mailbox string `json:"mailbox"`

	// Attachments lists the message's attachments' metadata. Empty when the
	// message has none.
	Attachments []MailAttachment `json:"attachments,omitempty"`

	// Body is the message's plain-text content. OPTIONAL: populated by
	// "show" only when the backend can supply it, and absent from the
	// entries "list"/"search_messages" return (a listing MUST NOT pay to
	// fetch every body). Absence means "not supplied", never "empty
	// message".
	Body string `json:"body,omitempty"`

	// MailboxPriority is this capability's static-classification field —
	// e.g. "high"/"low", read from the backend's own per-mailbox config
	// (mirroring CalendarEvent.CalendarPriority's convention, so the tier
	// is on the WIRE rather than only an internal input of one backend). It
	// has no counterpart in anything Mail.app reports. OPTIONAL: empty when
	// the backend has no configured priority tier for this message's
	// mailbox, which is the expected steady state until tiers are decided
	// (operator ruling, Phillip, 2026-10-05: the field exists but is left
	// unset for now). It is NOT an attention severity input (see the
	// INV-MAIL-3 invariant in packages/pg-connector/docs/behavior/
	// invariants.md).
	MailboxPriority string `json:"mailbox_priority,omitempty"`

	// AsOf is this read's own as-of time (RFC3339, UTC), mirroring
	// Issue.AsOf/Thread.AsOf's identical INV-ASOF-1 contract: "every
	// acted-on read seam MUST carry its own as-of time." Empty only when
	// this backend has no usable as-of time for this read, which MUST pair
	// with Stale true rather than a plausible-looking but meaningless
	// timestamp.
	AsOf string `json:"as_of"`

	// Stale is this backend's own as-of/stale determination for this read
	// (INV-ASOF-2), mirroring every other current backend's identical
	// precedent (Thread.Stale's own doc comment).
	Stale bool `json:"stale"`
}

// MailListResult is the "list"/"search_messages" ops' shared wire result
// payload for the mail capability — the same generic shape
// ThreadListResult documents in full (see its own doc comment for
// Cursor/Entities/PresentIDs/ids_only semantics, which apply identically
// here with MailMessage in place of Thread), with these departures:
//
//   - Truncated is BACKEND-DETERMINED, neither unconditionally true
//     (ThreadListResult: its backend cannot confirm completeness) nor
//     unconditionally false (CalendarListResult: a complete range query). A
//     mail backend sets it true exactly when the reply was cut short by the
//     request's limit or by a backend-side cap, or when the backend cannot
//     rule out further matching messages; it sets it false only when it
//     CAN confirm the reply holds the complete match set.
//   - Cursor is always nil: this capability has no incremental-listing
//     backend at this phase.
type MailListResult struct {
	Entities   []MailMessage `json:"entities"`
	PresentIDs []string      `json:"present_ids"`
	Cursor     *string       `json:"cursor"`
	Truncated  bool          `json:"truncated"`
}

// MailAttachmentFile is the "fetch_attachment" op's wire result: where the
// attachment was saved. Path is an absolute local filesystem path under a
// destination directory that is part of the backend implementation's own
// CONFIG (never a request field — operator ruling, Phillip, 2026-10-05,
// bead pg2-qc5uc.1). A caller MUST treat Path as opaque: which directory it
// is under, and how the file is named, is the bridge-side service's and the
// backend's concern.
type MailAttachmentFile struct {
	// MessageID is the owning message's ID, echoing the request.
	MessageID string `json:"message_id"`

	// AttachmentID is the fetched attachment's ID, echoing the request.
	AttachmentID string `json:"attachment_id"`

	// Path is the full local filesystem path the attachment was saved to.
	Path string `json:"path"`
}
