package gate

import (
	"os"
	"testing"
)

// TestMain removes the process-wide seeded git templates (see internal/gittemplate)
// after the run.
func TestMain(m *testing.M) {
	code := m.Run()
	noBaselineTemplate.Close()
	preBaselineTemplate.Close()
	rebasedTemplate.Close()
	os.Exit(code)
}
