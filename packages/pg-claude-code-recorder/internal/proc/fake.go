package proc

import (
	"sync"
	"time"
)

// Fake is the ProcessTable seam for tests: processes are added by hand, exits
// are fired by hand.
type Fake struct {
	mu      sync.Mutex
	procs   map[int]*fakeProc
	Watched []int // pids a watch was registered on, in order
}

type fakeProc struct {
	start time.Time
	env   map[string]string
	cbs   map[int]func(time.Time)
	next  int
}

// NewFake returns an empty process table.
func NewFake() *Fake { return &Fake{procs: map[int]*fakeProc{}} }

// Add makes pid a live process that started at start with the given environment.
func (f *Fake) Add(pid int, start time.Time, env map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid] = &fakeProc{start: start.UTC(), env: env}
}

// Exit ends pid: its watches fire with at, and it is gone.
func (f *Fake) Exit(pid int, at time.Time) {
	f.mu.Lock()
	p := f.procs[pid]
	delete(f.procs, pid)
	f.mu.Unlock()
	if p == nil {
		return
	}
	f.mu.Lock()
	cbs := make([]func(time.Time), 0, len(p.cbs))
	for _, cb := range p.cbs {
		cbs = append(cbs, cb)
	}
	f.mu.Unlock()
	for _, cb := range cbs {
		cb(at)
	}
}

// Vanish removes pid without firing its watches (it ended while nothing watched).
func (f *Fake) Vanish(pid int) {
	f.mu.Lock()
	delete(f.procs, pid)
	f.mu.Unlock()
}

// StartTime implements System.
func (f *Fake) StartTime(pid int) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.procs[pid]; p != nil {
		return p.start, nil
	}
	return time.Time{}, ErrGone
}

// Environ implements System.
func (f *Fake) Environ(pid int) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.procs[pid]; p != nil {
		return p.env, nil
	}
	return nil, ErrGone
}

// Watch implements System.
func (f *Fake) Watch(pid int, onExit func(time.Time)) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.procs[pid]
	if p == nil {
		return nil, ErrGone
	}
	if p.cbs == nil {
		p.cbs = map[int]func(time.Time){}
	}
	id := p.next
	p.next++
	p.cbs[id] = onExit
	f.Watched = append(f.Watched, pid)
	return func() {
		f.mu.Lock()
		delete(p.cbs, id)
		f.mu.Unlock()
	}, nil
}
