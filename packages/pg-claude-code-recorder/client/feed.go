package client

import (
	"context"
	"fmt"
)

// FeedOptions tune a Feed iterator.
type FeedOptions struct {
	// Project names the fields each record entry carries.
	Project []string
	// Limit is the page size (the daemon's default 1,000 when zero).
	Limit int
	// WaitMS is how long the daemon holds an empty feed request (30,000 when zero;
	// at most 60,000).
	WaitMS int
}

type recKey struct {
	file    string
	gen     int
	offset  int64
	item    int
	hasItem bool
}

// FeedIter tails the change feed. It applies a reset (dropping what it has seen
// of that file and generation) before de-duplicating by record id, reconnects
// with the same cursor after a daemon restart, and backs off on busy and
// unavailable. Delivery is at least once; the iterator removes the repeats it
// can see.
type FeedIter struct {
	c      *Client
	filter *Filter
	opt    FeedOptions
	cursor string
	buf    []Entry
	pageTo string
	back   Backoff
	seen   map[recKey]struct{}
	order  []recKey
}

const seenCap = 1 << 17

// Feed returns an iterator over the entries after the cursor (empty starts at
// the retention floor). A cursor below the floor, beyond the head or from an old
// epoch makes Next return an error matching ErrCursorExpired whose Error.Cursor
// is the cursor to resync from.
func (c *Client) Feed(ctx context.Context, filter *Filter, after string, opts ...FeedOptions) *FeedIter {
	it := &FeedIter{c: c, filter: filter, cursor: after, seen: map[recKey]struct{}{}}
	if len(opts) > 0 {
		it.opt = opts[0]
	}
	if it.opt.WaitMS <= 0 {
		it.opt.WaitMS = 30000
	}
	return it
}

// Cursor is the position to persist: resuming after it repeats nothing the
// caller has consumed and skips nothing it has not.
func (it *FeedIter) Cursor() string { return it.cursor }

func (it *FeedIter) remember(k recKey) bool {
	if _, ok := it.seen[k]; ok {
		return false
	}
	it.seen[k] = struct{}{}
	it.order = append(it.order, k)
	if len(it.order) > seenCap {
		drop := it.order[:seenCap/2]
		for _, d := range drop {
			delete(it.seen, d)
		}
		it.order = append([]recKey(nil), it.order[seenCap/2:]...)
	}
	return true
}

func (it *FeedIter) forget(file string, gen int) {
	kept := it.order[:0]
	for _, k := range it.order {
		if k.file == file && k.gen == gen {
			delete(it.seen, k)
			continue
		}
		kept = append(kept, k)
	}
	it.order = kept
}

// Next returns the next entry, waiting for one. A reset entry is returned as is
// (Kind "reset"): the caller drops its stored records of that file and
// generation. A repeated record is skipped.
func (it *FeedIter) Next(ctx context.Context) (Entry, error) {
	for {
		for len(it.buf) > 0 {
			en := it.buf[0]
			it.buf = it.buf[1:]
			if len(it.buf) == 0 {
				it.cursor = it.pageTo
			} else {
				it.cursor = en.Seq
			}
			switch en.Kind {
			case "reset":
				it.forget(en.File, en.Gen)
				return en, nil
			case "record":
				if en.Record == nil {
					continue
				}
				r := en.Record
				if !it.remember(recKey{file: r.File, gen: r.Gen, offset: r.Offset, item: r.Item, hasItem: true}) {
					continue
				}
				return en, nil
			default:
				return Entry{}, fmt.Errorf("recorder feed: unknown entry kind %q", en.Kind)
			}
		}
		ans, err := it.c.FeedPage(ctx, it.filter, it.cursor, it.opt.Project, it.opt.Limit, it.opt.WaitMS)
		if err != nil {
			if ctx.Err() != nil {
				return Entry{}, ctx.Err()
			}
			if Retryable(err) {
				if serr := it.c.opt.Sleep(ctx, it.back.Next()); serr != nil {
					return Entry{}, serr
				}
				continue
			}
			return Entry{}, err
		}
		it.back.Reset()
		if len(ans.Entries) == 0 {
			it.cursor = ans.NextCursor
			continue
		}
		it.buf, it.pageTo = ans.Entries, ans.NextCursor
		if ans.More {
			it.pageTo = ans.Entries[len(ans.Entries)-1].Seq
		}
	}
}
