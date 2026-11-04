package copier

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// TestRealCloneOnAPFS uses the real clonefile call (darwin: CLONE_NOFOLLOW, the
// VOL_CAP_INT_CLONE probe, ATTR_CMNEXT_PRIVATESIZE). It skips, with the reason,
// where the temporary directory is not on a clone-capable volume.
func TestRealCloneOnAPFS(t *testing.T) {
	root := t.TempDir()
	policy := SystemClone{}
	state := filepath.Join(root, "state")
	claude := filepath.Join(root, "claude")
	for _, d := range []string{state, filepath.Join(claude, "projects", "example-project")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ok, reason := policy.Probe(state, claude)
	if !ok {
		t.Skipf("no clone-capable volume here: %s", reason)
	}
	o := obs.New(nil)
	st, err := store.Open(state, store.Options{Obs: o})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cp, err := New(Config{Store: st, Scope: scope.New(claude, ""), Clock: clock.NewFake(t0), Clone: policy, Obs: o})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := cp.Method(); m != "clone" {
		t.Fatalf("method %s", m)
	}
	p := filepath.Join(claude, "projects", "example-project", "example-session.jsonl")
	body := make([]byte, 64<<10)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := cp.Ingest(p)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := st.Generation(r.File, 1)
	if g.Method != "clone" || g.CopyOffset != int64(len(body)) {
		t.Fatalf("gen %+v", g)
	}
	// A deleted original leaves the clone intact.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(st.CopyPath(r.File, 1))
	if err != nil || string(got) != string(body) {
		t.Fatalf("clone lost its bytes after the original was deleted: %v", err)
	}
	if err := cp.SweepMissing(map[Identity]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := o.Reg.Gauge(obs.MClonePrivateMax, nil); !ok && runtime.GOOS == "darwin" {
		t.Fatal("private size not exported")
	}
}

func TestRealCloneRefusesToFollowSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if ok, why := (SystemClone{}).Probe(root, root); !ok {
		t.Skipf("no clone-capable volume here: %s", why)
	}
	dst := filepath.Join(root, "dst")
	if err := (SystemClone{}).Clone(link, dst); err == nil {
		fi, _ := os.Lstat(dst)
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("CLONE_NOFOLLOW followed the link")
		}
	}
}

func TestRealPrivateSizeOfAClone(t *testing.T) {
	root := t.TempDir()
	if ok, why := (SystemClone{}).Probe(root, root); !ok {
		t.Skipf("no clone-capable volume here: %s", why)
	}
	src, dst := filepath.Join(root, "s"), filepath.Join(root, "d")
	if err := os.WriteFile(src, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (SystemClone{}).Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	ps, err := (SystemClone{}).PrivateSize(dst)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("private size is darwin-only")
	}
	if err != nil {
		t.Fatal(err)
	}
	if ps != 0 {
		t.Logf("private size of an untouched clone = %d (expected 0 on APFS)", ps)
	}
}
