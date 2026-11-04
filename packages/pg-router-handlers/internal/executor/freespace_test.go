package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// freeSpaceRun builds a run whose worktree opener records every worktree it is
// asked to create and whose free-space function reports free bytes.
func freeSpaceRun(t *testing.T, free uint64, floor int64) (*ccpoolRun, *dtest.FakeCC, *dtest.NoopGitOpener, *[]string) {
	t.Helper()
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cfg.MinFreeBytes = floor
	cc := &dtest.FakeCC{}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, cfg)
	opener.MissingAt = map[string]bool{filepath.Join(cfg.WorktreeDir, "zr-w"): true}
	var measured []string
	e.deps.FreeBytes = func(p string) (uint64, error) {
		measured = append(measured, p)
		return free, nil
	}
	return e, cc, opener, &measured
}

// Below the floor a dispatch creates no worktree, launches no session and
// declines with the low-disk sentinel.
func TestRun_freeSpaceBelowFloorSkipsWorktreeCreation(t *testing.T) {
	e, cc, opener, measured := freeSpaceRun(t, 30<<30, 40<<30)
	d := DispatchContext{Role: workerRole(e.deps.Cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrLowDisk) {
		t.Fatalf("err = %v, want errors.Is(err, ErrLowDisk)", err)
	}
	var lf *LowFreeSpaceError
	if !errors.As(err, &lf) || lf.Free != 30<<30 || lf.Floor != 40<<30 {
		t.Fatalf("err = %v, want a *LowFreeSpaceError with free 30GiB and floor 40GiB", err)
	}
	if !strings.Contains(err.Error(), "no worktree created") {
		t.Errorf("message must say nothing was created: %v", err)
	}
	if created(opener) {
		t.Fatalf("a worktree was created below the floor: %v", opener.WTM.Calls)
	}
	if len(cc.Ensured) != 0 {
		t.Fatal("no session may be launched below the floor")
	}
	if len(*measured) != 1 || (*measured)[0] != e.deps.Cfg.WorktreeDir {
		t.Fatalf("free space must be measured once on the worktree dir; got %v", *measured)
	}
}

// At or above the floor the dispatch proceeds to create the worktree.
func TestRun_freeSpaceAtFloorProceeds(t *testing.T) {
	e, _, opener, _ := freeSpaceRun(t, 40<<30, 40<<30)
	d := DispatchContext{Role: workerRole(e.deps.Cfg), Item: item.Item{ID: "zr-w"}}
	_, _ = e.run(context.Background(), d)
	if !created(opener) {
		t.Fatalf("a worktree must be created when free space meets the floor; calls=%v", opener.WTM.Calls)
	}
}

// A floor of 0 disables the check: the free-space function is never read.
func TestRun_freeSpaceFloorZeroDisablesCheck(t *testing.T) {
	e, _, _, measured := freeSpaceRun(t, 0, 0)
	d := DispatchContext{Role: workerRole(e.deps.Cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if errors.Is(err, ErrLowDisk) || len(*measured) != 0 {
		t.Fatalf("floor 0 must not measure or decline; err=%v measured=%v", err, *measured)
	}
}

// A worktree that already exists is reused and needs no new space.
func TestRun_freeSpaceExistingWorktreeNotChecked(t *testing.T) {
	e, _, _, measured := freeSpaceRun(t, 1, 40<<30)
	d := DispatchContext{Role: workerRole(e.deps.Cfg), Item: item.Item{ID: "zr-w"}}
	// worktreePathExists is a plain directory probe on WorktreeDir/<bead>.
	mk := filepath.Join(e.deps.Cfg.WorktreeDir, "zr-w")
	if err := mkdir(mk); err != nil {
		t.Fatal(err)
	}
	_, err := e.run(context.Background(), d)
	if errors.Is(err, ErrLowDisk) || len(*measured) != 0 {
		t.Fatalf("an existing worktree must skip the check; err=%v measured=%v", err, *measured)
	}
}

// A measurement failure fails open: the guard must not itself stop dispatching.
func TestCheckFreeSpace_measurementErrorPasses(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cfg.MinFreeBytes = 40 << 30
	d := Deps{Cfg: cfg, FreeBytes: func(string) (uint64, error) { return 0, errors.New("statfs failed") }}
	if _, err := d.checkFreeSpace(); err != nil {
		t.Fatalf("a measurement error must pass, got %v", err)
	}
}

// The real statfs resolves a not-yet-created directory through its ancestor.
func TestStatfsFree_nonexistentPathUsesAncestor(t *testing.T) {
	free, err := statfsFree(filepath.Join(t.TempDir(), "not", "yet", "there"))
	if err != nil || free == 0 {
		t.Fatalf("statfsFree = %d, %v; want a positive free count", free, err)
	}
}

func mkdir(p string) error { return os.MkdirAll(p, 0o755) }

// created reports whether the opener's manager was asked to add a worktree.
func created(o *dtest.NoopGitOpener) bool {
	for _, c := range o.WTM.Calls {
		if len(c) > 0 && c[0] == "add" {
			return true
		}
	}
	return false
}
