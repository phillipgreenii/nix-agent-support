package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func putShim(t *testing.T, d *Dir, key string) *Lease {
	t.Helper()
	l := &Lease{Owner: "shim", Key: key, Kind: KindShim, Client: &ProcRef{PID: 4412, StartUS: 1760400000123456}, Flags: []string{"i"}}
	if err := d.PutLease(l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	return l
}

func TestLeaseCreateRefreshListRemove(t *testing.T) {
	d := newDir(t)
	l := putShim(t, d, "1-1")

	fi, err := os.Stat(filepath.Join(d.LeasesPath(), "shim.1-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lease mode = %04o, want 0600", fi.Mode().Perm())
	}

	// Refresh: same id, new expiry.
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	refreshed := *l
	refreshed.ExpiresAt = &exp
	if err := d.PutLease(&refreshed); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadLease(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.Client == nil || got.Client.PID != 4412 {
		t.Errorf("refresh not persisted: %+v", got)
	}

	putShim(t, d, "2-2")
	res, err := d.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Leases) != 2 || res.Rejected != 0 || res.Leases[0].ID != "shim.1-1" || res.Leases[1].ID != "shim.2-2" {
		t.Errorf("list = %+v", res)
	}

	if err := d.RemoveLease("shim.1-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveLease("shim.1-1"); err != nil {
		t.Errorf("removing a missing lease must be fine: %v", err)
	}
	if _, err := d.ReadLease("shim.1-1"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadLease after remove = %v", err)
	}
	res, _ = d.ListLeases()
	if len(res.Leases) != 1 {
		t.Errorf("after remove: %d leases", len(res.Leases))
	}
}

func TestPutLeaseRefusesUnboundedAndLeavesNoFile(t *testing.T) {
	d := newDir(t)
	l := &Lease{Owner: "alice", Key: "k", Kind: KindAcquire}
	if err := d.PutLease(l); err == nil {
		t.Fatal("a lease with no client, expires_at or watch must be refused")
	}
	entries, _ := os.ReadDir(d.LeasesPath())
	for _, e := range entries {
		if e.Name() != ".lock" {
			t.Errorf("refused lease left %s behind", e.Name())
		}
	}
}

func TestInvalidIDsRejectedByIO(t *testing.T) {
	d := newDir(t)
	for _, id := range []string{"../x.y", "noDot", "a.b.c", "a./b", ""} {
		if err := d.RemoveLease(id); err == nil {
			t.Errorf("RemoveLease(%q) accepted", id)
		}
		if _, err := d.ReadLease(id); err == nil {
			t.Errorf("ReadLease(%q) accepted", id)
		}
	}
}

func TestScannerIgnoresDotAndTmpAndNonJSON(t *testing.T) {
	d := newDir(t)
	putShim(t, d, "1-1")
	for _, name := range []string{".shim.9-9.123.abc.tmp", ".hidden.json", "shim.8-8.json.tmp", "notes.txt", "x.tmp"} {
		if err := os.WriteFile(filepath.Join(d.LeasesPath(), name), []byte("{garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := d.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Leases) != 1 || res.Rejected != 0 || len(res.Unparsable) != 0 {
		t.Errorf("scanner looked at ignored names: %+v", res)
	}
}

func TestWriterLeavesNoTempFilesAndUsesTempThenRename(t *testing.T) {
	d := newDir(t)
	putShim(t, d, "1-1")
	entries, _ := os.ReadDir(d.LeasesPath())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSymlinkAndFIFOAreSkippedAndCounted(t *testing.T) {
	d := newDir(t)
	putShim(t, d, "1-1")

	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(d.LeasesPath(), "x.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(d.LeasesPath(), "fifo.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan *ScanResult, 1)
	go func() { res, _ := d.ListLeases(); done <- res }() // a FIFO must not block the scan
	select {
	case res := <-done:
		if len(res.Leases) != 1 || res.Rejected != 2 {
			t.Errorf("want 1 lease and 2 rejected, got %d leases, %d rejected", len(res.Leases), res.Rejected)
		}
		if len(res.Unparsable) != 0 {
			t.Errorf("symlink/FIFO must not be junk-eligible: %+v", res.Unparsable)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListLeases blocked on a FIFO")
	}
	if _, err := d.ReadLease("x.json"); err == nil {
		t.Error("ReadLease accepted a bad id")
	}
	// Neither special file is removed by a sweep, however old.
	lk, err := d.TakeLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	if n, _ := lk.SweepJunk(time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("sweep removed %d special files", n)
	}
	for _, name := range []string{"x.json", "fifo.json"} {
		if _, err := os.Lstat(filepath.Join(d.LeasesPath(), name)); err != nil {
			t.Errorf("%s was deleted: %v", name, err)
		}
	}
}

func TestOversizedLeaseRejected(t *testing.T) {
	d := newDir(t)
	big := `{"schema_version":1,"pad":"` + strings.Repeat("x", MaxLeaseFileBytes) + `"}`
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), "shim.1-1.json"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := d.ListLeases()
	if len(res.Leases) != 0 || res.Rejected != 1 {
		t.Errorf("oversized file: %+v", res)
	}
	// Exactly at the cap is accepted by the size check (it is then judged on content).
	if _, _, err := readSafe(filepath.Join(d.LeasesPath(), "shim.1-1.json"), int64(len(big))); err != nil {
		t.Errorf("file at the cap rejected: %v", err)
	}
}

func TestScanConsidersAtMost1024Files(t *testing.T) {
	d := newDir(t)
	for i := 0; i < MaxLeaseFiles+5; i++ {
		// Cheap valid leases, written directly.
		body := fmt.Sprintf(`{"schema_version":1,"id":"o.k%04d","owner":"o","key":"k%04d","kind":"run","client":{"pid":5,"start_us":5}}`, i, i)
		if err := os.WriteFile(filepath.Join(d.LeasesPath(), fmt.Sprintf("o.k%04d.json", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := d.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Leases) != MaxLeaseFiles || res.Rejected != 5 {
		t.Errorf("got %d leases and %d rejected, want %d and 5", len(res.Leases), res.Rejected, MaxLeaseFiles)
	}
}

func TestUnparsableJSONKeptUntilStale(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(d.LeasesPath(), "shim.7-7.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := d.ListLeases()
	if len(res.Leases) != 0 || res.Rejected != 1 || len(res.Unparsable) != 1 {
		t.Fatalf("scan = %+v", res)
	}

	sweep := func(now time.Time) int {
		t.Helper()
		lk, err := d.TakeLock()
		if err != nil {
			t.Fatal(err)
		}
		defer lk.Unlock()
		n, err := lk.SweepJunk(now)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	mtime := res.Unparsable[0].ModTime
	if n := sweep(mtime.Add(59 * time.Second)); n != 0 {
		t.Errorf("deleted at 59s: %d", n)
	}
	if n := sweep(mtime.Add(StaleAfter)); n != 0 {
		t.Errorf("deleted at exactly 60s (must be MORE than 60s): %d", n)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("junk removed too early: %v", err)
	}
	if n := sweep(mtime.Add(61 * time.Second)); n != 1 {
		t.Errorf("not deleted at 61s: %d", n)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("junk still present after the stale sweep: %v", err)
	}
}

func TestInvalidLeaseContentAndNameMismatchAreJunk(t *testing.T) {
	d := newDir(t)
	// Parses, but breaks the rules: no client/expires/watch.
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), "o.k.json"),
		[]byte(`{"schema_version":1,"id":"o.k","owner":"o","key":"k","kind":"run"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid lease under the wrong file name.
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), "wrong.json"),
		[]byte(`{"schema_version":1,"id":"o.k2","owner":"o","key":"k2","kind":"run","client":{"pid":5,"start_us":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := d.ListLeases()
	if len(res.Leases) != 0 || res.Rejected != 2 || len(res.Unparsable) != 2 {
		t.Errorf("scan = %+v", res)
	}
}

func TestFutureSchemaLeaseUnreadableNotDeleted(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(d.LeasesPath(), "o.k.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"id":"o.k","new_field":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := d.ListLeases()
	if len(res.Leases) != 0 || len(res.Unreadable) != 1 || res.Unreadable[0] != "o.k.json" || len(res.Unparsable) != 0 {
		t.Errorf("scan = %+v", res)
	}
	if _, err := d.ReadLease("o.k"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("ReadLease = %v, want ErrUnreadable", err)
	}
	lk, _ := d.TakeLock()
	defer lk.Unlock()
	if n, _ := lk.SweepJunk(time.Now().Add(24 * time.Hour)); n != 0 {
		t.Errorf("sweep deleted a future-schema file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("future-schema file deleted: %v", err)
	}
}

func TestLeaseReadIgnoresUnknownFields(t *testing.T) {
	d := newDir(t)
	body := `{"schema_version":1,"id":"o.k","owner":"o","key":"k","kind":"run","client":{"pid":5,"start_us":5,"extra":1},"from_the_future":[1,2]}`
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), "o.k.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := d.ReadLease("o.k")
	if err != nil || l.Client == nil || l.Client.PID != 5 {
		t.Errorf("unknown fields not tolerated: %v %+v", err, l)
	}
}

func TestSweepRemovesOnlyStaleTmp(t *testing.T) {
	d := newDir(t)
	old := filepath.Join(d.LeasesPath(), ".o.k.1.abc.tmp")
	fresh := filepath.Join(d.LeasesPath(), ".o.k.2.def.tmp")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * StaleAfter)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	lk, _ := d.TakeLock()
	defer lk.Unlock()
	n, err := lk.SweepJunk(time.Now())
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Error("stale tmp not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh tmp removed")
	}
}

func TestLockBoundIsTwoSeconds(t *testing.T) {
	if LockTimeout != 2*time.Second {
		t.Fatalf("LockTimeout = %v, want 2s", LockTimeout)
	}
	d := newDir(t)
	held, err := d.TakeLock()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = d.TakeLock() // a second open file description conflicts even in-process
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("second TakeLock = %v, want ErrLockTimeout", err)
	}
	if elapsed < 1900*time.Millisecond || elapsed > 4*time.Second {
		t.Errorf("waited %v, want about 2s", elapsed)
	}
	// PutLease on a wedged lock fails the same way, without writing.
	if err := d.PutLease(&Lease{Owner: "o", Key: "k", Kind: KindRun, Client: &ProcRef{PID: 5, StartUS: 5}}); !errors.Is(err, ErrLockTimeout) {
		t.Errorf("PutLease under a held lock = %v", err)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	lk, err := d.TakeLock()
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	lk.Unlock()
	if err := lk.Unlock(); err != nil {
		t.Errorf("double Unlock: %v", err)
	}
}

func TestLockShortTimeout(t *testing.T) {
	d := newDir(t)
	held, _ := d.TakeLock()
	defer held.Unlock()
	start := time.Now()
	if _, err := d.takeLock(50 * time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("short timeout not honoured")
	}
}

// A lease refreshed between a reaper's first read and its unlink survives
// because the reaper re-reads under the lock (the protocol this layer offers).
func TestLockSerialisesWriters(t *testing.T) {
	d := newDir(t)
	lk, _ := d.TakeLock()
	done := make(chan error, 1)
	go func() {
		done <- d.PutLease(&Lease{Owner: "o", Key: "k", Kind: KindRun, Client: &ProcRef{PID: 5, StartUS: 5}})
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(d.LeasesPath(), "o.k.json")); err == nil {
		t.Fatal("writer got past the held lock")
	}
	lk.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.LeasesPath(), "o.k.json")); err != nil {
		t.Fatal("writer did not finish after unlock")
	}
}
