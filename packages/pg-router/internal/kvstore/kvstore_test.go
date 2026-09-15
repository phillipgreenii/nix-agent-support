package kvstore

import "testing"

// Interface contract tests: any Store implementation must behave this way.
// NewInMemory is exercised here as the default implementation under test.

func TestGetAfterPut(t *testing.T) {
	s := NewInMemory()

	if err := s.Put("k", "v1"); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	value, ok, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("Get: expected ok=true after Put")
	}
	if value != "v1" {
		t.Fatalf("Get: expected value %q, got %q", "v1", value)
	}

	// A second Put on the same key overwrites the value.
	if err := s.Put("k", "v2"); err != nil {
		t.Fatalf("Put (overwrite): unexpected error: %v", err)
	}
	value, ok, err = s.Get("k")
	if err != nil {
		t.Fatalf("Get (after overwrite): unexpected error: %v", err)
	}
	if !ok || value != "v2" {
		t.Fatalf("Get (after overwrite): expected (v2, true), got (%q, %v)", value, ok)
	}
}

func TestGetOfMissingKey(t *testing.T) {
	s := NewInMemory()

	value, ok, err := s.Get("absent")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("Get: expected ok=false for a key that was never put, got value %q", value)
	}
}

func TestGetAfterDelete(t *testing.T) {
	s := NewInMemory()

	if err := s.Put("k", "v"); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	if err := s.Delete("k"); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	value, ok, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("Get: expected ok=false after Delete, got value %q", value)
	}
}

func TestDeleteOfAbsentKeyIsNoOp(t *testing.T) {
	s := NewInMemory()

	// Deleting a key that was never put must succeed silently, not error.
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("Delete of absent key: expected no error, got: %v", err)
	}

	// And it must not disturb an unrelated existing key.
	if err := s.Put("other", "value"); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	if err := s.Delete("still-absent"); err != nil {
		t.Fatalf("Delete of absent key: expected no error, got: %v", err)
	}
	value, ok, err := s.Get("other")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if !ok || value != "value" {
		t.Fatalf("Get: expected unrelated key to survive, got (%q, %v)", value, ok)
	}
}
