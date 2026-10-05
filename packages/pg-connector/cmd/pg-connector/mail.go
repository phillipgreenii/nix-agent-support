// mail.go: the "pg-connector mail" CLI verb group — wiring the mail
// capability's already-landed Provider/NewDispatchTable
// (pkg/provider/mail) and wire schema (pkg/schema/mail.go) into
// pg-connector's Tier-1 core. The capability and its place in the
// architecture are recorded in phillipgreenii-nix-agent-support ADR 0062,
// "Decision" item 10, and the behavior-docs set under
// packages/pg-connector/docs/behavior/ (the "Per-capability op catalog"
// in interfaces.md; invariant INV-MAIL-1, "Mail has no delete").
//
// connector.mail is list-valued (multiple simultaneously-registered mail
// backends, matching pr/issue/ci/thread/calendar — never scm's
// single-valued one); registry.go's entityTypes carries "mail".
//
// Verbs: list, show, search, mark-read, mark-unread, archive, unarchive
// and attachment fetch. There is NO delete verb of any kind, and none
// MUST EVER be added (INV-MAIL-1) — archive moves a message out of its
// mailbox without destroying it. There is likewise no create, reply or
// changes verb: the design names no mail delta feed, and create/reply are
// a later bead's concern.
//
// Exit codes follow the kind of op:
//
//   - list and search are FAN-OUT ops over every registered mail backend
//     (or the one --backend pins), reporting each backend's health as one
//     sources[] row (INV-OUT-1) under the ordinary 0/2/3 scheme
//     (outcome.go's FanOutOutcome.ExitCode).
//   - show, mark-read, mark-unread, archive, unarchive and attachment
//     fetch are id-keyed TARGETED ops resolved by DispatchTargeted (try
//     each registered backend, stop at the first non-not_found answer;
//     --backend pins one) under the targeted 0/4/1 scheme
//     (outcome.go's TargetedExitCode).
//
// This file imports only pkg/schema and pkg/scriptout, never
// pkg/provider/<capability>: the wire op names and args keys it uses are
// the ones pkg/provider/mail.NewDispatchTable pins ("list", "show",
// "search_messages", "mark_read", "mark_unread", "archive", "unarchive",
// "fetch_attachment"). The search verb dispatches the DISTINCTLY named
// "search_messages" wire op, never "search" (that name belongs to the
// cross-cutting search capability).
//
// Like calendar and thread, no umbrella entity-cache fallback is wired
// for mail (a single-backend capability today): a degraded backend is
// simply reported degraded.
//
// "attachment fetch" prints the saved local path the backend returned and
// treats it as an opaque string; the destination directory is part of the
// backend implementation's own config, never a CLI flag.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newMailCmd() *cobra.Command {
	mailCmd := &cobra.Command{
		Use:   "mail",
		Short: "Mail capability commands (no delete)",
	}
	mailCmd.AddCommand(newMailListCmd())
	mailCmd.AddCommand(newMailShowCmd())
	mailCmd.AddCommand(newMailSearchCmd())
	mailCmd.AddCommand(newMailIDOnlyCmd("mark-read", "mark_read", "Mark a message read", "marked read"))
	mailCmd.AddCommand(newMailIDOnlyCmd("mark-unread", "mark_unread", "Mark a message unread", "marked unread"))
	mailCmd.AddCommand(newMailIDOnlyCmd("archive", "archive", "Archive a message (moves it out of its mailbox; never deletes it)", "archived"))
	mailCmd.AddCommand(newMailIDOnlyCmd("unarchive", "unarchive", "Unarchive a message, returning it to a mailbox", "unarchived"))
	mailCmd.AddCommand(newMailAttachmentCmd())
	return mailCmd
}

// mailListOutcome is "mail list"'s/"mail search"'s wire response: every
// queried mail backend's matched messages concatenated into Entities, with
// each backend's own health as one sources[] row (INV-OUT-1) — mirroring
// calendarListOutcome's/threadListOutcome's identical shape.
type mailListOutcome struct {
	Entities   []schema.MailMessage `json:"entities"`
	PresentIDs []string             `json:"present_ids"`
	Sources    []SourceResult       `json:"sources"`
}

// fanOutMail invokes the given wire op with args against every backend in
// backends, concatenating their matched messages and building one
// sources[] row per backend queried — structurally mirroring
// fanOutCalendarList (calendar.go): a direct, per-backend scriptout.Invoke
// call using the verb's own distinctly-named wire op. A backend not
// implementing the op (recognized generically via the wire-level
// unknown_op sentinel) is reported disabled with reason "not applicable";
// any other error is reported degraded with that error's own message.
func fanOutMail(ctx context.Context, reg *Registry, backends []string, op string, args map[string]any) mailListOutcome {
	// Entities/PresentIDs/Sources all start as non-nil empty slices so a
	// zero-backend result, or a backend answering zero matches, still
	// marshals entities[]/present_ids[]/sources[] as [] rather than null.
	out := mailListOutcome{
		Entities:   make([]schema.MailMessage, 0),
		PresentIDs: make([]string, 0),
		Sources:    make([]SourceResult, 0, len(backends)),
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, op, args, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var result schema.MailListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Entities = append(out.Entities, result.Entities...)
		out.PresentIDs = append(out.PresentIDs, result.PresentIDs...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(result.PresentIDs)})
	}
	return out
}

// runMailFanOut is the shared RunE body of "mail list" and "mail search":
// load the registry, resolve the backends (all registered mail backends,
// or the one --backend pins), fan out op, and report. Both CLI-level
// failure paths (registry-load, an unresolvable --backend pin) route
// through reportMailTargetedOutcome so stdout carries a JSON error
// envelope rather than a bare stderr line (thread.go's precedent).
func runMailFanOut(cmd *cobra.Command, backendPin, op string, args map[string]any) error {
	reg, err := LoadRegistry()
	if err != nil {
		return reportMailTargetedOutcome(cmd, nil, err, humanizeMailNothing)
	}
	backends, err := resolveListBackends(reg, "mail", backendPin)
	if err != nil {
		return reportMailTargetedOutcome(cmd, nil, err, humanizeMailNothing)
	}
	outcome := fanOutMail(cmd.Context(), reg, backends, op, args)
	return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
		return humanizeMailListOutcome(outcome)
	})
}

func newMailListCmd() *cobra.Command {
	var mailbox string
	var limit int
	var unreadOnly, idsOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages, fanned out across every registered mail backend unless --backend pins one",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered mail backend")
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "pin to exactly this mailbox; empty means every mailbox a backend is configured for")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of returned messages; 0 means each backend's own default cap")
	cmd.Flags().BoolVar(&unreadOnly, "unread-only", false, "return only messages not marked read")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "return only each matched message's id, omitting full entity detail")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runMailFanOut(cmd, *backendFlag, "list", map[string]any{
			"mailbox":     mailbox,
			"unread_only": unreadOnly,
			"limit":       limit,
			"ids_only":    idsOnly,
		})
	}
	return cmd
}

func newMailSearchCmd() *cobra.Command {
	var mailbox string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search messages by free text, fanned out across every registered mail backend unless --backend pins one",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered mail backend")
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "pin to exactly this mailbox; empty means every mailbox a backend is configured for")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of returned messages; 0 means each backend's own default cap")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runMailFanOut(cmd, *backendFlag, "search_messages", map[string]any{
			"query":   args[0],
			"mailbox": mailbox,
			"limit":   limit,
		})
	}
	return cmd
}

func newMailShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a message's current state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportMailTargetedOutcome(cmd, nil, err, humanizeMailShow)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "mail", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportMailTargetedOutcome(cmd, resp, dispatchErr, humanizeMailShow)
	}
	return cmd
}

// newMailIDOnlyCmd builds one of the four id-keyed, no-result mutating
// verbs (mark-read, mark-unread, archive, unarchive): a targeted op over
// the wire op named wireOp, answering the targeted 0/4/1 exit scheme.
// done is the past-tense phrase the human rendering uses.
func newMailIDOnlyCmd(verb, wireOp, short, done string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   verb + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		humanize := func(json.RawMessage) (string, error) {
			return fmt.Sprintf("Message %s %s", args[0], done), nil
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportMailTargetedOutcome(cmd, nil, err, humanize)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "mail", wireOp, map[string]string{"id": args[0]}, *backendFlag)
		return reportMailTargetedOutcome(cmd, resp, dispatchErr, humanize)
	}
	return cmd
}

// newMailAttachmentCmd is the "mail attachment" sub-group, currently
// holding only "fetch".
func newMailAttachmentCmd() *cobra.Command {
	attachmentCmd := &cobra.Command{
		Use:   "attachment",
		Short: "Mail attachment commands",
	}
	attachmentCmd.AddCommand(newMailAttachmentFetchCmd())
	return attachmentCmd
}

func newMailAttachmentFetchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch <message-id> <attachment-id>",
		Short: "Save an attachment locally and print the saved path",
		Args:  cobra.ExactArgs(2),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportMailTargetedOutcome(cmd, nil, err, humanizeMailAttachmentFetch)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "mail", "fetch_attachment", map[string]string{
			"id":            args[0],
			"attachment_id": args[1],
		}, *backendFlag)
		return reportMailTargetedOutcome(cmd, resp, dispatchErr, humanizeMailAttachmentFetch)
	}
	return cmd
}

// humanizeMailListOutcome formats "mail list"'s/"mail search"'s fan-out
// outcome for human display, mirroring humanizeThreadListOutcome's shape:
// --ids-only leaves Entities empty by design and renders PresentIDs as a
// plain id list instead of falling through to the empty "(none)" branch.
func humanizeMailListOutcome(o mailListOutcome) string {
	var b strings.Builder
	switch {
	case len(o.Entities) > 0:
		fmt.Fprintf(&b, "messages (%d):\n", len(o.Entities))
		for _, m := range o.Entities {
			state := "unread"
			if m.Read {
				state = "read"
			}
			fmt.Fprintf(&b, "  [%s] %q from %s %s (%s, mailbox=%s)\n", m.ID, m.Subject, m.Sender, m.DateReceived, state, m.Mailbox)
		}
	case len(o.PresentIDs) > 0:
		fmt.Fprintf(&b, "messages (%d, ids only):\n", len(o.PresentIDs))
		for _, id := range o.PresentIDs {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	default:
		b.WriteString("messages: (none)\n")
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

// reportMailTargetedOutcome writes resp's outcome to stdout, mirroring
// reportThreadTargetedOutcome's thin wrapper around output.go's
// writeTargetedResult.
func reportMailTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// humanizeMailNothing is the humanize callback for the fan-out verbs'
// CLI-level failure paths: writeHumanTargeted never calls humanize on the
// error branch, so this only needs to satisfy humanizeResult's signature.
func humanizeMailNothing(json.RawMessage) (string, error) { return "", nil }

// humanizeMailShow formats a `mail show` result (schema.MailMessage) for
// human display.
func humanizeMailShow(raw json.RawMessage) (string, error) {
	var m schema.MailMessage
	if err := scriptout.Decode(raw, &m); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "message %s [%s]\n", m.ID, m.Mailbox)
	fmt.Fprintf(&b, "  subject: %s\n", m.Subject)
	fmt.Fprintf(&b, "  from: %s\n", m.Sender)
	fmt.Fprintf(&b, "  received: %s\n", m.DateReceived)
	fmt.Fprintf(&b, "  read: %t\n", m.Read)
	fmt.Fprintf(&b, "  flagged: %t\n", m.Flagged)
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "  attachment: [%s] %s\n", a.ID, a.Filename)
	}
	if m.Body != "" {
		fmt.Fprintf(&b, "  body: %s\n", m.Body)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// humanizeMailAttachmentFetch formats a `mail attachment fetch` result
// (schema.MailAttachmentFile) for human display: the saved path, treated
// as an opaque string.
func humanizeMailAttachmentFetch(raw json.RawMessage) (string, error) {
	var f schema.MailAttachmentFile
	if err := scriptout.Decode(raw, &f); err != nil {
		return "", err
	}
	return f.Path, nil
}
