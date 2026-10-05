package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/mail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeTransport is a stubbed Transport double — no live socket, no Mail.app —
// keyed by mailbox name so a test can script per-mailbox replies
// independently, and recording every call.
type fakeTransport struct {
	lists       map[string][]apiMessage
	listErrs    map[string]error
	searches    map[string][]apiMessage
	searchErrs  map[string]error
	detail      apiMessageDetail
	detailErr   error
	attachment  apiAttachmentFile
	attachErr   error
	stateErr    error
	listCalls   []apiListQuery
	searchCalls []apiSearchQuery
	stateCalls  []string
	attachCalls []apiAttachmentRef
}

func (f *fakeTransport) List(_ context.Context, q apiListQuery) ([]apiMessage, error) {
	f.listCalls = append(f.listCalls, q)
	if err := f.listErrs[q.Mailbox]; err != nil {
		return nil, err
	}
	return f.lists[q.Mailbox], nil
}

func (f *fakeTransport) Show(context.Context, string) (apiMessageDetail, error) {
	return f.detail, f.detailErr
}

func (f *fakeTransport) Search(_ context.Context, q apiSearchQuery) ([]apiMessage, error) {
	f.searchCalls = append(f.searchCalls, q)
	if err := f.searchErrs[q.Mailbox]; err != nil {
		return nil, err
	}
	return f.searches[q.Mailbox], nil
}

func (f *fakeTransport) SetRead(_ context.Context, id string, read bool) error {
	op := "mark-unread"
	if read {
		op = "mark-read"
	}
	f.stateCalls = append(f.stateCalls, op+":"+id)
	return f.stateErr
}

func (f *fakeTransport) Archive(_ context.Context, id string) error {
	f.stateCalls = append(f.stateCalls, "archive:"+id)
	return f.stateErr
}

func (f *fakeTransport) Unarchive(_ context.Context, id string) error {
	f.stateCalls = append(f.stateCalls, "unarchive:"+id)
	return f.stateErr
}

func (f *fakeTransport) FetchAttachment(_ context.Context, messageID, attachmentID string) (apiAttachmentFile, error) {
	f.attachCalls = append(f.attachCalls, apiAttachmentRef{MessageID: messageID, AttachmentID: attachmentID})
	return f.attachment, f.attachErr
}

var _ Transport = (*fakeTransport)(nil)

// ctxWithConfig builds a context carrying cfg as the request's own opaque
// config block, via scriptout.WithConfig — the same mechanism ServeLoop
// uses on every real request.
func ctxWithConfig(t *testing.T, cfg any) context.Context {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return scriptout.WithConfig(context.Background(), raw)
}

var fixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newBackend(ft *fakeTransport) *Backend {
	b := New(ft)
	b.now = func() time.Time { return fixedNow }
	return b
}

func msg(id, mailbox string, minutesAgo int) apiMessage {
	return apiMessage{
		ID:           id,
		Subject:      "subject of " + id,
		Sender:       "Ada <ada@example.com>",
		DateReceived: fixedNow.Add(-time.Duration(minutesAgo) * time.Minute),
		Mailbox:      mailbox,
	}
}

func ids(entities []string) string { return strings.Join(entities, ",") }

// ----------------------------------------------------------------------
// Compile-time / type-check wiring
// ----------------------------------------------------------------------

func TestBackend_ImplementsMailAndOptionalCapabilities(t *testing.T) {
	b := New(&fakeTransport{})
	var _ mail.Provider = b
	var _ attention.Provider = b
	var _ search.Provider = b.SearchProvider()
}

func TestBackend_NoDeleteShapedMethod(t *testing.T) {
	// INV-MAIL-1: neither the Transport seam nor the Backend exposes a
	// delete-shaped method.
	for _, typ := range []reflect.Type{reflect.TypeOf((*Transport)(nil)).Elem(), reflect.TypeOf(&Backend{})} {
		for i := 0; i < typ.NumMethod(); i++ {
			name := strings.ToLower(typ.Method(i).Name)
			for _, bad := range []string{"delete", "remove", "trash", "expunge", "purge", "destroy"} {
				if strings.Contains(name, bad) {
					t.Errorf("%s has delete-shaped method %q", typ, typ.Method(i).Name)
				}
			}
		}
	}
}

// ----------------------------------------------------------------------
// Translation: bridge message -> schema.MailMessage
// ----------------------------------------------------------------------

func TestBackend_List_TranslatesMessageAndAttachments(t *testing.T) {
	m := msg("<a@example.com>", "INBOX", 5)
	m.Read, m.Flagged = true, true
	m.Attachments = []apiAttachment{
		{ID: "att-1", Name: "report.pdf", MimeType: "application/pdf", Size: 2048},
		{ID: "att-2", Name: "blob.bin", MimeType: "", Size: 0}, // bridge sent no mime type
	}
	ft := &fakeTransport{lists: map[string][]apiMessage{"INBOX": {m}}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})

	res, err := b.List(ctx, "", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Entities) != 1 || len(res.PresentIDs) != 1 || res.PresentIDs[0] != "<a@example.com>" {
		t.Fatalf("res = %+v", res)
	}
	if res.Cursor != nil || res.Truncated {
		t.Fatalf("Cursor = %v, Truncated = %v, want nil/false", res.Cursor, res.Truncated)
	}
	got := res.Entities[0]
	if got.ID != "<a@example.com>" || got.Subject != "subject of <a@example.com>" || got.Sender != "Ada <ada@example.com>" ||
		!got.Read || !got.Flagged || got.Mailbox != "INBOX" || got.Body != "" {
		t.Fatalf("entity = %+v", got)
	}
	if got.DateReceived != fixedNow.Add(-5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("DateReceived = %q", got.DateReceived)
	}
	if got.AsOf != fixedNow.Format(time.RFC3339) || got.Stale {
		t.Fatalf("AsOf = %q Stale = %v", got.AsOf, got.Stale)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("Attachments = %+v", got.Attachments)
	}
	if a := got.Attachments[0]; a.ID != "att-1" || a.Filename != "report.pdf" || a.MimeType != "application/pdf" || a.Size != 2048 {
		t.Fatalf("attachment 0 = %+v", a)
	}
	if a := got.Attachments[1]; a.ID != "att-2" || a.Filename != "blob.bin" || a.MimeType != "" {
		t.Fatalf("attachment 1 = %+v (mime type must stay empty when the bridge sends none)", a)
	}
}

func TestBackend_List_IDsOnly_OmitsEntities(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1)}}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})

	res, err := b.List(ctx, "", false, 0, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Entities != nil || len(res.PresentIDs) != 1 {
		t.Fatalf("res = %+v, want PresentIDs only", res)
	}
}

func TestBackend_Show_IncludesBodyAndPriority(t *testing.T) {
	ft := &fakeTransport{detail: apiMessageDetail{apiMessage: msg("<a@example.com>", "INBOX", 1), To: []string{"me@example.com"}, Body: "full text"}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX", Priority: "high"}}})

	got, err := b.Show(ctx, "<a@example.com>")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Body != "full text" || got.MailboxPriority != "high" || got.ID != "<a@example.com>" {
		t.Fatalf("got = %+v", got)
	}
}

func TestBackend_Show_NotFound_PassedThrough(t *testing.T) {
	ft := &fakeTransport{detailErr: scriptout.WrapError(scriptout.ErrNotFound, "no such message")}
	_, err := newBackend(ft).Show(context.Background(), "<missing@example.com>")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// ----------------------------------------------------------------------
// Mailbox -> priority-tier mapping
// ----------------------------------------------------------------------

func TestBackend_List_StampsMailboxPriorityFromConfig(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{
		"INBOX":   {msg("<a@example.com>", "INBOX", 1)},
		"Receipt": {msg("<b@example.com>", "Receipt", 2)},
		"Plain":   {msg("<c@example.com>", "Plain", 3)},
	}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{
		{Name: "INBOX", Priority: "high"},
		{Name: "Receipt", Priority: "low"},
		{Name: "Plain"}, // priority is optional
	}})

	res, err := b.List(ctx, "", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]string{}
	for _, e := range res.Entities {
		got[e.ID] = e.MailboxPriority
	}
	want := map[string]string{"<a@example.com>": "high", "<b@example.com>": "low", "<c@example.com>": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
}

func TestBackend_List_UnconfiguredMailboxPinned_NoPriority(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{"Other": {msg("<a@example.com>", "Other", 1)}}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX", Priority: "high"}}})

	res, err := b.List(ctx, "Other", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].MailboxPriority != "" {
		t.Fatalf("res = %+v", res)
	}
	if len(ft.listCalls) != 1 || ft.listCalls[0].Mailbox != "Other" {
		t.Fatalf("listCalls = %+v, want exactly the pinned mailbox", ft.listCalls)
	}
}

// ----------------------------------------------------------------------
// Mailbox scope, degradation, duplicates, ordering, limits
// ----------------------------------------------------------------------

func TestBackend_List_MissingConfiguredMailbox_SkippedNotFatal(t *testing.T) {
	ft := &fakeTransport{
		lists:    map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1)}},
		listErrs: map[string]error{"Ghost": scriptout.WrapError(scriptout.ErrNotFound, "mailbox not found")},
	}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "Ghost"}, {Name: "INBOX"}}})

	res, err := b.List(ctx, "", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v (a missing configured mailbox must not fail the call)", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "<a@example.com>" {
		t.Fatalf("res = %+v, want INBOX's message still served", res)
	}
	if len(ft.listCalls) != 2 {
		t.Fatalf("listCalls = %+v, want both configured mailboxes queried", ft.listCalls)
	}
}

func TestBackend_Search_MissingConfiguredMailbox_SkippedNotFatal(t *testing.T) {
	ft := &fakeTransport{
		searches:   map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1)}},
		searchErrs: map[string]error{"Ghost": scriptout.WrapError(scriptout.ErrNotFound, "mailbox not found")},
	}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "Ghost"}, {Name: "INBOX"}}})

	res, err := b.Search(ctx, "invoice", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestBackend_List_PinnedMissingMailbox_NotFoundPassedThrough(t *testing.T) {
	ft := &fakeTransport{listErrs: map[string]error{"Ghost": scriptout.WrapError(scriptout.ErrNotFound, "mailbox not found")}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})

	_, err := b.List(ctx, "Ghost", false, 0, false)
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want not_found for an explicitly named missing mailbox", err)
	}
}

func TestBackend_List_OtherBridgeErrorsFailTheCall(t *testing.T) {
	for _, sentinel := range []error{scriptout.ErrUnavailable, scriptout.ErrUnauthenticated, scriptout.ErrInvalidArgument} {
		ft := &fakeTransport{listErrs: map[string]error{"INBOX": scriptout.WrapError(sentinel, "bridge said no")}}
		ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})
		_, err := newBackend(ft).List(ctx, "", false, 0, false)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	}
}

func TestBackend_List_NoConfiguredMailboxes_EmptyWithoutBridgeCall(t *testing.T) {
	ft := &fakeTransport{}
	res, err := newBackend(ft).List(context.Background(), "", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Entities) != 0 || len(res.PresentIDs) != 0 || res.Truncated {
		t.Fatalf("res = %+v, want empty", res)
	}
	if len(ft.listCalls) != 0 {
		t.Fatalf("listCalls = %+v, want none", ft.listCalls)
	}
}

func TestBackend_List_DuplicateMessageIDAcrossMailboxes_MergedEarliestConfiguredWins(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{
		"INBOX":   {msg("<dup@example.com>", "INBOX", 10), msg("<only-inbox@example.com>", "INBOX", 20)},
		"Mention": {msg("<dup@example.com>", "Mention", 10), msg("<only-mention@example.com>", "Mention", 5)},
	}}
	b := newBackend(ft)
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{
		{Name: "INBOX", Priority: "low"},
		{Name: "Mention", Priority: "high"},
	}})

	res, err := b.List(ctx, "", false, 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := ids(res.PresentIDs); got != "<only-mention@example.com>,<dup@example.com>,<only-inbox@example.com>" {
		t.Fatalf("PresentIDs = %s, want deduped and newest first", got)
	}
	for _, e := range res.Entities {
		if e.ID == "<dup@example.com>" && (e.Mailbox != "INBOX" || e.MailboxPriority != "low") {
			t.Fatalf("dup entity = %+v, want the earliest-configured mailbox (INBOX, low) to win", e)
		}
	}
}

func TestBackend_List_LimitDefaultsAndClamps(t *testing.T) {
	cases := []struct{ in, want int }{{0, defaultListLimit}, {7, 7}, {maxListLimit, maxListLimit}, {maxListLimit + 50, maxListLimit}}
	for _, c := range cases {
		ft := &fakeTransport{}
		ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})
		if _, err := newBackend(ft).List(ctx, "", false, c.in, false); err != nil {
			t.Fatalf("List(limit=%d): %v", c.in, err)
		}
		if len(ft.listCalls) != 1 || ft.listCalls[0].Limit != c.want {
			t.Fatalf("limit %d: bridge saw %+v, want Limit %d", c.in, ft.listCalls, c.want)
		}
	}
}

func TestBackend_List_PassesUnreadOnlyThrough(t *testing.T) {
	ft := &fakeTransport{}
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})
	if _, err := newBackend(ft).List(ctx, "", true, 0, false); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !ft.listCalls[0].UnreadOnly {
		t.Fatalf("listCalls = %+v, want UnreadOnly", ft.listCalls)
	}
}

func TestBackend_List_TruncatedWhenBridgeReplyFillsLimit(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1), msg("<b@example.com>", "INBOX", 2)}}}
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})

	res, err := newBackend(ft).List(ctx, "", false, 2, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true when the bridge reply filled the limit (more may exist)")
	}

	res, err = newBackend(ft).List(ctx, "", false, 3, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Truncated {
		t.Fatal("Truncated = true, want false when the reply is provably complete")
	}
}

func TestBackend_List_MergeCutToLimitIsTruncated(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{
		"INBOX":   {msg("<a@example.com>", "INBOX", 1)},
		"Mention": {msg("<b@example.com>", "Mention", 2)},
	}}
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}, {Name: "Mention"}}})

	res, err := newBackend(ft).List(ctx, "", false, 1, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := ids(res.PresentIDs); got != "<a@example.com>" || !res.Truncated {
		t.Fatalf("PresentIDs = %s Truncated = %v, want the newest only and Truncated", got, res.Truncated)
	}
}

// ----------------------------------------------------------------------
// Search (dedicated op and cross-cutting capability)
// ----------------------------------------------------------------------

func TestBackend_Search_QueriesEachConfiguredMailbox(t *testing.T) {
	ft := &fakeTransport{searches: map[string][]apiMessage{
		"INBOX":   {msg("<a@example.com>", "INBOX", 2)},
		"Mention": {msg("<b@example.com>", "Mention", 1)},
	}}
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}, {Name: "Mention"}}})

	res, err := newBackend(ft).Search(ctx, "invoice", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := ids(res.PresentIDs); got != "<b@example.com>,<a@example.com>" {
		t.Fatalf("PresentIDs = %s", got)
	}
	if len(ft.searchCalls) != 2 || ft.searchCalls[0].Query != "invoice" || ft.searchCalls[0].Mailbox != "INBOX" || ft.searchCalls[1].Mailbox != "Mention" {
		t.Fatalf("searchCalls = %+v", ft.searchCalls)
	}
}

func TestSearchProvider_ReturnsResults(t *testing.T) {
	ft := &fakeTransport{searches: map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1)}}}
	ctx := ctxWithConfig(t, backendConfig{Mailboxes: []mailboxConfig{{Name: "INBOX"}}})

	results, err := newBackend(ft).SearchProvider().Search(ctx, "invoice", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	r := results[0]
	if r.Type != "mail_message" || r.ID != "<a@example.com>" || r.Title != "subject of <a@example.com>" || r.Source != "pg-connector-mail-osx-bridge" {
		t.Fatalf("result = %+v", r)
	}
}

func TestSearchProvider_EmptyQuery_InvalidArgument(t *testing.T) {
	_, err := newBackend(&fakeTransport{}).SearchProvider().Search(context.Background(), "", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// ----------------------------------------------------------------------
// Attention (INV-MAIL-3 / INV-MAIL-4)
// ----------------------------------------------------------------------

func TestBackend_ListAttention_EmptyAndIgnoresMailboxPriorityAndImportantPeople(t *testing.T) {
	ft := &fakeTransport{lists: map[string][]apiMessage{"INBOX": {msg("<a@example.com>", "INBOX", 1)}}}
	ctx := ctxWithConfig(t, backendConfig{
		Mailboxes:       []mailboxConfig{{Name: "INBOX", Priority: "high"}},
		ImportantPeople: []string{"ada@example.com"},
	})

	items, err := newBackend(ft).ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want a non-nil empty list", items)
	}
	if len(ft.listCalls) != 0 {
		t.Fatalf("listCalls = %+v, want no bridge traffic for attention", ft.listCalls)
	}
}

// ----------------------------------------------------------------------
// State ops and attachment fetch
// ----------------------------------------------------------------------

func TestBackend_StateOps_CallTheMatchingTransportOp(t *testing.T) {
	ft := &fakeTransport{}
	b := newBackend(ft)
	ctx := context.Background()
	for _, fn := range []func(context.Context, string) error{b.MarkRead, b.MarkUnread, b.Archive, b.Unarchive} {
		if err := fn(ctx, "<a@example.com>"); err != nil {
			t.Fatalf("op: %v", err)
		}
	}
	want := []string{"mark-read:<a@example.com>", "mark-unread:<a@example.com>", "archive:<a@example.com>", "unarchive:<a@example.com>"}
	if !reflect.DeepEqual(ft.stateCalls, want) {
		t.Fatalf("stateCalls = %v, want %v", ft.stateCalls, want)
	}
}

func TestBackend_StateOps_NotFoundPassedThrough(t *testing.T) {
	ft := &fakeTransport{stateErr: scriptout.WrapError(scriptout.ErrNotFound, "no such message")}
	err := newBackend(ft).Archive(context.Background(), "<missing@example.com>")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestBackend_FetchAttachment_ReturnsFullPathAndEchoesIDs(t *testing.T) {
	ft := &fakeTransport{attachment: apiAttachmentFile{Path: "/data/attachments/report.pdf", Name: "report.pdf", Size: 2048}}

	got, err := newBackend(ft).FetchAttachment(context.Background(), "<a@example.com>", "att-1")
	if err != nil {
		t.Fatalf("FetchAttachment: %v", err)
	}
	if got.Path != "/data/attachments/report.pdf" || got.MessageID != "<a@example.com>" || got.AttachmentID != "att-1" {
		t.Fatalf("got = %+v", got)
	}
	if len(ft.attachCalls) != 1 || ft.attachCalls[0].MessageID != "<a@example.com>" || ft.attachCalls[0].AttachmentID != "att-1" {
		t.Fatalf("attachCalls = %+v", ft.attachCalls)
	}
}

func TestBackend_FetchAttachment_NotFoundPassedThrough(t *testing.T) {
	ft := &fakeTransport{attachErr: scriptout.WrapError(scriptout.ErrNotFound, "no such attachment")}
	_, err := newBackend(ft).FetchAttachment(context.Background(), "<a@example.com>", "nope")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// ----------------------------------------------------------------------
// Config decoding
// ----------------------------------------------------------------------

func TestDecodeBackendConfig_MalformedJSON_InvalidArgument(t *testing.T) {
	_, err := decodeBackendConfig(json.RawMessage(`{"mailboxes": "not a list"}`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestDecodeBackendConfig_Empty_ZeroValue(t *testing.T) {
	cfg, err := decodeBackendConfig(nil)
	if err != nil || len(cfg.Mailboxes) != 0 || len(cfg.ImportantPeople) != 0 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestDecodeBackendConfig_AcceptsSharedShape(t *testing.T) {
	cfg, err := decodeBackendConfig(json.RawMessage(`{"mailboxes":[{"name":"INBOX"},{"name":"Other","priority":"low"}],"important_people":["x@example.com"]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Mailboxes) != 2 || cfg.Mailboxes[1].Priority != "low" || len(cfg.ImportantPeople) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
}
