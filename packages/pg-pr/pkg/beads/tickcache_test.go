package beads

import (
	"testing"
)

func TestTickCache_NilSafe(t *testing.T) {
	var cache *TickCache
	if _, ok := cache.OpenCycleFor("anything"); ok {
		t.Error("OpenCycleFor on nil cache should return ok=false")
	}
	if _, ok := cache.FindMergeRequest("r", 1); ok {
		t.Error("FindMergeRequest on nil cache should return ok=false")
	}
}

func TestTickCache_DepsUpFor_NilCache(t *testing.T) {
	var cache *TickCache
	if _, ok := cache.DepsUpFor("anything"); ok {
		t.Error("DepsUpFor on nil cache should return ok=false")
	}
}
