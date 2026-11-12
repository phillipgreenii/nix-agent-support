//go:build soak

// Scale loop (260 registry versions, 60 loads) moved out of the unit tier.
// Run by pg-claude-code-recorder-soak (tag soak, tests named TestSoak*), never in the unit gate.

package lifecycle

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// A model-cache miss reads the registry versions stored since the last load, not
// every version ever stored: the rows read per load stay flat while the history
// grows, and the model always equals one a fresh engine derives.
func TestSoakModelLoadRowsReadStayBoundedAsRegistryVersionsGrow(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()
	for pid := 73291; pid < 73301; pid++ {
		for i := 0; i < 20; i++ {
			v.writeRegistry(pid, fmt.Sprintf("d503eec4-6b2c-4372-a989-%012d", pid), []string{"idle", "busy"}[i%2])
		}
	}
	if _, err := v.e.loadCtx(ctx); err != nil {
		t.Fatal(err)
	}
	var first, last int64
	for i := 0; i < 60; i++ {
		v.writeRegistry(73291, fmt.Sprintf("d503eec4-6b2c-4372-a989-%012d", 73291), []string{"idle", "busy"}[i%2])
		v.cnt.Reset()
		m, err := v.e.loadCtx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows := v.cnt.RowsRead()
		if i == 0 {
			first = rows
		}
		last = rows
		if !reflect.DeepEqual(m, v.uncachedModel()) {
			t.Fatalf("write %d: incremental model differs from a fresh load", i)
		}
	}
	// 260 versions are stored by now; a full read would return all of them.
	if first == 0 || last > first+5 {
		t.Fatalf("rows read per model load grew with the history: %d after 200 versions, %d after 260", first, last)
	}
	if first > 40 {
		t.Fatalf("a load after one new version read %d rows, want a handful", first)
	}
}
