package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquirePIDFile_PermDeniedReportsClearly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, perm checks bypassed")
	}

	parent := shortTempDir(t)
	readonly := filepath.Join(parent, "readonly")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	paths := Paths{
		Dir:     filepath.Join(readonly, "pa-monitor"),
		PIDFile: filepath.Join(readonly, "pa-monitor", "daemon.pid"),
		Socket:  filepath.Join(readonly, "pa-monitor", "daemon.sock"),
	}
	_, err := AcquirePIDFile(paths)
	if err == nil {
		t.Fatal("expected error on readonly parent")
	}
	if !strings.Contains(err.Error(), "pa-monitor") {
		t.Errorf("error does not include offending path: %v", err)
	}
}
