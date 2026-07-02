package usage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestProviderActiveBlockParsesCachedBytes proves the parse moved into the
// adapter: the Provider fetches raw bytes from the CachedRunner and returns a
// parsed *Block, so consumers never call ParseActiveBlock themselves.
func TestProviderActiveBlockParsesCachedBytes(t *testing.T) {
	cache := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte(`{"blocks":[{"id":"b1","isActive":true,"costUSD":7.5}]}`), nil
	})
	cache.refresh(context.Background())
	p := NewProvider(cache, &Runner{})

	b, err := p.ActiveBlock(context.Background())
	if err != nil {
		t.Fatalf("ActiveBlock err: %v", err)
	}
	if b == nil {
		t.Fatal("ActiveBlock = nil, want parsed block")
	}
	if b.CostUSD != 7.5 {
		t.Errorf("CostUSD = %v, want 7.5", b.CostUSD)
	}
}

// TestProviderActiveBlockNilBeforeFirstSuccess pins the load-bearing caveat:
// CachedRunner.Get returns (nil,nil) before the first successful refresh, and
// the adapter MUST NOT attempt to parse nil — it returns (nil,nil) so the
// poller treats the block as "not yet available" (behavior unchanged).
func TestProviderActiveBlockNilBeforeFirstSuccess(t *testing.T) {
	cache := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return []byte(`{"blocks":[]}`), nil
	})
	// No refresh() called → cache has no bytes yet.
	p := NewProvider(cache, &Runner{})

	b, err := p.ActiveBlock(context.Background())
	if err != nil {
		t.Fatalf("ActiveBlock err before first success = %v, want nil", err)
	}
	if b != nil {
		t.Errorf("ActiveBlock before first success = %+v, want nil", b)
	}
}

// TestProviderProbedReflectsCache pins the probe-state pair semantics: Probed()
// returns (CachedRunner.Probed(), CachedRunner.LastErr()) — "probed but errored"
// must not collapse into "not probed".
func TestProviderProbedReflectsCache(t *testing.T) {
	boom := errors.New("boom")
	cache := NewCachedRunner(1*time.Hour, 1*time.Second, func(context.Context) ([]byte, error) {
		return nil, boom
	})
	p := NewProvider(cache, &Runner{})

	if probed, err := p.Probed(); probed || err != nil {
		t.Errorf("before refresh: Probed() = (%v,%v), want (false,nil)", probed, err)
	}

	cache.refresh(context.Background())
	probed, err := p.Probed()
	if !probed {
		t.Error("after failed refresh: Probed() probed = false, want true")
	}
	if err == nil {
		t.Error("after failed refresh: Probed() err = nil, want non-nil")
	}
}

// TestProviderCurrentWeeklyDelegatesToRunner proves the weekly path routes
// through the adapter (built from a Runner whose RunCmd is injected).
func TestProviderCurrentWeeklyDelegatesToRunner(t *testing.T) {
	runner := &Runner{
		RunCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"totals":{"totalCost":90.0},"weekly":[{"period":"2026-05-18","totalCost":90.0,"agent":"all"}]}`), nil
		},
	}
	p := NewProvider(NewCachedRunner(1*time.Hour, 1*time.Second, nil), runner)

	w, err := p.CurrentWeekly(context.Background())
	if err != nil {
		t.Fatalf("CurrentWeekly err: %v", err)
	}
	if w == nil {
		t.Fatal("CurrentWeekly = nil, want entry")
	}
	if w.Period != "2026-05-18" || w.TotalCost != 90.0 {
		t.Errorf("CurrentWeekly = %+v, want period=2026-05-18 cost=90", w)
	}
}
