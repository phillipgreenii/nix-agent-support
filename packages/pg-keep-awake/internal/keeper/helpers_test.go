package keeper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// Re-exec helper. A test binary started with KEEPER_TEST_HELPER set becomes a
// child the test controls, so tests have processes with a chosen executable
// name (a copy named pg-caffeinate-shim) and one that holds leases/.lock.
//
//	sleep        sleep for two minutes
//	flock:<path> take LOCK_EX on <path>, print "locked", then sleep
func TestMain(m *testing.M) {
	switch h := os.Getenv("KEEPER_TEST_HELPER"); {
	case h == "sleep":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case len(h) > 6 && h[:6] == "flock:":
		fd, err := unix.Open(h[6:], unix.O_RDWR|unix.O_CREAT, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("locked")
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newDir(t *testing.T) *lease.Dir {
	t.Helper()
	d, err := lease.EnsureDir(filepath.Join(t.TempDir(), "state", lease.DirName))
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	return d
}

// refusePID is the guard every signalling path in these tests goes through:
// pid 1 or lower would signal init or a process group.
func refusePID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	return nil
}

// fakeProber answers pid liveness from a table.
type fakeProber map[int]lease.ProcState

func (f fakeProber) Probe(pid int) lease.ProcState { return f[pid] }

func alive(startUS int64) lease.ProcState { return lease.ProcState{Exists: true, StartUS: startUS} }

// recorder is a Signal that never touches a real process: it records the pid
// and fails the test if asked about pid 1 or lower.
type recorder struct {
	t    *testing.T
	mu   sync.Mutex
	pids []int
}

func (r *recorder) signal(pid int, sig syscall.Signal) error {
	r.t.Helper()
	if err := refusePID(pid); err != nil {
		r.t.Errorf("backstop tried to signal: %v", err)
		return err
	}
	if sig != syscall.SIGTERM {
		r.t.Errorf("backstop sent %v, want SIGTERM", sig)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pids = append(r.pids, pid)
	return nil
}

func (r *recorder) got() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.pids...)
}

// infoNames answers the executable-name lookup from a table.
func infoNames(names map[int]string) func(int) (lease.ProcInfo, error) {
	return func(pid int) (lease.ProcInfo, error) {
		n, ok := names[pid]
		if !ok {
			return lease.ProcInfo{}, lease.ErrNoProcess
		}
		return lease.ProcInfo{Name: n, Argv: []string{n}}, nil
	}
}

func tp(t time.Time) *time.Time { return &t }

func put(t *testing.T, d *lease.Dir, l *lease.Lease) *lease.Lease {
	t.Helper()
	if err := d.PutLease(l); err != nil {
		t.Fatalf("PutLease(%s.%s): %v", l.Owner, l.Key, err)
	}
	return l
}

// writeRaw writes a lease file without taking the lock, bypassing the writer.
func writeRaw(t *testing.T, d *lease.Dir, name string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

func leaseFile(d *lease.Dir, id string) string {
	return filepath.Join(d.LeasesPath(), id+".json")
}

func liveIDs(res *Result) []string {
	ids := make([]string, 0, len(res.Live))
	for _, l := range res.Live {
		ids = append(ids, l.ID)
	}
	return ids
}
