package client

import (
	"context"
	"time"
)

// API lists the methods of a recorder client. *Client implements it over the
// wire; clienttest.Fake implements it in memory, so a consumer can depend on API
// and test without a daemon. Every type it returns is a name from the wire
// package or the standard library.
type API interface {
	// Hello returns the daemon's identity; Capabilities and HasCapability read
	// what the last hello reported.
	Hello(ctx context.Context) (Hello, error)
	Capabilities() []string
	HasCapability(op string) bool

	FilesInventory(ctx context.Context, req InventoryRequest) (*InventoryAnswer, error)
	Query(ctx context.Context, req QueryRequest) (*QueryAnswer, error)
	Search(ctx context.Context, req SearchRequest) (*SearchAnswer, error)
	Snapshot(ctx context.Context, req SnapshotRequest) (*SnapshotAnswer, error)
	Raw(ctx context.Context, req RawRequest) (*RawAnswer, error)
	FeedPage(ctx context.Context, filter *Filter, after string, project []string, limit, waitMS int) (*FeedAnswer, error)
	Feed(ctx context.Context, filter *Filter, after string, opts ...FeedOptions) *FeedIter

	Conversation(ctx context.Context, id string) (*ConversationInfo, error)
	ConversationList(ctx context.Context, f ConversationFilter) (*ConversationListAnswer, error)
	Process(ctx context.Context, pid int64, procStart string) (*ProcessInfo, error)
	ProcessList(ctx context.Context, f ProcessFilter) (*ProcessListAnswer, error)
	Ended(ctx context.Context, q EndQuery) (*EndedResult, error)
	Crashed(ctx context.Context, since time.Time) ([]EndedConversation, error)
	SessionsFacts(ctx context.Context, ids, fields []string) (*SessionsFactsAnswer, error)
}

var _ API = (*Client)(nil)
