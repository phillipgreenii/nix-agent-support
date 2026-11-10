//go:build !darwin

package proc

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"time"
)

// Real is the operating-system process table (Linux: /proc and a one-second
// liveness poll that checks the start time).
type Real struct{}

// NewReal returns the real process table.
func NewReal() *Real { return &Real{} }

// Close is a no-op here: each watch's poller stops with its stop func.
func (*Real) Close() error { return nil }

// StartTime reads the start time from /proc/<pid>/stat and the boot time from
// /proc/stat (USER_HZ is 100 on every supported kernel).
func (*Real) StartTime(pid int) (time.Time, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, ErrGone
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return time.Time{}, ErrGone
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return time.Time{}, ErrGone
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return time.Time{}, ErrGone
	}
	st, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, ErrGone
	}
	var boot int64
	for _, ln := range strings.Split(string(st), "\n") {
		if v, ok := strings.CutPrefix(ln, "btime "); ok {
			boot, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return time.Unix(boot+ticks/100, (ticks%100)*10_000_000).UTC(), nil
}

// Environ reads /proc/<pid>/environ.
func (*Real) Environ(pid int) (map[string]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if k, v, ok := strings.Cut(string(kv), "="); ok && k != "" {
			env[k] = v
		}
	}
	return env, nil
}

// Watch polls once a second for the pid to vanish or to change start time.
func (r *Real) Watch(pid int, onExit func(time.Time)) (func(), error) {
	t0, err := r.StartTime(pid)
	if err != nil {
		return nil, ErrGone
	}
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				if t, err := r.StartTime(pid); err != nil || !t.Equal(t0) {
					onExit(time.Now())
					return
				}
			}
		}
	}()
	var once bool
	return func() {
		if !once {
			once = true
			close(done)
		}
	}, nil
}
