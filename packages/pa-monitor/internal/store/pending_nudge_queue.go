package store

import (
	"context"
	"sync"
)

// PendingNudge is one queued intent. Session+Source is the natural key.
type PendingNudge struct {
	SessionID string
	Source    string
	Text      string
}

// PendingNudgeQueue stores pending nudges. v1 implementation is in-memory.
// A future DB-backed impl can satisfy this interface without changing callers.
type PendingNudgeQueue interface {
	Enqueue(ctx context.Context, p PendingNudge) error
	Cancel(ctx context.Context, sessionID, source string) error
	ForSession(ctx context.Context, sessionID string) ([]PendingNudge, error)
	All(ctx context.Context) ([]PendingNudge, error)
}

type inMemoryQueue struct {
	mu sync.Mutex
	m  map[string]PendingNudge // key = sessionID+"\x00"+source
}

func NewInMemoryPendingNudgeQueue() PendingNudgeQueue {
	return &inMemoryQueue{m: map[string]PendingNudge{}}
}

func (q *inMemoryQueue) key(sid, source string) string { return sid + "\x00" + source }

func (q *inMemoryQueue) Enqueue(_ context.Context, p PendingNudge) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := q.key(p.SessionID, p.Source)
	if _, exists := q.m[k]; exists {
		return nil
	}
	q.m[k] = p
	return nil
}

func (q *inMemoryQueue) Cancel(_ context.Context, sid, source string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.m, q.key(sid, source))
	return nil
}

func (q *inMemoryQueue) ForSession(_ context.Context, sid string) ([]PendingNudge, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []PendingNudge
	for _, p := range q.m {
		if p.SessionID == sid {
			out = append(out, p)
		}
	}
	return out, nil
}

func (q *inMemoryQueue) All(_ context.Context) ([]PendingNudge, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PendingNudge, 0, len(q.m))
	for _, p := range q.m {
		out = append(out, p)
	}
	return out, nil
}
