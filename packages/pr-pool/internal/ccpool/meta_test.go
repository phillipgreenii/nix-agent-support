package ccpool

import (
	"reflect"
	"testing"
)

func TestDispatchMeta_buildsPrpoolNamespacedMap(t *testing.T) {
	got := DispatchMeta("zr-1", "worker")
	want := map[string]string{
		"prpool.bead": "zr-1",
		"prpool.role": "worker",
		"prpool.pool": "pr-pool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DispatchMeta = %v, want %v", got, want)
	}
}
