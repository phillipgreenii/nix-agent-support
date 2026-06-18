package item

import "testing"

func TestItem_zeroValueAndFields(t *testing.T) {
	it := Item{ID: "pg2-x", Type: "task", Title: "t", Metadata: map[string]any{"author": "me"}}
	if it.ID != "pg2-x" || it.Type != "task" || it.Title != "t" {
		t.Fatalf("fields not set: %+v", it)
	}
	if it.Metadata["author"] != "me" {
		t.Fatalf("metadata not set: %+v", it.Metadata)
	}
	var zero Item
	if zero.Metadata != nil {
		t.Fatalf("zero metadata should be nil")
	}
}
