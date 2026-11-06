//go:build integration

package serve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Status answers from the cached snapshot: with the read pool closed (so any
// statement would fail) a call over 20,000 files still answers, quickly, and
// the answer does not depend on the file count.
func TestStatusIssuesNoStatementsAndIsFastOver20000Files(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 20000)
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	snap := d.loadStatusSnap()
	if snap == nil || snap.cov.Files != 20000 {
		t.Fatalf("snapshot %+v", snap)
	}
	if err := d.st.Read().Close(); err != nil { // from here every read statement errors
		t.Fatal(err)
	}
	if _, err := d.st.Stages(); err == nil {
		t.Fatal("read pool still answers; the test would prove nothing")
	}
	start := time.Now()
	body, werr := d.opStatus(context.Background(), nil)
	if werr != nil {
		t.Fatal(werr)
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("status took %v over 20000 files", el)
	}
	if st := body.(wire.Status); st.StatusAgeS < 0 {
		t.Fatalf("status %+v", st)
	}
}
