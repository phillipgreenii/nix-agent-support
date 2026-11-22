package github

import (
	"context"
	"sync"
	"time"
)

// manualScopes is the scope-timer seam (Provider.scopeTimeout) for a test: the
// scopes it hands out end only when the test fires them, with
// DeadlineExceeded as a real timeout would, so a share is cut without waiting
// it out in real time.
type manualScopes struct {
	mu     sync.Mutex
	scopes []*manualScope
}

type manualScope struct {
	context.Context // the parent
	done            chan struct{}
	once            sync.Once
	mu              sync.Mutex
	err             error
}

func (s *manualScope) Done() <-chan struct{} { return s.done }

func (s *manualScope) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *manualScope) end(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (m *manualScopes) timeout(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
	s := &manualScope{Context: parent, done: make(chan struct{})}
	go func() {
		select {
		case <-parent.Done():
			s.end(parent.Err())
		case <-s.done:
		}
	}()
	m.mu.Lock()
	m.scopes = append(m.scopes, s)
	m.mu.Unlock()
	return s, func() { s.end(context.Canceled) }
}

// fire ends the n-th scope handed out (0-based, in creation order) the way its
// timeout would.
func (m *manualScopes) fire(n int) {
	m.mu.Lock()
	s := m.scopes[n]
	m.mu.Unlock()
	s.end(context.DeadlineExceeded)
}

// cutProvider is a Provider whose shares are cut by hand: a page that stalls
// fires the scope of its own connection (ListCommentsReport opens the threads'
// scope first and the issue comments' second).
func cutProvider(r *stallRunner) *Provider {
	ms := &manualScopes{}
	r.onStall = func(kind string) {
		switch kind {
		case "threads":
			ms.fire(0)
		case "issues":
			ms.fire(1)
		}
	}
	p := NewWithRunner(r)
	p.scopeTimeout = ms.timeout
	return p
}
