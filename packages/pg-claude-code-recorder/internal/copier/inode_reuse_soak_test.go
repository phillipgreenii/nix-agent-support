//go:build soak

// Scale loop (150 inode-reuse generations) moved out of the unit tier.
// Run by pg-claude-code-recorder-soak (tag soak, tests named TestSoak*), never in the unit gate.

package copier

import (
	"fmt"
	"os"
	"testing"
)

func TestSoakInodeReuseLoopCopiesWhateverNowHoldsThePath(t *testing.T) {
	e := newEnv(t)
	p := e.abs(tx)
	for i := 0; i < 150; i++ {
		_ = os.Remove(p)
		content := fmt.Sprintf("generation %d of the file\n", i)
		e.write(tx, content)
		r := e.ingest(p)
		e.mustEqual(r.File, content)
		holders, _ := e.st.FilesByCurrentPath(p)
		if len(holders) != 1 || holders[0].ID != r.File {
			t.Fatalf("iteration %d: holders %+v", i, holders)
		}
	}
}
