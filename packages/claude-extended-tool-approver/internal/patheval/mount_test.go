package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestEval(t *testing.T, projectRoot string) *PathEvaluator {
	t.Helper()
	return NewWithCWD(projectRoot, projectRoot)
}

func TestWithMounts_NoMatchReturnsReadWrite(t *testing.T) {
	tmp := t.TempDir()
	pe := newTestEval(t, tmp).WithMounts(nil)
	// Container-mode with no mounts: any absolute container path is internal.
	if got := pe.Evaluate("/opt/app/data"); got != PathReadWrite {
		t.Errorf("no-mounts container path: got %v, want PathReadWrite", got)
	}
	if got := pe.Evaluate("/nix/store/abc/cli.js"); got != PathReadWrite {
		t.Errorf("container /nix/store path: got %v, want PathReadWrite", got)
	}
}

func TestWithMounts_RWMountPreservesHostAccess(t *testing.T) {
	projectRoot := t.TempDir()
	hostPE := newTestEval(t, projectRoot)

	// Mount the host project root into the container as /work (rw).
	pe := hostPE.WithMounts([]Mount{
		{HostPath: projectRoot, ContainerPath: "/work", ReadOnly: false},
	})
	// /work/foo.go → projectRoot/foo.go → PathReadWrite (project root).
	if got := pe.Evaluate("/work/foo.go"); got != PathReadWrite {
		t.Errorf("rw mount into project root: got %v, want PathReadWrite", got)
	}
	// A non-mount container path is still internal → RW.
	if got := pe.Evaluate("/etc/passwd"); got != PathReadWrite {
		t.Errorf("non-mount container path: got %v, want PathReadWrite", got)
	}
}

func TestWithMounts_ROMountClampsToReadOnly(t *testing.T) {
	projectRoot := t.TempDir()
	hostPE := newTestEval(t, projectRoot)
	pe := hostPE.WithMounts([]Mount{
		{HostPath: projectRoot, ContainerPath: "/ro", ReadOnly: true},
	})
	if got := pe.Evaluate("/ro/foo.go"); got != PathReadOnly {
		t.Errorf("ro mount: got %v, want PathReadOnly", got)
	}
}

func TestWithMounts_LongestPrefixWins(t *testing.T) {
	projectRoot := t.TempDir()
	// Subdir inside projectRoot so both mounts resolve into the rw project
	// zone; the ro mount should clamp to PathReadOnly.
	inner := filepath.Join(projectRoot, "sub")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	pe := newTestEval(t, projectRoot)
	scoped := pe.WithMounts([]Mount{
		{HostPath: projectRoot, ContainerPath: "/a", ReadOnly: false},
		{HostPath: inner, ContainerPath: "/a/b", ReadOnly: true},
	})
	if got := scoped.Evaluate("/a/b/x"); got != PathReadOnly {
		t.Errorf("longest-prefix ro: got %v, want PathReadOnly", got)
	}
	if got := scoped.Evaluate("/a/c"); got != PathReadWrite {
		t.Errorf("shallower rw: got %v, want PathReadWrite", got)
	}
}

func TestWithMounts_PreservedByWithCWD(t *testing.T) {
	projectRoot := t.TempDir()
	pe := newTestEval(t, projectRoot).WithMounts([]Mount{
		{HostPath: projectRoot, ContainerPath: "/work", ReadOnly: false},
	})
	scoped := pe.WithCWD("/work")
	if !scoped.inContainer {
		t.Fatal("WithCWD dropped container mode")
	}
	if got := scoped.Evaluate("/work/foo"); got != PathReadWrite {
		t.Errorf("after WithCWD: got %v, want PathReadWrite", got)
	}
}
