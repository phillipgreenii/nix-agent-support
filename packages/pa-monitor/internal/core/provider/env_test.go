package provider

import "testing"

func TestEnv_FetchesOnceWhileAlive(t *testing.T) {
	c := New(nil)
	c.PidAlive = func(int) bool { return true }
	calls := 0
	c.FetchEnv = func(int) (map[string]string, error) {
		calls++
		return map[string]string{"TMUX": "1"}, nil
	}
	m1, _ := c.Env("s", 42)
	m2, _ := c.Env("s", 42)
	if calls != 1 {
		t.Fatalf("expected 1 fetch while alive, got %d", calls)
	}
	if m1["TMUX"] != "1" || m2["TMUX"] != "1" {
		t.Fatalf("env not returned: %v / %v", m1, m2)
	}
}

func TestEnv_DeadPidEmptyNoFetch(t *testing.T) {
	c := New(nil)
	c.PidAlive = func(int) bool { return false }
	calls := 0
	c.FetchEnv = func(int) (map[string]string, error) { calls++; return map[string]string{"X": "y"}, nil }
	m, err := c.Env("s", 42)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("dead pid must yield empty env, got %v", m)
	}
	if calls != 0 {
		t.Fatalf("dead pid must not fetch, got %d fetches", calls)
	}
}

func TestEnv_ReturnsCopy(t *testing.T) {
	c := New(nil)
	c.PidAlive = func(int) bool { return true }
	c.FetchEnv = func(int) (map[string]string, error) { return map[string]string{"A": "1"}, nil }
	m1, _ := c.Env("s", 42)
	m1["A"] = "mutated"
	m1["B"] = "added"
	m2, _ := c.Env("s", 42)
	if m2["A"] != "1" || len(m2) != 1 {
		t.Fatalf("cache mutated through returned map: %v", m2)
	}
}

func TestEnv_ReusedPidFreshPerSession(t *testing.T) {
	c := New(nil)
	c.PidAlive = func(int) bool { return true }
	which := "A"
	c.FetchEnv = func(int) (map[string]string, error) { return map[string]string{"W": which}, nil }
	old, _ := c.Env("old", 42)
	which = "B"
	fresh, _ := c.Env("new", 42) // same pid, different session-id → fresh fetch
	if old["W"] != "A" {
		t.Fatalf("old session env changed: %v", old)
	}
	if fresh["W"] != "B" {
		t.Fatalf("reused pid did not fetch fresh for new session: %v", fresh)
	}
}
