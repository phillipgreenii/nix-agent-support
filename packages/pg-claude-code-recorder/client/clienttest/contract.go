package clienttest

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// RunContract is the table-driven contract of client.API over an empty recorder:
// the Fake runs it in the unit tier and the real daemon runs it in the
// integration tier, so both answer the same way. newAPI returns a client.API of
// an empty recorder for one case.
func RunContract(t *testing.T, newAPI func(t *testing.T) client.API) {
	t.Helper()
	ctx := context.Background()
	isErr := func(sentinel error) func(*testing.T, error) {
		return func(t *testing.T, err error) {
			t.Helper()
			if !errors.Is(err, sentinel) {
				t.Fatalf("err = %v, want %v", err, sentinel)
			}
		}
	}
	ok := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		do   func(t *testing.T, a client.API) error
		want func(*testing.T, error)
	}{
		{"hello speaks the protocol and lists operations", func(t *testing.T, a client.API) error {
			h, err := a.Hello(ctx)
			if err == nil && (h.ProtocolVersion != wire.ProtocolVersion || !a.HasCapability("query") || len(a.Capabilities()) == 0) {
				t.Fatalf("hello %+v caps %v", h, a.Capabilities())
			}
			return err
		}, ok},
		{"unknown conversation is not found", func(t *testing.T, a client.API) error {
			_, err := a.Conversation(ctx, "nope")
			return err
		}, isErr(client.ErrNotFound)},
		{"unknown process is not found", func(t *testing.T, a client.API) error {
			_, err := a.Process(ctx, 999999, "")
			return err
		}, isErr(client.ErrNotFound)},
		{"unknown raw record is not found", func(t *testing.T, a client.API) error {
			_, err := a.Raw(ctx, wire.RawRequest{Record: &wire.RecordRef{File: "f_999", Gen: 1, Item: -1}})
			return err
		}, isErr(client.ErrNotFound)},
		{"unknown filter field is a bad request", func(t *testing.T, a client.API) error {
			yes := true
			_, err := a.Query(ctx, client.QueryRequest{Where: &client.Filter{Field: "nope", Exists: &yes}})
			return err
		}, isErr(client.ErrBadRequest)},
		{"a cursor of another epoch is expired", func(t *testing.T, a client.API) error {
			_, err := a.FeedPage(ctx, nil, "99:0", nil, 0, 0)
			return err
		}, isErr(client.ErrCursorExpired)},
		{"an empty conversation list has no rows", func(t *testing.T, a client.API) error {
			l, err := a.ConversationList(ctx, client.ConversationFilter{})
			if err == nil && len(l.Conversations) != 0 {
				t.Fatalf("rows %v", l.Conversations)
			}
			return err
		}, ok},
		{"an empty feed page has a cursor to resume from", func(t *testing.T, a client.API) error {
			p, err := a.FeedPage(ctx, nil, "", nil, 0, 0)
			if err == nil && (len(p.Entries) != 0 || p.NextCursor == "") {
				t.Fatalf("page %+v", p)
			}
			return err
		}, ok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t)
			if _, err := a.Hello(ctx); err != nil {
				t.Fatal(err)
			}
			tc.want(t, tc.do(t, a))
		})
	}
}
