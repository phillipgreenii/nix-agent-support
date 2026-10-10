package unstick

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAllocateWorkdir_layoutAndSuffixes(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 10, 10, 23, 59, 58, 0, time.FixedZone("p", -7*3600)) // 2026-10-11 UTC
	var paths []string
	for i := 0; i < 3; i++ {
		w, err := AllocateWorkdir(base, now)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, w.Path)
	}
	want := []string{"bead-unstick-2026-10-11", "bead-unstick-2026-10-11-2", "bead-unstick-2026-10-11-3"}
	for i, p := range paths {
		if filepath.Base(p) != want[i] || filepath.Dir(p) != base {
			t.Errorf("path[%d] = %s, want %s", i, p, want[i])
		}
	}
	w := Workdir{Path: paths[0]}
	for _, d := range []string{BatchesDir, ResultsDir, FactsDir, ProbesDir, WorkDir} {
		if st, err := os.Stat(w.Join(d)); err != nil || !st.IsDir() {
			t.Errorf("missing dir %s: %v", d, err)
		}
	}
	prog, err := os.ReadFile(w.Join(ProgressFile))
	if err != nil || string(prog) != "2026-10-11T06:59:58Z\n" {
		t.Errorf("progress = %q err=%v", prog, err)
	}
	if fu, err := os.ReadFile(w.Join(FollowupsFile)); err != nil || len(fu) != 0 {
		t.Errorf("followups = %q err=%v", fu, err)
	}
}

func TestAllocateWorkdir_neverReusesExisting(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	pre := filepath.Join(base, "bead-unstick-2026-10-10")
	if err := os.Mkdir(pre, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(pre, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := AllocateWorkdir(base, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(w.Path) != "bead-unstick-2026-10-10-2" {
		t.Errorf("path = %s", w.Path)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("existing dir was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pre, ProgressFile)); err == nil {
		t.Error("existing dir was initialised")
	}
}

func TestAllocateWorkdir_concurrentRace(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	const n = 24
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w, err := AllocateWorkdir(base, now)
			got[i], errs[i] = w.Path, err
		}()
	}
	close(start)
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("alloc %d: %v", i, errs[i])
		}
		if seen[got[i]] {
			t.Fatalf("duplicate workdir %s", got[i])
		}
		seen[got[i]] = true
		if _, err := os.Stat(filepath.Join(got[i], ProgressFile)); err != nil {
			t.Errorf("%s not initialised: %v", got[i], err)
		}
	}
}

func TestAllocateWorkdir_missingBase(t *testing.T) {
	_, err := AllocateWorkdir(filepath.Join(t.TempDir(), "nope"), time.Now())
	if err == nil || !strings.Contains(err.Error(), "allocate workdir") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateWorkdir(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "mine")
	now := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	w, err := CreateWorkdir(p, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Path != p || w.Join(BatchesDir) != filepath.Join(p, "batches") {
		t.Errorf("w = %+v", w)
	}
	if _, err := CreateWorkdir(p, now); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second create err = %v", err)
	}
	if _, err := CreateWorkdir(filepath.Join(base, "a", "b"), now); err == nil {
		t.Error("missing parent must fail")
	}
}
