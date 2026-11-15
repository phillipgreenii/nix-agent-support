package executor

import (
	"context"
	"os"
	"testing"
)

// TestMain makes the default live-process probe hermetic: no test depends on
// lsof, and none sees a real process. A test that cares sets Deps.ProcessCWDs.
func TestMain(m *testing.M) {
	defaultProcessCWDs = func(context.Context) ([]string, error) { return nil, nil }
	code := m.Run()
	sharedRealGit.Remove()
	sharedOtherGit.Remove()
	sharedKilledGit.Remove()
	os.Exit(code)
}
