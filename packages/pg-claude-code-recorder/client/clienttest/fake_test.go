package clienttest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

func TestFakeMeetsTheContract(t *testing.T) {
	clienttest.RunContract(t, func(*testing.T) client.API { return clienttest.NewFake() })
}

func TestFakeServesSeededFeedConversationsAndFailures(t *testing.T) {
	ctx := context.Background()
	f := clienttest.NewFake()
	f.Entries = []wire.Entry{
		{Kind: "record", Record: &wire.RecordRef{File: "f_1", Gen: 1, Offset: 0, Item: -1}},
		{Kind: "record", Record: &wire.RecordRef{File: "f_1", Gen: 1, Offset: 10, Item: -1}},
	}
	f.Conversations = []wire.Conversation{{SessionID: "s1", State: "open"}}
	it := f.Feed(ctx, nil, "")
	for _, off := range []int64{0, 10} {
		en, err := it.Next(ctx)
		if err != nil || en.Record.Offset != off {
			t.Fatalf("entry %+v err %v, want offset %d", en, err, off)
		}
	}
	if it.Cursor() != "1:2" {
		t.Fatalf("cursor %q", it.Cursor())
	}
	if c, err := f.Conversation(ctx, "s1"); err != nil || c.State != "open" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := f.FeedPage(ctx, nil, "1:3", nil, 0, 0); !errors.Is(err, client.ErrCursorExpired) {
		t.Fatalf("beyond head: %v", err)
	}
	f.Fail = map[string]error{"Query": client.NewError(wire.ErrBusy, "busy", "")}
	if _, err := f.Query(ctx, client.QueryRequest{}); !client.Retryable(err) {
		t.Fatalf("injected: %v", err)
	}
	if rows, err := f.Crashed(ctx, time.Time{}); err != nil || len(rows) != 0 {
		t.Fatalf("crashed %v %v", rows, err)
	}
}
