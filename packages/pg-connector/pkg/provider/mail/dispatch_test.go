package mail

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeProvider is a mock Provider used to assert (a) that it satisfies the
// Provider interface's method set (a compile-time assertion, below), and
// (b) that NewDispatchTable wires each op to the right method and passes
// args/results/errors straight through — mirrors
// pkg/provider/calendar/dispatch_test.go's identical fakeProvider
// convention. It records the last call of each mutating method.
type fakeProvider struct {
	listFn            func(ctx context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error)
	showFn            func(ctx context.Context, id string) (*schema.MailMessage, error)
	searchFn          func(ctx context.Context, query, mailbox string, limit int) (*schema.MailListResult, error)
	fetchAttachmentFn func(ctx context.Context, messageID, attachmentID string) (*schema.MailAttachmentFile, error)
	// mutateFn is invoked by MarkRead/MarkUnread/Archive/Unarchive with
	// the method's own name and the id it was called with.
	mutateFn func(method, id string) error
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) List(ctx context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error) {
	return f.listFn(ctx, mailbox, unreadOnly, limit, idsOnly)
}

func (f *fakeProvider) Show(ctx context.Context, id string) (*schema.MailMessage, error) {
	return f.showFn(ctx, id)
}

func (f *fakeProvider) Search(ctx context.Context, query, mailbox string, limit int) (*schema.MailListResult, error) {
	return f.searchFn(ctx, query, mailbox, limit)
}

func (f *fakeProvider) MarkRead(_ context.Context, id string) error {
	return f.mutateFn("MarkRead", id)
}

func (f *fakeProvider) MarkUnread(_ context.Context, id string) error {
	return f.mutateFn("MarkUnread", id)
}

func (f *fakeProvider) Archive(_ context.Context, id string) error {
	return f.mutateFn("Archive", id)
}

func (f *fakeProvider) Unarchive(_ context.Context, id string) error {
	return f.mutateFn("Unarchive", id)
}

func (f *fakeProvider) FetchAttachment(ctx context.Context, messageID, attachmentID string) (*schema.MailAttachmentFile, error) {
	return f.fetchAttachmentFn(ctx, messageID, attachmentID)
}

// fakeProviderWithAuth additionally implements pkg/provider.AuthChecker, to
// exercise NewDispatchTable's type-check-asserted auth_status entry.
type fakeProviderWithAuth struct {
	fakeProvider
	checkAuthFn func(ctx context.Context) error
}

func (f *fakeProviderWithAuth) CheckAuth(ctx context.Context) error {
	return f.checkAuthFn(ctx)
}

// TestProviderInterface_HasExactlyTheDesignOpSet is the structural half of
// the no-delete guarantee (invariant INV-MAIL-1): the interface's method
// set is EXACTLY the design's op set — no delete-shaped method, no
// create/reply method.
func TestProviderInterface_HasExactlyTheDesignOpSet(t *testing.T) {
	typ := reflect.TypeOf((*Provider)(nil)).Elem()
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	want := []string{"Archive", "FetchAttachment", "List", "MarkRead", "MarkUnread", "Search", "Show", "Unarchive"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Provider method set = %v, want exactly %v", got, want)
	}
}

func TestNewDispatchTable_List_DecodesArgsAndInvokesProvider(t *testing.T) {
	var gotMailbox string
	var gotUnread, gotIDsOnly bool
	var gotLimit int
	p := &fakeProvider{
		listFn: func(_ context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error) {
			gotMailbox, gotUnread, gotLimit, gotIDsOnly = mailbox, unreadOnly, limit, idsOnly
			return &schema.MailListResult{Entities: []schema.MailMessage{{ID: "<m1@example.invalid>"}}, PresentIDs: []string{"<m1@example.invalid>"}, Truncated: true}, nil
		},
	}
	entry, ok := NewDispatchTable(p)["list"]
	if !ok {
		t.Fatal(`table["list"] missing`)
	}
	if entry.SchemaVersion != schema.MailSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", entry.SchemaVersion, schema.MailSchemaVersion)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"mailbox":"INBOX","unread_only":true,"limit":25,"ids_only":true}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if gotMailbox != "INBOX" || !gotUnread || gotLimit != 25 || !gotIDsOnly {
		t.Fatalf("mailbox=%q unread=%v limit=%d idsOnly=%v", gotMailbox, gotUnread, gotLimit, gotIDsOnly)
	}
	got, ok := result.(*schema.MailListResult)
	if !ok || len(got.PresentIDs) != 1 || got.PresentIDs[0] != "<m1@example.invalid>" || !got.Truncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_List_OmittedArgsMeanDefaults(t *testing.T) {
	called := false
	p := &fakeProvider{
		listFn: func(_ context.Context, mailbox string, unreadOnly bool, limit int, idsOnly bool) (*schema.MailListResult, error) {
			called = true
			if mailbox != "" || unreadOnly || limit != 0 || idsOnly {
				t.Fatalf("mailbox=%q unread=%v limit=%d idsOnly=%v, want all zero values", mailbox, unreadOnly, limit, idsOnly)
			}
			return &schema.MailListResult{}, nil
		},
	}
	if _, err := NewDispatchTable(p)["list"].Handle(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Fatal("List was not invoked")
	}
}

func TestNewDispatchTable_List_NegativeLimitIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listFn: func(context.Context, string, bool, int, bool) (*schema.MailListResult, error) {
			t.Fatal("List must not be invoked for a negative limit")
			return nil, nil
		},
	}
	_, err := NewDispatchTable(p)["list"].Handle(context.Background(), json.RawMessage(`{"limit":-1}`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_List_DecodeFailureIsInvalidArgument(t *testing.T) {
	p := &fakeProvider{
		listFn: func(context.Context, string, bool, int, bool) (*schema.MailListResult, error) {
			t.Fatal("List must not be invoked when args fail to decode")
			return nil, nil
		},
	}
	_, err := NewDispatchTable(p)["list"].Handle(context.Background(), json.RawMessage(`{not valid json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestNewDispatchTable_List_UnavailablePassesThroughUnwrapped(t *testing.T) {
	p := &fakeProvider{
		listFn: func(context.Context, string, bool, int, bool) (*schema.MailListResult, error) {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "bridge down")
		},
	}
	_, err := NewDispatchTable(p)["list"].Handle(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestNewDispatchTable_Show_DecodesIDAndInvokesProvider(t *testing.T) {
	var gotID string
	p := &fakeProvider{
		showFn: func(_ context.Context, id string) (*schema.MailMessage, error) {
			gotID = id
			return &schema.MailMessage{ID: id, Subject: "hello"}, nil
		},
	}
	entry, ok := NewDispatchTable(p)["show"]
	if !ok {
		t.Fatal(`table["show"] missing`)
	}
	if entry.SchemaVersion != schema.MailSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", entry.SchemaVersion, schema.MailSchemaVersion)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"<m1@example.invalid>"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.MailMessage)
	if gotID != "<m1@example.invalid>" || !ok || got.Subject != "hello" {
		t.Fatalf("gotID=%q result=%#v", gotID, result)
	}
}

func TestNewDispatchTable_Show_NotFoundPassesThroughUnwrapped(t *testing.T) {
	p := &fakeProvider{
		showFn: func(context.Context, string) (*schema.MailMessage, error) {
			return nil, scriptout.WrapError(scriptout.ErrNotFound, "no such message")
		},
	}
	_, err := NewDispatchTable(p)["show"].Handle(context.Background(), json.RawMessage(`{"id":"<x@example.invalid>"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestNewDispatchTable_Search_DecodesArgsAndInvokesProvider(t *testing.T) {
	var gotQuery, gotMailbox string
	var gotLimit int
	p := &fakeProvider{
		searchFn: func(_ context.Context, query, mailbox string, limit int) (*schema.MailListResult, error) {
			gotQuery, gotMailbox, gotLimit = query, mailbox, limit
			return &schema.MailListResult{PresentIDs: []string{"<m1@example.invalid>"}}, nil
		},
	}
	table := NewDispatchTable(p)
	if _, ok := table["search"]; ok {
		t.Fatal(`table["search"] present; the mail search op MUST be "search_messages" so it cannot collide with the cross-cutting search capability's own "search" op`)
	}
	entry, ok := table["search_messages"]
	if !ok {
		t.Fatal(`table["search_messages"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"query":"invoice","mailbox":"INBOX","limit":10}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if gotQuery != "invoice" || gotMailbox != "INBOX" || gotLimit != 10 {
		t.Fatalf("query=%q mailbox=%q limit=%d", gotQuery, gotMailbox, gotLimit)
	}
	if got, ok := result.(*schema.MailListResult); !ok || len(got.PresentIDs) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDispatchTable_Search_InvalidArgs(t *testing.T) {
	p := &fakeProvider{
		searchFn: func(context.Context, string, string, int) (*schema.MailListResult, error) {
			t.Fatal("Search must not be invoked for invalid args")
			return nil, nil
		},
	}
	handle := NewDispatchTable(p)["search_messages"].Handle
	for name, args := range map[string]string{
		"malformed json": `{not valid json`,
		"empty query":    `{"query":""}`,
		"missing query":  `{}`,
		"negative limit": `{"query":"x","limit":-3}`,
	} {
		if _, err := handle(context.Background(), json.RawMessage(args)); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Fatalf("%s: err = %v, want errors.Is(err, ErrInvalidArgument)", name, err)
		}
	}
}

// TestNewDispatchTable_MutatingOps_InvokeMatchingMethodWithNoPayload
// exercises every id-keyed mutation: the right Provider method is invoked
// with the decoded id, the reply payload is nil, and errors pass through
// unwrapped.
func TestNewDispatchTable_MutatingOps_InvokeMatchingMethodWithNoPayload(t *testing.T) {
	ops := map[string]string{
		"mark_read":   "MarkRead",
		"mark_unread": "MarkUnread",
		"archive":     "Archive",
		"unarchive":   "Unarchive",
	}
	for op, method := range ops {
		t.Run(op, func(t *testing.T) {
			var gotMethod, gotID string
			p := &fakeProvider{mutateFn: func(m, id string) error {
				gotMethod, gotID = m, id
				return nil
			}}
			entry, ok := NewDispatchTable(p)[op]
			if !ok {
				t.Fatalf("table[%q] missing", op)
			}
			if entry.SchemaVersion != schema.MailSchemaVersion {
				t.Fatalf("SchemaVersion = %d, want %d", entry.SchemaVersion, schema.MailSchemaVersion)
			}
			result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"<m1@example.invalid>"}`))
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if result != nil {
				t.Fatalf("result = %#v, want nil (no result payload)", result)
			}
			if gotMethod != method || gotID != "<m1@example.invalid>" {
				t.Fatalf("method=%q id=%q, want method=%q id=%q", gotMethod, gotID, method, "<m1@example.invalid>")
			}
		})
	}
}

func TestNewDispatchTable_MutatingOps_ErrorPathsAndInvalidArgs(t *testing.T) {
	for _, op := range []string{"mark_read", "mark_unread", "archive", "unarchive"} {
		t.Run(op, func(t *testing.T) {
			p := &fakeProvider{mutateFn: func(string, string) error {
				return scriptout.WrapError(scriptout.ErrNotFound, "no such message")
			}}
			handle := NewDispatchTable(p)[op].Handle
			if _, err := handle(context.Background(), json.RawMessage(`{"id":"<x@example.invalid>"}`)); !errors.Is(err, scriptout.ErrNotFound) {
				t.Fatalf("provider error: err = %v, want errors.Is(err, ErrNotFound)", err)
			}
			called := false
			p.mutateFn = func(string, string) error { called = true; return nil }
			for name, args := range map[string]string{
				"malformed json": `{not valid json`,
				"empty id":       `{"id":""}`,
				"missing id":     `{}`,
			} {
				if _, err := handle(context.Background(), json.RawMessage(args)); !errors.Is(err, scriptout.ErrInvalidArgument) {
					t.Fatalf("%s: err = %v, want errors.Is(err, ErrInvalidArgument)", name, err)
				}
			}
			if called {
				t.Fatal("the Provider must not be invoked for invalid args")
			}
		})
	}
}

func TestNewDispatchTable_FetchAttachment_DecodesArgsAndReturnsPath(t *testing.T) {
	var gotMsg, gotAtt string
	p := &fakeProvider{
		fetchAttachmentFn: func(_ context.Context, messageID, attachmentID string) (*schema.MailAttachmentFile, error) {
			gotMsg, gotAtt = messageID, attachmentID
			return &schema.MailAttachmentFile{MessageID: messageID, AttachmentID: attachmentID, Path: "/tmp/example/invoice.pdf"}, nil
		},
	}
	entry, ok := NewDispatchTable(p)["fetch_attachment"]
	if !ok {
		t.Fatal(`table["fetch_attachment"] missing`)
	}
	result, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"<m1@example.invalid>","attachment_id":"att-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.MailAttachmentFile)
	if gotMsg != "<m1@example.invalid>" || gotAtt != "att-1" || !ok || got.Path != "/tmp/example/invoice.pdf" {
		t.Fatalf("gotMsg=%q gotAtt=%q result=%#v", gotMsg, gotAtt, result)
	}
}

func TestNewDispatchTable_FetchAttachment_InvalidArgsAndPassthrough(t *testing.T) {
	called := false
	p := &fakeProvider{
		fetchAttachmentFn: func(context.Context, string, string) (*schema.MailAttachmentFile, error) {
			called = true
			return nil, scriptout.WrapError(scriptout.ErrNotFound, "no such attachment")
		},
	}
	handle := NewDispatchTable(p)["fetch_attachment"].Handle
	for name, args := range map[string]string{
		"malformed json":        `{not valid json`,
		"missing attachment id": `{"id":"<m1@example.invalid>"}`,
		"missing id":            `{"attachment_id":"att-1"}`,
	} {
		if _, err := handle(context.Background(), json.RawMessage(args)); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Fatalf("%s: err = %v, want errors.Is(err, ErrInvalidArgument)", name, err)
		}
	}
	if called {
		t.Fatal("the Provider must not be invoked for invalid args")
	}
	if _, err := handle(context.Background(), json.RawMessage(`{"id":"<m1@example.invalid>","attachment_id":"nope"}`)); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestNewDispatchTable_ExactlyTheDesignOps_NoDeleteShapedOp is the
// wire-level half of the no-delete guarantee (invariant INV-MAIL-1): the
// table registers exactly the design's eight ops, and no op whose name is
// delete-shaped (delete/remove/trash/expunge/purge/destroy/erase) nor any
// create/reply op.
func TestNewDispatchTable_ExactlyTheDesignOps_NoDeleteShapedOp(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	var got []string
	for op := range table {
		got = append(got, op)
	}
	sort.Strings(got)
	want := []string{"archive", "fetch_attachment", "list", "mark_read", "mark_unread", "search_messages", "show", "unarchive"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered ops = %v, want exactly %v", got, want)
	}
	for op := range table {
		for _, bad := range []string{"delete", "remove", "trash", "expunge", "purge", "destroy", "erase", "create", "reply", "send"} {
			if strings.Contains(op, bad) {
				t.Fatalf("op %q is delete/create/reply-shaped (%q); the mail capability MUST NOT expose it", op, bad)
			}
		}
	}
}

func TestNewDispatchTable_AuthStatusAbsentWithoutAuthChecker(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	if _, ok := table[scriptout.OpAuthStatus]; ok {
		t.Fatal("auth_status entry present for a Provider not implementing AuthChecker")
	}
}

func TestNewDispatchTable_AuthStatusPresentWithAuthChecker_OK(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(context.Context) error { return nil }}
	table := NewDispatchTable(p)
	entry, ok := table[scriptout.OpAuthStatus]
	if !ok {
		t.Fatal("auth_status entry missing for a Provider implementing AuthChecker")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	status, ok := result.(scriptout.AuthStatus)
	if !ok || status.State != scriptout.AuthOK {
		t.Fatalf("result = %#v", result)
	}
	if len(table) != 9 {
		t.Fatalf("len(table) = %d, want 9 (eight design ops plus auth_status)", len(table))
	}
}

func TestNewDispatchTable_AuthStatusPresentWithAuthChecker_Failure(t *testing.T) {
	p := &fakeProviderWithAuth{checkAuthFn: func(context.Context) error { return errors.New("no automation grant") }}
	result, err := NewDispatchTable(p)[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("auth_status must answer with a well-formed result, not a wire error: %v", err)
	}
	status, ok := result.(scriptout.AuthStatus)
	if !ok || status.State == scriptout.AuthOK || status.Detail == "" {
		t.Fatalf("result = %#v", result)
	}
}
