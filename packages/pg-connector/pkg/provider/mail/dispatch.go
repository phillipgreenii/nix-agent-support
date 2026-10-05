// dispatch.go: builds the mail capability's own op-dispatch table, bound to
// a concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; a Tier-2 mail backend's
// own main() calls NewDispatchTable and hands the result to ServeLoop —
// this package builds no binary of its own (INV-WIRE-1), mirroring
// pkg/provider/calendar/dispatch.go's identical structure and
// error-passthrough convention.
package mail

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the mail capability's op-dispatch table for p:
// exactly the eight ops below always; auth_status only when p also
// implements pkg/provider.AuthChecker, asserted via a type-check rather
// than folded into the Provider interface (INV-AUTH-1). No delete-shaped
// op exists or MUST ever be registered (invariant INV-MAIL-1); no
// create/reply op exists at this phase either. Every handler passes p's
// returned error straight through unwrapped — a well-behaved Provider
// implementation (built by a Tier-2 mail backend packet) is responsible for
// wrapping its own errors with the matching pkg/scriptout.Err* sentinel
// (e.g. ErrUnavailable, ErrNotFound); this table does no sentinel
// translation of its own, except for its own args-decoding / argument-shape
// failures (invalid_argument) below.
//
// Wire ops, with their wire-envelope JSON args keys (the pinned contract a
// later packet's scriptout.Invoke calls rely on; every op is stamped with
// schema.MailSchemaVersion):
//
//	list              {mailbox?, unread_only?, limit?, ids_only?}  -> schema.MailListResult
//	show              {id}                                         -> schema.MailMessage
//	search_messages   {query, mailbox?, limit?}                    -> schema.MailListResult
//	mark_read         {id}                                         -> null
//	mark_unread       {id}                                         -> null
//	archive           {id}                                         -> null
//	unarchive         {id}                                         -> null
//	fetch_attachment  {id, attachment_id}                          -> schema.MailAttachmentFile
//
// "id" is the message id (schema.MailMessage.ID) in every id-keyed op;
// fetch_attachment's attachment_id is schema.MailAttachment.ID. mailbox,
// when empty or omitted, means every mailbox the backend is configured for.
// limit omitted or 0 means the backend's own default cap; a negative limit
// answers scriptout.ErrInvalidArgument. An empty id (or attachment_id) or
// an empty search query answers scriptout.ErrInvalidArgument before the
// Provider is ever called. The search op is named "search_messages", never
// "search", so a single binary can also serve the cross-cutting search
// capability (whose own wire op is "search") without an op-name collision.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"list": {
			SchemaVersion: schema.MailSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Mailbox    string `json:"mailbox"`
					UnreadOnly bool   `json:"unread_only"`
					Limit      int    `json:"limit"`
					IDsOnly    bool   `json:"ids_only"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
				}
				if a.Limit < 0 {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "list: limit must not be negative")
				}
				return p.List(ctx, a.Mailbox, a.UnreadOnly, a.Limit, a.IDsOnly)
			},
		},
		"show": {
			SchemaVersion: schema.MailSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				id, err := decodeID("show", args)
				if err != nil {
					return nil, err
				}
				return p.Show(ctx, id)
			},
		},
		"search_messages": {
			SchemaVersion: schema.MailSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query   string `json:"query"`
					Mailbox string `json:"mailbox"`
					Limit   int    `json:"limit"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode search_messages args: "+err.Error())
				}
				if a.Query == "" {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search_messages: query must not be empty")
				}
				if a.Limit < 0 {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search_messages: limit must not be negative")
				}
				return p.Search(ctx, a.Query, a.Mailbox, a.Limit)
			},
		},
		"mark_read":   idOnlyOp("mark_read", p.MarkRead),
		"mark_unread": idOnlyOp("mark_unread", p.MarkUnread),
		"archive":     idOnlyOp("archive", p.Archive),
		"unarchive":   idOnlyOp("unarchive", p.Unarchive),
		"fetch_attachment": {
			SchemaVersion: schema.MailSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID           string `json:"id"`
					AttachmentID string `json:"attachment_id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode fetch_attachment args: "+err.Error())
				}
				if a.ID == "" || a.AttachmentID == "" {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "fetch_attachment: id and attachment_id must not be empty")
				}
				return p.FetchAttachment(ctx, a.ID, a.AttachmentID)
			},
		},
	}

	// A provider not implementing AuthChecker is not treated as a
	// forced/meaningless answer: the auth_status entry is simply omitted,
	// which pg-connector's own fan-out (cmd/pg-connector/auth.go) already
	// recognizes generically via the wire-level unknown_op sentinel and
	// reports as "disabled: not applicable" (INV-AUTH-1).
	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.MailSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// auth_status always answers with a well-formed result
				// (never a wire-level error) — the AuthStatus.State field
				// itself carries success/failure, matching pg-connector's
				// existing fan-out convention (cmd/pg-connector/auth.go).
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}

// decodeID decodes an id-keyed op's {id} args, answering
// scriptout.ErrInvalidArgument for malformed JSON or an empty id.
func decodeID(op string, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := scriptout.Decode(args, &a); err != nil {
		return "", scriptout.WrapError(scriptout.ErrInvalidArgument, "decode "+op+" args: "+err.Error())
	}
	if a.ID == "" {
		return "", scriptout.WrapError(scriptout.ErrInvalidArgument, op+": id must not be empty")
	}
	return a.ID, nil
}

// idOnlyOp builds the dispatch entry shared by every id-keyed, no-result
// mutation (mark_read, mark_unread, archive, unarchive): decode {id}, call
// fn, answer a nil result payload on success.
func idOnlyOp(op string, fn func(ctx context.Context, id string) error) scriptout.OpHandler {
	return scriptout.OpHandler{
		SchemaVersion: schema.MailSchemaVersion,
		Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			id, err := decodeID(op, args)
			if err != nil {
				return nil, err
			}
			if err := fn(ctx, id); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
