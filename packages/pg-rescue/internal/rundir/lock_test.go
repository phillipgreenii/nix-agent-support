package rundir

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

// mkRun makes <root>/runs/<name> holding a marker file.
func mkRun(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "runs", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestAcquireHoldsAnExclusiveLockUntilReleased(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".lock: %v %v", fi, err)
	}
	if !lockHeld(dir) {
		t.Error("the lock must be seen as held while the wrapper runs")
	}
	if _, err := Acquire(dir); err == nil {
		t.Error("a second Acquire on the same directory must fail")
	}
	l.Release()
	if lockHeld(dir) {
		t.Error("the lock must be free after Release")
	}
	l.Release() // idempotent
	var nilLock *Lock
	nilLock.Release() // nil-safe
}

func TestAcquireFailsWithoutADirectory(t *testing.T) {
	if _, err := Acquire(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error")
	}
}

func TestAcquireDoesNotFollowASymlinkedLockFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(target, filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Error("Acquire must refuse a symlinked .lock")
	}
	if exists(target) {
		t.Error("the symlink target was created through the link")
	}
}

func TestLockHeldIsFalseWithoutALockFile(t *testing.T) {
	if lockHeld(t.TempDir()) {
		t.Error("no lock file means not held")
	}
}

func TestIDTime(t *testing.T) {
	got, ok := IDTime("20261002T140311Z-7f3a9c2e")
	if !ok || !got.Equal(time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)) {
		t.Errorf("IDTime = %v %v", got, ok)
	}
	for _, bad := range []string{"", "20261002T140311Z", "20261002T140311Z-7F3A9C2E", "20261302T140311Z-7f3a9c2e", "x20261002T140311Z-7f3a9c2e", "20261002T140311Z-7f3a9c2e/"} {
		if _, ok := IDTime(bad); ok {
			t.Errorf("IDTime(%q) must fail", bad)
		}
	}
}

func TestPruneDeletesOnlyOldUnlockedRealRunDirectories(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	keepMarker := filepath.Join(outside, "precious")
	if err := os.WriteFile(keepMarker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	old := "20260901T000000Z-aaaaaaaa"        // 41 days before now
	borderline := "20261005T000000Z-bbbbbbbb" // exactly 7 days
	justOver := "20261004T235959Z-cccccccc"   // 7 days and 1 second
	fresh := "20261011T000000Z-dddddddd"
	locked := "20260801T000000Z-eeeeeeee"
	misnamed := "20260801T000000Z-EEEEEEEE"
	notAnID := "scratch"
	symlinked := "20260802T000000Z-ffffffff"
	filePosing := "20260803T000000Z-99999999"

	oldDir := mkRun(t, root, old)
	borderDir := mkRun(t, root, borderline)
	overDir := mkRun(t, root, justOver)
	freshDir := mkRun(t, root, fresh)
	lockedDir := mkRun(t, root, locked)
	misDir := mkRun(t, root, misnamed)
	scratchDir := mkRun(t, root, notAnID)
	if err := os.Symlink(outside, filepath.Join(root, "runs", symlinked)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs", filePosing), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE an old run directory is removed as a link, never followed.
	if err := os.Symlink(outside, filepath.Join(oldDir, "evil")); err != nil {
		t.Fatal(err)
	}
	lk, err := Acquire(lockedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()

	removed := Prune(root, now)
	slices.Sort(removed)
	if want := []string{old, justOver}; !slices.Equal(removed, want) {
		t.Errorf("removed = %v; want %v", removed, want)
	}
	for _, d := range []string{oldDir, overDir} {
		if exists(d) {
			t.Errorf("%s should have been pruned", d)
		}
	}
	for name, p := range map[string]string{
		"exactly 7 days":    borderDir,
		"fresh":             freshDir,
		"locked":            lockedDir,
		"misnamed":          misDir,
		"not a run id":      scratchDir,
		"symlinked":         filepath.Join(root, "runs", symlinked),
		"file posing as id": filepath.Join(root, "runs", filePosing),
	} {
		if !exists(p) {
			t.Errorf("%s entry was deleted: %s", name, p)
		}
	}
	if !exists(keepMarker) {
		t.Error("pruning followed a symlink and deleted its target")
	}
}

func TestPruneTakesAFreedLockAsPrunable(t *testing.T) {
	root := t.TempDir()
	dir := mkRun(t, root, "20260801T000000Z-12345678")
	lk, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := Prune(root, now); len(got) != 0 {
		t.Fatalf("pruned a locked directory: %v", got)
	}
	lk.Release()
	if got := Prune(root, now); len(got) != 1 || exists(dir) {
		t.Errorf("a released directory should be pruned: %v", got)
	}
}

func TestPruneWithNoRunsDirectoryIsANoOp(t *testing.T) {
	if got := Prune(filepath.Join(t.TempDir(), "nothing"), now); got != nil {
		t.Errorf("removed = %v", got)
	}
}

func TestLockIsReleasedWhenTheHoldingProcessDies(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if !lockHeld(dir) {
		t.Fatal("expected held")
	}
	_ = f.Close() // closing the descriptor is what process death does
	if lockHeld(dir) {
		t.Error("closing the descriptor must release the lock")
	}
}
