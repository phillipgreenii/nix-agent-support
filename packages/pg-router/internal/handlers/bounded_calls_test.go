package handlers_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

// Every cmdrun.Call in the module's non-test code sets a Timeout (or carries a
// stated //cmdrun:unbounded reason), so no external call can run unbounded.
func TestCmdrunCallsAreBounded(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, ".")
}

// Negative control: the guard really flags an unbounded Call, and accepts a
// bounded one and an annotated one.
func TestCmdrunBoundedGuardFlagsAnUnboundedCall(t *testing.T) {
	root := t.TempDir()
	src := `package x

import "github.com/phillipgreenii/x/cmdrun"

var bad = cmdrun.Call{Name: "a"}

var ok = cmdrun.Call{Name: "b", Timeout: 1}

//cmdrun:unbounded detached by design
var annotated = cmdrun.Call{Name: "c"}
`
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := cmdruntest.UnboundedCalls(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("flagged %v, want exactly the one unbounded literal", got)
	}
}
