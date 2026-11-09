package client

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bounded applies the request timeout when the caller set no deadline.
func (c *Client) bounded(ctx context.Context, extra time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.opt.RequestTimeout+extra)
}

func (c *Client) post(ctx context.Context, op string, extra time.Duration, body, out any) error {
	ctx, cancel := c.bounded(ctx, extra)
	defer cancel()
	return convert(c.c.Post(ctx, op, body, out))
}

// Hello returns the daemon's identity: protocol_version, schema_version, epoch,
// the capabilities list, caught_up and degraded.
func (c *Client) Hello(ctx context.Context) (Hello, error) {
	ctx, cancel := c.bounded(ctx, 0)
	defer cancel()
	var h Hello
	if err := convert(c.c.Do(ctx, http.MethodGet, "/v1/hello", nil, &h)); err != nil {
		return Hello{}, err
	}
	c.hello = h
	return h, nil
}

// Capabilities returns what the last hello reported.
func (c *Client) Capabilities() []string { return c.hello.Capabilities }

// FilesInventory lists the files that have records with their path facts and
// newest record time, from the recorder's per-file table (no scan of records).
func (c *Client) FilesInventory(ctx context.Context, req InventoryRequest) (*InventoryAnswer, error) {
	var ans InventoryAnswer
	if err := c.post(ctx, "files.inventory", 0, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Query runs a query. A degraded answer (the recorder is catching up) is
// returned with a nil error; check QueryAnswer.Degraded.
func (c *Client) Query(ctx context.Context, req QueryRequest) (*QueryAnswer, error) {
	var ans QueryAnswer
	if err := c.post(ctx, "query", 0, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Search runs a word or substring search.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchAnswer, error) {
	var ans SearchAnswer
	if err := c.post(ctx, "search", 0, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Snapshot returns one page of the state at a cursor. Take pages until More is
// false, repeating the first answer's Cursor as At and sending NextPage as Page;
// then tail Feed from that Cursor.
func (c *Client) Snapshot(ctx context.Context, req SnapshotRequest) (*SnapshotAnswer, error) {
	var ans SnapshotAnswer
	if err := c.post(ctx, "snapshot", 0, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Raw returns the bytes of a record or a file range, from a copy or an archive.
func (c *Client) Raw(ctx context.Context, req RawRequest) (*RawAnswer, error) {
	var ans RawAnswer
	if err := c.post(ctx, "raw", 0, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// FeedPage asks the feed once (no iterator, no retry), waiting up to waitMS on
// the server. It is the building block of Feed.
func (c *Client) FeedPage(ctx context.Context, filter *Filter, after string, project []string, limit, waitMS int) (*FeedAnswer, error) {
	var ans FeedAnswer
	req := map[string]any{"after": after, "wait_ms": waitMS}
	if filter != nil {
		req["where"] = filter
	}
	if len(project) > 0 {
		req["project"] = project
	}
	if limit > 0 {
		req["limit"] = limit
	}
	if err := c.post(ctx, "feed", time.Duration(waitMS)*time.Millisecond, req, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Backoff is the retry guidance for busy and unavailable: it starts at 1 s and
// doubles to 30 s.
type Backoff struct{ next time.Duration }

// Next returns the delay before the next attempt.
func (b *Backoff) Next() time.Duration {
	switch {
	case b.next <= 0:
		b.next = time.Second
	case b.next < 30*time.Second:
		b.next *= 2
		if b.next > 30*time.Second {
			b.next = 30 * time.Second
		}
	}
	return b.next
}

// Reset starts the schedule over after a success.
func (b *Backoff) Reset() { b.next = 0 }

// Retryable reports whether err is one the retry guidance backs off on: busy,
// unavailable or a daemon that is down (a restart).
func Retryable(err error) bool {
	// errors.As, not a type assertion: a caller that wraps the error with %w
	// (a snapshot deadline, say) must still be told to poll again later.
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Name {
	case wire.ErrBusy, wire.ErrUnavailable, wire.ErrDaemonOffline, wire.ErrQueryTimeout:
		return true
	}
	return false
}
