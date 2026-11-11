package copier

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// sizedClone answers PrivateSize from a table keyed by base name and counts calls.
type sizedClone struct {
	fakeClone
	sizes map[string]int64
	calls int
	err   error
}

func (s *sizedClone) PrivateSize(p string) (int64, error) {
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	v, ok := s.sizes[filepath.Base(p)]
	if !ok {
		return 0, os.ErrNotExist
	}
	return v, nil
}

// scanEnv puts n files in copies/ and removes them again when the test ends, so
// the store invariants check sees the directory as it found it.
func scanEnv(t *testing.T, n int) (*env, *sizedClone) {
	t.Helper()
	e := newEnv(t)
	sc := &sizedClone{sizes: map[string]int64{}}
	e.cp.cfg.Clone = sc
	dir := filepath.Join(e.state, "copies")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("900%d.1", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		sc.sizes[name] = 10
		t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, name)) })
	}
	return e, sc
}

func gauge(t *testing.T, e *env, name string) (float64, bool) {
	t.Helper()
	return e.obs.Reg.Gauge(name, nil)
}

func TestPrivateScanSumsWholeTreeAndPublishes(t *testing.T) {
	e, _ := scanEnv(t, 3)
	e.cp.PrivateScanStep(t0, time.Hour)
	if v, ok := gauge(t, e, obs.MCopiesPrivateSize); !ok || v != 30 {
		t.Fatalf("size = %v %v, want 30", v, ok)
	}
	if v, _ := gauge(t, e, obs.MCopiesPrivateFiles); v != 3 {
		t.Fatalf("files = %v, want 3", v)
	}
	if v, _ := gauge(t, e, obs.MCopiesPrivateScanned); v != float64(t0.UnixNano())/1e9 {
		t.Fatalf("scan timestamp = %v", v)
	}
}

func TestPrivateScanIsBoundedPerStep(t *testing.T) {
	n := PrivateScanBudget*2 + 7
	e, sc := scanEnv(t, n)
	e.cp.PrivateScanStep(t0, time.Hour)
	if sc.calls != PrivateScanBudget {
		t.Fatalf("first step examined %d files, want the budget %d", sc.calls, PrivateScanBudget)
	}
	if _, ok := gauge(t, e, obs.MCopiesPrivateSize); ok {
		t.Fatal("gauge published before the sweep finished")
	}
	e.cp.PrivateScanStep(t0.Add(time.Second), time.Hour)
	e.cp.PrivateScanStep(t0.Add(2*time.Second), time.Hour)
	if v, ok := gauge(t, e, obs.MCopiesPrivateSize); !ok || v != float64(n*10) {
		t.Fatalf("size = %v %v, want %d", v, ok, n*10)
	}
	if sc.calls != n {
		t.Fatalf("examined %d files in total, want %d", sc.calls, n)
	}
}

func TestPrivateScanWaitsForNextSweep(t *testing.T) {
	e, sc := scanEnv(t, 2)
	e.cp.PrivateScanStep(t0, time.Hour)
	done := sc.calls
	e.cp.PrivateScanStep(t0.Add(30*time.Minute), time.Hour)
	if sc.calls != done {
		t.Fatalf("a sweep started before the interval passed (%d calls)", sc.calls)
	}
	for k := range sc.sizes {
		sc.sizes[k] = 25
	}
	e.cp.PrivateScanStep(t0.Add(time.Hour), time.Hour)
	if v, _ := gauge(t, e, obs.MCopiesPrivateSize); v != 50 {
		t.Fatalf("size after the second sweep = %v, want 50", v)
	}
}

func TestPrivateScanSkipsVanishedFilesAndStopsWhenUnsupported(t *testing.T) {
	e, sc := scanEnv(t, 3)
	delete(sc.sizes, "9001.1") // listed, then PrivateSize says it is gone
	e.cp.PrivateScanStep(t0, time.Hour)
	if v, _ := gauge(t, e, obs.MCopiesPrivateSize); v != 20 {
		t.Fatalf("size = %v, want 20", v)
	}
	if v, _ := gauge(t, e, obs.MCopiesPrivateFiles); v != 2 {
		t.Fatalf("files = %v, want 2", v)
	}

	e2, sc2 := scanEnv(t, 3)
	sc2.err = errors.ErrUnsupported
	e2.cp.PrivateScanStep(t0, time.Hour)
	e2.cp.PrivateScanStep(t0.Add(2*time.Hour), time.Hour)
	if _, ok := gauge(t, e2, obs.MCopiesPrivateSize); ok {
		t.Fatal("gauge set on a platform that reports no private size")
	}
	if sc2.calls != 1 {
		t.Fatalf("kept probing after ErrUnsupported (%d calls)", sc2.calls)
	}
}
