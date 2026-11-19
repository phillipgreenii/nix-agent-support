package executor

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	sharedRealGit.Remove()
	sharedOtherGit.Remove()
	sharedKilledGit.Remove()
	os.Exit(code)
}
