package governor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const requestsBucket Bucket = "requests"

// declaredConfig is a one-bucket governor: two requests per second, a hard cap.
func declaredConfig() Config {
	return Config{
		Declared:        []BucketSpec{{Bucket: requestsBucket, Unit: "requests", Limits: Limits{Cap: 2, Window: time.Second}}},
		BackgroundShare: 1,
	}
}

func TestDeclaredBucketIsAHardRollingCapWithASubMinuteWindow(t *testing.T) {
	g, clk := newGov(t, declaredConfig(), Options{})
	if err := spend(t, g, requestsBucket, ClassInteractive, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(400 * time.Millisecond)
	if err := spend(t, g, requestsBucket, ClassInteractive, 1); err != nil {
		t.Fatalf("second admission inside the second refused: %v", err)
	}
	var r *Refusal
	err := g.Check(Request{Bucket: requestsBucket, Class: ClassInteractive})
	if !errors.As(err, &r) || r.Kind != RefusedCap || !errors.Is(err, ErrRefused) {
		t.Fatalf("third admission = %v, want a cap *Refusal", err)
	}
	if want := t0.Add(time.Second); !r.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want when the oldest spend ages out, %v", r.RetryAt, want)
	}
	for _, want := range []string{"requests", "per second", "cap reached"} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q lacks %q", r.Reason, want)
		}
	}
	// The cap is hard for the background classes too.
	if err := g.Check(Request{Bucket: requestsBucket, Class: ClassQuery}); !errors.Is(err, ErrRefused) {
		t.Errorf("background admission past the cap = %v", err)
	}
	clk.Set(t0.Add(time.Second))
	if err := spend(t, g, requestsBucket, ClassInteractive, 1); err != nil {
		t.Fatalf("admission one second after the first spend refused: %v", err)
	}
	if err := g.Check(Request{Bucket: requestsBucket, Class: ClassInteractive}); !errors.Is(err, ErrRefused) {
		t.Errorf("the window holds two again, yet a third was admitted: %v", err)
	}
}

func TestDeclaredGovernorMetersExactlyTheDeclaredBuckets(t *testing.T) {
	cfg := Config{
		Declared: []BucketSpec{
			{Bucket: "zeta", Unit: "points", Limits: Limits{Cap: 10}},
			{Bucket: "alpha", Limits: Limits{Cap: 5, Window: time.Minute}},
		},
		BackgroundShare: 0.5,
		// The fixed fields are ignored and not validated.
		RESTCore: Limits{Cap: -1},
	}
	g, _ := newGov(t, cfg, Options{})
	if got := g.Buckets(); len(got) != 2 || got[0] != "zeta" || got[1] != "alpha" {
		t.Fatalf("Buckets() = %v, want declaration order [zeta alpha]", got)
	}
	st := g.State()
	if len(st.Buckets) != 2 || st.Buckets[0].Bucket != "zeta" || st.Buckets[1].Bucket != "alpha" {
		t.Fatalf("State buckets = %+v", st.Buckets)
	}
	if st.Buckets[0].Window != DefaultWindow || st.Buckets[1].Window != time.Minute {
		t.Errorf("windows = %v, %v; want the default hour and a minute", st.Buckets[0].Window, st.Buckets[1].Window)
	}
	if st.Buckets[1].BackgroundCap != 2 {
		t.Errorf("background cap = %d, want 2 (half of 5, rounded down)", st.Buckets[1].BackgroundCap)
	}
	if err := g.Check(Request{Bucket: Buckets()[0], Class: ClassInteractive}); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("an undeclared bucket is admitted or refused as a limit: %v", err)
	}
	// Units name the refusal: the declared one, and "requests" by default.
	for bucket, unit := range map[Bucket]string{"zeta": "points", "alpha": "requests"} {
		err := g.Check(Request{Bucket: bucket, Class: ClassInteractive, Cost: 11})
		var r *Refusal
		if !errors.As(err, &r) || !strings.Contains(r.Reason, unit) {
			t.Errorf("%s refusal = %v, want unit %q", bucket, err, unit)
		}
	}
	// PauseAll holds every declared bucket's background classes.
	g.PauseAll(time.Minute, "")
	for _, b := range g.Buckets() {
		if err := g.Check(Request{Bucket: b, Class: ClassQuery}); err == nil || !strings.Contains(err.Error(), "paused") {
			t.Errorf("%s background not paused: %v", b, err)
		}
	}
}

func TestUndeclaredGovernorKeepsTheFixedBucketsInOrder(t *testing.T) {
	g, _ := newGov(t, testConfig(), Options{})
	got, want := g.Buckets(), Buckets()
	if len(got) != len(want) {
		t.Fatalf("Buckets() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Buckets()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	got[0] = "mutated"
	if g.Buckets()[0] != want[0] {
		t.Error("Buckets() returned the governor's own slice")
	}
	st := g.State()
	if len(st.Buckets) != 3 || st.Buckets[0].Bucket != Buckets()[0] {
		t.Errorf("State = %+v", st.Buckets)
	}
	err := g.Check(Request{Bucket: Buckets()[0], Class: ClassInteractive, Cost: 4001})
	if err == nil || !strings.Contains(err.Error(), "points") {
		t.Errorf("first bucket refusal = %v, want unit points", err)
	}
}

func TestDeclaredConfigValidate(t *testing.T) {
	ok := declaredConfig()
	if err := ok.Validate(); err != nil {
		t.Fatalf("one-second window rejected: %v", err)
	}
	spec := func(name Bucket, cap, reserve int64) BucketSpec {
		return BucketSpec{Bucket: name, Limits: Limits{Cap: cap, Reserve: reserve, Window: time.Second}}
	}
	for name, declared := range map[string][]BucketSpec{
		"empty list":       {},
		"empty name":       {spec("", 1, 0)},
		"duplicate name":   {spec("a", 1, 0), spec("a", 2, 0)},
		"zero cap":         {spec("a", 0, 0)},
		"negative cap":     {spec("a", -1, 0)},
		"negative reserve": {spec("a", 1, -1)},
		"second one bad":   {spec("a", 1, 0), spec("b", 0, 0)},
	} {
		cfg := Config{Declared: declared, BackgroundShare: 1}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, declared)
		}
		if _, err := New(cfg, Options{}); err == nil {
			t.Errorf("%s: New accepted %+v", name, declared)
		}
	}
	bad := declaredConfig()
	bad.BackgroundShare = 0
	if err := bad.Validate(); err == nil {
		t.Error("a declared list with background share zero was accepted")
	}
}

func TestDeclaredSpendIsReplayedAfterARestart(t *testing.T) {
	path := storePath(t)
	clk := NewManualClock(t0)
	s1 := openStore(t, path)
	g1, _ := newGov(t, declaredConfig(), Options{Clock: clk, Store: s1})
	for i := 0; i < 2; i++ {
		if err := spend(t, g1, requestsBucket, ClassInteractive, 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(100 * time.Millisecond)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openStore(t, path)
	defer func() { _ = s2.Close() }()
	g2, _ := newGov(t, declaredConfig(), Options{Clock: clk, Store: s2})
	var r *Refusal
	err := g2.Check(Request{Bucket: requestsBucket, Class: ClassInteractive})
	if !errors.As(err, &r) || r.Kind != RefusedCap {
		t.Fatalf("after a restart the spent window was forgotten: %v", err)
	}
	if want := t0.Add(time.Second); !r.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", r.RetryAt, want)
	}
	if st, ok := g2.State().Bucket(requestsBucket); !ok || st.Spent != 2 {
		t.Errorf("replayed spend = %+v, want 2", st)
	}
	clk.Set(t0.Add(time.Second))
	if err := spend(t, g2, requestsBucket, ClassInteractive, 1); err != nil {
		t.Errorf("admission after the oldest spend aged out: %v", err)
	}
}
