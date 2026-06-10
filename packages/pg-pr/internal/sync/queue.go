package sync

import stdsync "sync"

// refreshQueue is a dedup FIFO of prKeys. Enqueuing a key already pending is a
// no-op that keeps its position. Safe for concurrent producers/consumers.
type refreshQueue struct {
	mu      stdsync.Mutex
	order   []prKey
	pending map[prKey]struct{}
}

func newRefreshQueue() *refreshQueue {
	return &refreshQueue{pending: map[prKey]struct{}{}}
}

func (q *refreshQueue) enqueue(k prKey) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[k]; ok {
		return
	}
	q.pending[k] = struct{}{}
	q.order = append(q.order, k)
}

func (q *refreshQueue) dequeue() (prKey, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return prKey{}, false
	}
	k := q.order[0]
	q.order = q.order[1:]
	delete(q.pending, k)
	return k, true
}

func (q *refreshQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.order)
}
