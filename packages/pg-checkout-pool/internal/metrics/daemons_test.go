package metrics

import (
	"context"
	"testing"
	"time"
)

type lsofRec struct {
	calls [][]int
	cwd   map[int]string
}

func (r *lsofRec) run(_ context.Context, pids []int) ([]byte, error) {
	r.calls = append(r.calls, append([]int(nil), pids...))
	var s string
	for _, p := range pids {
		if c, ok := r.cwd[p]; ok {
			s += "p" + itoa(p) + "\nn" + c + "\n"
		}
	}
	return []byte(s), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestDaemonCounterMatchesPSParse(t *testing.T) {
	t0 := time.Unix(1000, 0)
	procs := []Proc{
		{Pid: 101, Start: t0, Args: "/usr/bin/git fsmonitor--daemon run --detach"},
		{Pid: 202, Start: t0, Args: "git fsmonitor--daemon run"},
		{Pid: 303, Start: t0, Args: "/usr/bin/vim notes.txt"},
		{Pid: 404, Start: t0, Args: "git fsmonitor--daemon run"},
	}
	ps := " 101 /usr/bin/git fsmonitor--daemon run --detach\n 202 git fsmonitor--daemon run\n 303 /usr/bin/vim notes.txt\n 404 git fsmonitor--daemon run\n"
	if got := ParsePSPids(ps); len(got) != 3 {
		t.Fatalf("ps parse = %v", got)
	}
	rec := &lsofRec{cwd: map[int]string{101: "/pool/slots/slot-01/tree", 202: "/elsewhere/repo", 404: "/pool/slots/slot-02/tree"}}
	c := NewDaemonCounterWith(func() ([]Proc, error) { return procs, nil }, rec.run, func() time.Time { return t0 })
	n, err := c.CountDaemons("/pool/slots")
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2 (the orphan at /elsewhere is not in-slot)", n, err)
	}
	if len(rec.calls) != 1 || len(rec.calls[0]) != 3 {
		t.Fatalf("lsof calls = %v", rec.calls)
	}
}

func TestDaemonCounterCachesByPidAndStart(t *testing.T) {
	t0 := time.Unix(1000, 0)
	now := t0
	procs := []Proc{{Pid: 101, Start: t0, Args: "git fsmonitor--daemon run"}}
	rec := &lsofRec{cwd: map[int]string{101: "/pool/slots/slot-01/tree"}}
	c := NewDaemonCounterWith(func() ([]Proc, error) { return procs, nil }, rec.run, func() time.Time { return now })
	count := func() int {
		n, err := c.CountDaemons("/pool/slots")
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 || count() != 1 || len(rec.calls) != 1 {
		t.Fatalf("a cached (pid,start) must not be looked up again: calls=%v", rec.calls)
	}
	// Negative control: the pid is reused by a daemon elsewhere with a new start time.
	procs = []Proc{{Pid: 101, Start: t0.Add(time.Hour), Args: "git fsmonitor--daemon run"}}
	rec.cwd[101] = "/elsewhere"
	if n := count(); n != 0 || len(rec.calls) != 2 {
		t.Fatalf("reused pid must be re-looked-up: n=%d calls=%v", n, rec.calls)
	}
	// Expiry.
	now = now.Add(10 * time.Minute)
	count()
	if len(rec.calls) != 3 {
		t.Fatalf("expired entry must be re-looked-up: calls=%v", rec.calls)
	}
	// No daemons: no lsof, cache emptied.
	procs = nil
	if count() != 0 || len(rec.calls) != 3 || len(c.cache) != 0 {
		t.Fatalf("no daemons: calls=%v cache=%v", rec.calls, c.cache)
	}
}
