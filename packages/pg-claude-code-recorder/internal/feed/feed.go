// Package feed implements the recorder's change feed, snapshot and raw
// operations: the cursor and epoch rules, the retention floor, resets as their
// own sequenced entries, and the optional wait. It reads only committed
// transactions of the parsed records.
package feed

import (
	"context"
	"database/sql"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Hard limits of the wire contract.
const (
	// MaxWaitMS is the longest wait_ms the protocol allows.
	MaxWaitMS = 60000
	// MaxRawBytes is the most a raw file range returns.
	MaxRawBytes = 8 << 20
	// DefaultRetention is feed.retention's default (14 days of commit time).
	DefaultRetention = 14 * 24 * time.Hour
	// DefaultMaxWaiters is wait.maxWaiters' default.
	DefaultMaxWaiters = 256
)

// Request is the feed operation's body.
type Request struct {
	Op      string        `json:"op,omitempty"`
	After   string        `json:"after,omitempty"`
	Where   *query.Filter `json:"where,omitempty"`
	Project []string      `json:"project,omitempty"`
	WaitMS  int           `json:"wait_ms,omitempty"`
	Limit   int           `json:"limit,omitempty"`
}

// Entry is one feed entry: a reset or a record.
type Entry struct {
	Seq    string           `json:"seq"`
	Kind   string           `json:"kind"` // reset or record
	Record *query.RecordRef `json:"record,omitempty"`
	Fields map[string]any   `json:"fields,omitempty"`
	// A reset entry names the file and generation whose records are replaced.
	File   string `json:"file,omitempty"`
	Gen    int    `json:"gen,omitempty"`
	Parser string `json:"parser,omitempty"`
}

// Answer is the feed answer.
type Answer struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor"`
	More       bool    `json:"more"`
	TimedOut   bool    `json:"timed_out"`
	Degraded   bool    `json:"degraded,omitempty"`
}

// SnapshotRequest is the snapshot operation's body. The first page is asked
// without At or Page; later pages repeat the answer's Cursor as At and send
// NextPage as Page.
type SnapshotRequest struct {
	Op      string        `json:"op,omitempty"`
	Where   *query.Filter `json:"where,omitempty"`
	Project []string      `json:"project,omitempty"`
	Limit   int           `json:"limit,omitempty"`
	At      string        `json:"at,omitempty"`
	Page    string        `json:"page,omitempty"`
}

// SnapshotAnswer is the snapshot answer: the cursor to tail the feed from and a
// page of the records current at it.
type SnapshotAnswer struct {
	Cursor   string  `json:"cursor"`
	Entries  []Entry `json:"entries"`
	More     bool    `json:"more"`
	NextPage string  `json:"next_page,omitempty"`
	Degraded bool    `json:"degraded,omitempty"`
}

// RawRequest names a record, or a file range, whose bytes to read.
type RawRequest struct {
	Op     string           `json:"op,omitempty"`
	Record *query.RecordRef `json:"record,omitempty"`
	File   string           `json:"file,omitempty"`
	Gen    int              `json:"gen,omitempty"`
	Offset int64            `json:"offset,omitempty"`
	Length int              `json:"length,omitempty"`
}

// RawAnswer holds the bytes: Data is the text when they are valid UTF-8
// (Encoding "utf8"), else base64 (Encoding "base64").
type RawAnswer struct {
	File     string `json:"file"`
	Gen      int    `json:"gen"`
	Offset   int64  `json:"offset"`
	Length   int    `json:"length"`
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
}

// Engine answers feed, snapshot and raw.
type Engine struct {
	Q     *query.Engine
	Store *store.Store
	Clock clock.Clock
	Obs   *obs.Obs
	// Retention is feed.retention; WaitMax is wait.max (capped at 60 s);
	// MaxWaiters is wait.maxWaiters.
	Retention  time.Duration
	WaitMax    time.Duration
	MaxWaiters int
	// Behind reports that the recorder is catching up.
	Behind func() bool

	mu      sync.Mutex
	waiters int
	ch      chan struct{}
}

func (e *Engine) init() {
	if e.Clock == nil {
		e.Clock = clock.Real{}
	}
	if e.Retention <= 0 {
		e.Retention = DefaultRetention
	}
	if e.MaxWaiters <= 0 {
		e.MaxWaiters = DefaultMaxWaiters
	}
	if e.ch == nil {
		e.ch = make(chan struct{})
	}
}

// Notify wakes every waiter to re-check its filter (a parse commit happened).
// It never blocks.
func (e *Engine) Notify() {
	e.mu.Lock()
	e.init()
	close(e.ch)
	e.ch = make(chan struct{})
	e.mu.Unlock()
}

func (e *Engine) wakeCh() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	return e.ch
}

// Waiters reports how many waits are in progress.
func (e *Engine) Waiters() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiters
}

func (e *Engine) admit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if e.waiters >= e.MaxWaiters {
		return false
	}
	e.waiters++
	if e.Obs != nil {
		e.Obs.Reg.Set(obs.MFeedWaiters, nil, float64(e.waiters))
	}
	return true
}

func (e *Engine) release() {
	e.mu.Lock()
	e.waiters--
	if e.Obs != nil {
		e.Obs.Reg.Set(obs.MFeedWaiters, nil, float64(e.waiters))
	}
	e.mu.Unlock()
}

func bad(msg string) *wire.Error { return wire.Errf(wire.ErrBadRequest, msg) }

func (e *Engine) behind() bool { return e.Behind != nil && e.Behind() }

// floor returns the retention floor as a sequence: the greatest record id
// committed before now minus the retention, doubled.
func (e *Engine) floor(tx store.Querier) int64 {
	e.init()
	cutoff := e.Clock.Now().Add(-e.Retention).UnixNano()
	var rec int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(rec_id),0) FROM feed_marks WHERE at_ns < ?`, cutoff).Scan(&rec)
	return query.RecSeq(rec)
}

func expired(epoch, head int64, why string) *wire.Error {
	err := wire.Errf(wire.ErrCursorExpired, why+"; take a snapshot and tail the feed from its cursor")
	err.Cursor = query.Cursor(epoch, head)
	return err
}

// Feed answers one feed request, waiting up to wait_ms for a match when none is
// ready.
func (e *Engine) Feed(ctx context.Context, body []byte) (any, *wire.Error) {
	var req Request
	if err := query.Decode(body, &req); err != nil {
		return nil, err
	}
	e.init()
	if req.WaitMS < 0 {
		return nil, bad("wait_ms must not be negative")
	}
	maxWait := int(e.WaitMax / time.Millisecond)
	if maxWait <= 0 || maxWait > MaxWaitMS {
		maxWait = MaxWaitMS
	}
	if req.WaitMS > maxWait {
		return nil, bad("wait_ms is at most " + strconv.Itoa(maxWait))
	}
	var timer <-chan time.Time
	if req.WaitMS > 0 {
		timer = e.Clock.After(time.Duration(req.WaitMS) * time.Millisecond)
	}
	admitted := false
	defer func() {
		if admitted {
			e.release()
		}
	}()
	for {
		wake := e.wakeCh()
		ans, werr := e.once(ctx, req)
		if werr != nil {
			return nil, werr
		}
		if len(ans.Entries) > 0 || req.WaitMS == 0 {
			return ans, nil
		}
		if !admitted {
			if !e.admit() {
				return nil, wire.Errf(wire.ErrBusy, "the daemon already holds its limit of waiters; retry later")
			}
			admitted = true
		}
		select {
		case <-wake:
		case <-timer:
			ans.TimedOut = true
			return ans, nil
		case <-ctx.Done():
			return nil, wire.Errf(wire.ErrUnavailable, "the request ended before the wait did")
		}
	}
}

func (e *Engine) once(ctx context.Context, req Request) (*Answer, *wire.Error) {
	dbtx, err := e.Q.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = dbtx.Rollback() }()
	tx := store.BindCtx(ctx, dbtx) // every statement runs under the request ctx
	epoch, head, maxRec := query.Head(tx)
	floor := e.floor(tx)
	afterSeq := floor
	if req.After != "" && req.After != "0" {
		ce, cs, ok := query.ParseCursor(req.After)
		if !ok {
			return nil, bad("after is not a cursor of the form <epoch>:<sequence>")
		}
		switch {
		case ce != epoch:
			return nil, expired(epoch, head, "the cursor is from epoch "+strconv.FormatInt(ce, 10)+", the daemon is on "+strconv.FormatInt(epoch, 10))
		case cs < floor:
			return nil, expired(epoch, head, "the cursor is older than the feed retention")
		case cs > head:
			return nil, expired(epoch, head, "the cursor is beyond the head of the feed")
		}
		afterSeq = cs
	}
	limit := query.LimitOf(req.Limit)
	recs, werr := e.Q.Select(tx, query.Selection{
		Where: req.Where, Project: req.Project, AfterID: afterSeq / 2, UpToID: maxRec, Limit: limit,
	})
	if werr != nil {
		return nil, werr
	}
	bound := head
	if len(recs) > limit {
		bound = query.RecSeq(recs[limit].ID) - 1
		recs = recs[:limit]
	}
	resets, werr := e.resets(tx, req.Where, afterSeq, bound, epoch)
	if werr != nil {
		return nil, werr
	}
	entries := make([]Entry, 0, len(recs)+len(resets))
	entries = append(entries, resets...)
	for _, r := range recs {
		ref := r.Ref
		entries = append(entries, Entry{Seq: query.Cursor(epoch, query.RecSeq(r.ID)), Kind: "record", Record: &ref, Fields: r.Fields})
	}
	sort.SliceStable(entries, func(i, j int) bool { return seqOf(entries[i]) < seqOf(entries[j]) })
	ans := &Answer{Entries: entries, Degraded: e.behind()}
	if len(entries) > limit {
		entries = entries[:limit]
		ans.Entries, ans.More = entries, true
	}
	if len(recs) == limit && bound != head {
		ans.More = true
	}
	if ans.More && len(entries) > 0 {
		ans.NextCursor = entries[len(entries)-1].Seq
	} else {
		ans.NextCursor = query.Cursor(epoch, head)
	}
	return ans, nil
}

func seqOf(en Entry) int64 {
	_, s, _ := query.ParseCursor(en.Seq)
	return s
}

// resets lists the reset entries in (after, bound] whose file could hold a
// record matching the filter.
func (e *Engine) resets(tx store.Querier, where *query.Filter, after, bound, epoch int64) ([]Entry, *wire.Error) {
	rs, err := tx.Query(`SELECT p.file_id,p.gen,p.parser,p.next_rec,COALESCE(f.source,'') FROM parse_resets p LEFT JOIN files f ON f.id=p.file_id
WHERE 2*p.next_rec-1 > ? AND 2*p.next_rec-1 <= ? ORDER BY p.next_rec,p.id`, after, bound)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	type row struct {
		file, next  int64
		gen         int
		parser, src string
	}
	var all []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.file, &r.gen, &r.parser, &r.next, &r.src); err != nil {
			_ = rs.Close()
			return nil, wire.FromErr(err)
		}
		all = append(all, r)
	}
	_ = rs.Close()
	var out []Entry
	for _, r := range all {
		if where != nil {
			types := map[string]bool{}
			trs, err := tx.Query(`SELECT DISTINCT type FROM records WHERE file_id=?`, r.file)
			if err == nil {
				for trs.Next() {
					var t string
					if trs.Scan(&t) == nil {
						types[t] = true
					}
				}
				_ = trs.Close()
			}
			if !query.CouldMatch(where, r.src, types) {
				continue
			}
		}
		out = append(out, Entry{Seq: query.Cursor(epoch, query.ResetSeq(r.next)), Kind: "reset", File: query.FileRef(r.file), Gen: r.gen, Parser: r.parser})
	}
	return out, nil
}

// Snapshot answers one page of the state at a cursor.
func (e *Engine) Snapshot(ctx context.Context, body []byte) (any, *wire.Error) {
	var req SnapshotRequest
	if err := query.Decode(body, &req); err != nil {
		return nil, err
	}
	dbtx, err := e.Q.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = dbtx.Rollback() }()
	tx := store.BindCtx(ctx, dbtx) // every statement runs under the request ctx
	epoch, head, maxRec := query.Head(tx)
	bound := maxRec
	if req.At != "" {
		ce, cs, ok := query.ParseCursor(req.At)
		if !ok {
			return nil, bad("at is not a cursor of the form <epoch>:<sequence>")
		}
		if ce != epoch {
			return nil, expired(epoch, head, "the snapshot cursor is from an earlier epoch")
		}
		bound = cs / 2
		if bound > maxRec {
			return nil, expired(epoch, head, "the snapshot cursor is beyond the head of the feed")
		}
	}
	var after int64
	if req.Page != "" {
		n, perr := strconv.ParseInt(strings.TrimPrefix(req.Page, "r"), 10, 64)
		if perr != nil || !strings.HasPrefix(req.Page, "r") {
			return nil, bad("page is not a snapshot page token")
		}
		after = n
	}
	limit := query.LimitOf(req.Limit)
	recs, werr := e.Q.Select(tx, query.Selection{
		Where: req.Where, Project: req.Project, AfterID: after, UpToID: bound, Limit: limit, CurGenOnly: true,
	})
	if werr != nil {
		return nil, werr
	}
	ans := &SnapshotAnswer{Cursor: query.Cursor(epoch, query.RecSeq(bound)), Entries: []Entry{}, Degraded: e.behind()}
	if len(recs) > limit {
		ans.More = true
		recs = recs[:limit]
		ans.NextPage = "r" + strconv.FormatInt(recs[len(recs)-1].ID, 10)
	}
	for _, r := range recs {
		ref := r.Ref
		ans.Entries = append(ans.Entries, Entry{Seq: query.Cursor(epoch, query.RecSeq(r.ID)), Kind: "record", Record: &ref, Fields: r.Fields})
	}
	return ans, nil
}

// Raw answers the bytes of a record or a file range through the one pointer
// abstraction over copies and archives.
func (e *Engine) Raw(ctx context.Context, body []byte) (any, *wire.Error) {
	var req RawRequest
	if err := query.Decode(body, &req); err != nil {
		return nil, err
	}
	var file int64
	var gen, n int
	var off int64
	switch {
	case req.Record != nil:
		f, ok := query.ParseFileRef(req.Record.File)
		if !ok {
			return nil, bad("record.file is not a file id like f_12")
		}
		file, gen, off = f, req.Record.Gen, req.Record.Offset
		err := e.Q.DB.QueryRowContext(ctx, `SELECT len FROM records WHERE file_id=? AND gen=? AND off=? AND item=?`, file, gen, off, req.Record.Item).Scan(&n)
		if err == sql.ErrNoRows {
			return nil, wire.Errf(wire.ErrNotFound, "no such record")
		}
		if err != nil {
			return nil, wire.FromErr(err)
		}
	case req.File != "":
		f, ok := query.ParseFileRef(req.File)
		if !ok {
			return nil, bad("file is not a file id like f_12")
		}
		file, gen, off, n = f, req.Gen, req.Offset, req.Length
		if n <= 0 || n > MaxRawBytes {
			return nil, bad("length must be between 1 and " + strconv.Itoa(MaxRawBytes))
		}
		if off < 0 {
			return nil, bad("offset must not be negative")
		}
		if gen == 0 {
			if err := e.Q.DB.QueryRowContext(ctx, `SELECT cur_gen FROM files WHERE id=?`, file).Scan(&gen); err != nil {
				return nil, wire.Errf(wire.ErrNotFound, "no such file")
			}
		}
	default:
		return nil, bad("raw needs a record or a file with a length")
	}
	var data []byte
	if n > 0 {
		var err error
		if data, err = e.Store.ReadPointer(file, gen, off, n); err != nil {
			return nil, wire.Errf(wire.ErrNotFound, err.Error())
		}
	}
	ans := RawAnswer{File: query.FileRef(file), Gen: gen, Offset: off, Length: len(data), Encoding: "utf8"}
	if utf8.Valid(data) {
		ans.Data = string(data)
	} else {
		ans.Encoding, ans.Data = "base64", base64.StdEncoding.EncodeToString(data)
	}
	return ans, nil
}
