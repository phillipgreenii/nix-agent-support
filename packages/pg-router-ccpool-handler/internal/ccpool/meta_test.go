package ccpool

import (
	"reflect"
	"testing"
)

func TestDispatchMeta_buildsPgrouterNamespacedMap(t *testing.T) {
	got := DispatchMeta("zr-1", "worker")
	want := map[string]string{
		"pgrouter.bead": "zr-1",
		"pgrouter.role": "worker",
		"pgrouter.pool": "pg-router",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DispatchMeta = %v, want %v", got, want)
	}
}
