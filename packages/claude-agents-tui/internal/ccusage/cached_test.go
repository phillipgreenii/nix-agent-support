package ccusage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCachedRunnerReturnsNilBeforeFirstRefresh(t *testing.T) {
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte("fresh"), nil
	})
	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Errorf("Get before refresh = %q, want nil", string(got))
	}
}

func TestCachedRunnerRefreshPopulatesCache(t *testing.T) {
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte("fresh"), nil
	})
	c.refresh(context.Background())
	got, _ := c.Get(context.Background())
	if string(got) != "fresh" {
		t.Errorf("Get after refresh = %q, want %q", string(got), "fresh")
	}
}

func TestCachedRunnerProbedFalseBeforeRefresh(t *testing.T) {
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte("x"), nil
	})
	if c.Probed() {
		t.Error("Probed() should be false before any refresh")
	}
}

func TestCachedRunnerProbedTrueAfterSuccessfulRefresh(t *testing.T) {
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte("x"), nil
	})
	c.refresh(context.Background())
	if !c.Probed() {
		t.Error("Probed() should be true after successful refresh")
	}
	if c.LastErr() != nil {
		t.Errorf("LastErr() should be nil after successful refresh, got %v", c.LastErr())
	}
}

func TestCachedRunnerProbedTrueAndLastErrSetAfterFailedRefresh(t *testing.T) {
	boom := errors.New("boom")
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return nil, boom
	})
	c.refresh(context.Background())
	if !c.Probed() {
		t.Error("Probed() should be true even after failed refresh")
	}
	if c.LastErr() == nil {
		t.Error("LastErr() should be non-nil after failed refresh")
	}
}

func TestCachedRunnerKeepsLastGoodOnError(t *testing.T) {
	calls := 0
	c := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("v1"), nil
		}
		return nil, errors.New("boom")
	})
	c.refresh(context.Background())
	c.refresh(context.Background())
	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("Get after failed refresh = %q, want last-good %q", string(got), "v1")
	}
}
