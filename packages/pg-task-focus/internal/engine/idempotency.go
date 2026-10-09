package engine

import (
	"container/list"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// noOpCacheSize is the number of no-op results the engine remembers.
const noOpCacheSize = 1024

// request is a durable request that carried an id: its request hash, the
// events it appended (without the batch.committed) and its batch id when it
// appended a batch.
type request struct {
	hash   string
	events []event.ID
	batch  event.ID
}

// storedID is an id the log already uses: an event's id, with the batch it
// is a member of (member), or a batch's id with the events of the batch.
type storedID struct {
	batch  bool
	member event.ID
	events []event.ID
}

// index is the record of request ids, rebuilt from the req_hash fields of the
// log at Open and kept up to date by each commit. It works from the stored
// events as appended, retracted ones and batch.committed included, never from
// the model's views.
type index struct {
	requests map[event.ID]*request // by request id: a lone event's id or a batch's id
	stored   map[event.ID]*storedID
}

func newIndex(log []event.Event) *index {
	x := &index{requests: map[event.ID]*request{}, stored: map[event.ID]*storedID{}}
	for _, e := range log {
		x.add(e)
	}
	return x
}

// add records one stored event: its id, the batch it is a member of, and,
// when it carries a req_hash, the request it belongs to. The request id of a
// batch member (and of its batch.committed) is the batch id; of a lone event,
// its own id.
func (x *index) add(e event.Event) {
	committed := e.Payload.EventType() == event.TypeBatchCommitted
	batch := e.Payload.BatchID()
	x.stored[e.ID] = &storedID{member: batch, events: []event.ID{e.ID}}
	if batch != "" {
		s := x.stored[batch]
		if s == nil {
			s = &storedID{batch: true}
			x.stored[batch] = s
		}
		if !committed {
			s.events = append(s.events, e.ID)
		}
	}
	if e.ReqHash == "" {
		return
	}
	key := batch
	if key == "" {
		key = e.ID
	}
	r := x.requests[key]
	if r == nil {
		r = &request{hash: e.ReqHash, batch: batch}
		x.requests[key] = r
	}
	if !committed {
		r.events = append(r.events, e.ID)
	}
}

// lookup answers a request with id and hash from the record of request ids:
// found is set when the id is a durable request's with the same hash, and a
// non-nil refusal is the id_conflict of an id the log already uses otherwise.
func (x *index) lookup(id event.ID, hash string) (r *request, found bool, conflict *command.Rejection) {
	if r := x.requests[id]; r != nil {
		if r.hash == hash {
			return r, true, nil
		}
		what := "event " + string(id)
		if r.batch != "" {
			what = fmt.Sprintf("batch %s (%s)", id, eventList(r.events))
		}
		return nil, false, &command.Rejection{
			Reason: command.ReasonIDConflict, Events: append([]event.ID(nil), r.events...),
			Message: fmt.Sprintf(
				"The request id %s was already used by an earlier request with different content, which stored %s; a new request needs a new id.",
				id, what,
			),
		}
	}
	if s := x.stored[id]; s != nil {
		// A request's id is its lone event's id or its batch's id, so an id
		// recorded under neither is a member of a batch, or an event or a
		// batch stored with no req_hash.
		msg := fmt.Sprintf("The request id %s is the id of the stored event %s, which no request with an id produced; a new request needs a new id.", id, id)
		switch {
		case s.batch:
			msg = fmt.Sprintf("The request id %s is the id of the stored batch %s (%s), which no request with an id produced; a new request needs a new id.", id, id, eventList(s.events))
		case s.member != "":
			msg = fmt.Sprintf("The request id %s is the id of the stored event %s, a member of batch %s, and a request with a batch is known by its batch id, never by a member's; a new request needs a new id.", id, id, s.member)
		}
		return nil, false, &command.Rejection{
			Reason: command.ReasonIDConflict, Events: append([]event.ID(nil), s.events...), Message: msg,
		}
	}
	return nil, false, nil
}

// eventList names events as "event a", "events a and b" or "events a, b and c".
func eventList(ids []event.ID) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = string(id)
	}
	switch len(names) {
	case 0:
		return "no events"
	case 1:
		return "event " + names[0]
	}
	return "events " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// noOp is a remembered no-op: the request id, its hash and its result.
type noOp struct {
	id     event.ID
	hash   string
	result Result
}

// noOpCache remembers the results of no-ops, which append nothing and so
// leave no req_hash in the log, for the life of the process. It is bounded and
// evicts the least recently used entry; a lookup counts as a use.
type noOpCache struct {
	size  int
	order *list.List // of *noOp, the most recently used first
	byID  map[event.ID]*list.Element
}

func newNoOpCache(size int) *noOpCache {
	return &noOpCache{size: size, order: list.New(), byID: map[event.ID]*list.Element{}}
}

// get returns the no-op remembered under id, marking it used.
func (c *noOpCache) get(id event.ID) (*noOp, bool) {
	el, ok := c.byID[id]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*noOp), true
}

// put remembers a no-op, evicting the least recently used entry when full.
func (c *noOpCache) put(n *noOp) {
	if el, ok := c.byID[n.id]; ok {
		el.Value = n
		c.order.MoveToFront(el)
		return
	}
	c.byID[n.id] = c.order.PushFront(n)
	for c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.byID, oldest.Value.(*noOp).id)
	}
}

// lookup answers a request with id and hash from the cache: found is set for
// the same hash, and a non-nil refusal is the id_conflict of other content.
func (c *noOpCache) lookup(id event.ID, hash string) (n *noOp, found bool, conflict *command.Rejection) {
	n, ok := c.get(id)
	switch {
	case !ok:
		return nil, false, nil
	case n.hash == hash:
		return n, true, nil
	}
	return nil, false, &command.Rejection{
		Reason: command.ReasonIDConflict,
		Message: fmt.Sprintf(
			"The request id %s was already used by an earlier request with different content, which changed nothing; a new request needs a new id.",
			id,
		),
	}
}
