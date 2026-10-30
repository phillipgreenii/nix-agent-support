//go:build darwin

package settle

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
)

// A real clonefile: a copy that diverged from its original (the copier appended
// private blocks) shares its blocks again after settle re-clones it. It skips,
// with the reason, off APFS or when the volume reports no clone capability.
func TestRealClonefileSettleRestoresSharedBlocks(t *testing.T) {
	sys := copier.SystemClone{}
	e := newEnv(t, true)
	e.policy = sys
	if ok, why := sys.Probe(e.state, e.claude); !ok {
		t.Skipf("clonefile unavailable here: %s", why)
	}
	e.open()
	var first string
	for i := 1; i <= 5000; i++ {
		first += line(i % 1000)
	}
	e.write(e.transcript(), first, e.clk.Now().Add(-48*time.Hour), false)
	e.ingest(e.transcript())
	more := strings.Repeat(line(7), 800)
	e.write(e.transcript(), more, e.clk.Now().Add(-48*time.Hour), true)
	e.ingest(e.transcript())
	e.parseAll()
	file := e.fileID()
	f, _ := e.st.File(file)
	cp := e.st.CopyPath(file, f.CurGen)
	before, err := sys.PrivateSize(cp)
	if err != nil || before == 0 {
		t.Fatalf("private size before settle = %d (%v), want the appended blocks", before, err)
	}
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s", e.stage())
	}
	after, err := sys.PrivateSize(cp)
	if err != nil || after != 0 {
		t.Fatalf("private size after settle = %d (%v), want 0 (blocks shared again)", after, err)
	}
}
