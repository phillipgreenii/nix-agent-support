//go:build darwin

package proc

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Real is the operating-system process table.
type Real struct {
	once sync.Once
	kq   int
	kerr error
	mu   sync.Mutex
	cbs  map[int]func(time.Time)
}

// NewReal returns the real process table.
func NewReal() *Real { return &Real{cbs: map[int]func(time.Time){}} }

// StartTime reads the kernel's start time of pid (kern.proc.pid).
func (*Real) StartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return time.Time{}, ErrGone
	}
	tv := kp.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000).UTC(), nil
}

// Environ reads the process's environment with the KERN_PROCARGS2 sysctl (not
// by running ps).
func (*Real) Environ(pid int) (map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcArgs2(b), nil
}

func (r *Real) init() error {
	r.once.Do(func() {
		fd, err := unix.Kqueue()
		if err != nil {
			r.kerr = err
			return
		}
		r.kq = fd
		go r.loop()
	})
	return r.kerr
}

func (r *Real) loop() {
	events := make([]unix.Kevent_t, 16)
	ts := unix.NsecToTimespec(int64(time.Second))
	for {
		n, err := unix.Kevent(r.kq, nil, events, &ts)
		if err != nil && err != unix.EINTR {
			return
		}
		now := time.Now()
		for _, ev := range events[:max(n, 0)] {
			if ev.Filter != unix.EVFILT_PROC || ev.Fflags&unix.NOTE_EXIT == 0 {
				continue
			}
			r.mu.Lock()
			cb := r.cbs[int(ev.Ident)]
			delete(r.cbs, int(ev.Ident))
			r.mu.Unlock()
			if cb != nil {
				cb(now)
			}
		}
	}
}

// Watch registers a kqueue NOTE_EXIT watch on pid. Registering on an already
// exited pid fails with ESRCH, reported as ErrGone.
func (r *Real) Watch(pid int, onExit func(time.Time)) (func(), error) {
	if err := r.init(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cbs[pid] = onExit
	r.mu.Unlock()
	ev := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	if _, err := unix.Kevent(r.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		r.mu.Lock()
		delete(r.cbs, pid)
		r.mu.Unlock()
		if err == unix.ESRCH {
			return nil, ErrGone
		}
		return nil, err
	}
	stop := func() {
		r.mu.Lock()
		delete(r.cbs, pid)
		r.mu.Unlock()
		del := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_DELETE}
		_, _ = unix.Kevent(r.kq, []unix.Kevent_t{del}, nil, nil)
	}
	return stop, nil
}
