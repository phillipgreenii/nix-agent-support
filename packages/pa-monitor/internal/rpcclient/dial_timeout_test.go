package rpcclient

import (
	"testing"
	"time"
)

func TestDialTimeout_ProductionDefaultIsTwoSeconds(t *testing.T) {
	t.Setenv(DialTimeoutEnv, "")
	if got := DialTimeout(); got != 2*time.Second {
		t.Fatalf("DialTimeout() = %v, want 2s", got)
	}
	if DefaultDialTimeout != 2*time.Second {
		t.Fatalf("DefaultDialTimeout = %v, want 2s", DefaultDialTimeout)
	}
}

func TestDialTimeout_Override(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"5ms", 5 * time.Millisecond},
		{"garbage", DefaultDialTimeout},
		{"0s", DefaultDialTimeout},
		{"-1s", DefaultDialTimeout},
	} {
		t.Setenv(DialTimeoutEnv, tc.env)
		if got := DialTimeout(); got != tc.want {
			t.Errorf("env %q: DialTimeout() = %v, want %v", tc.env, got, tc.want)
		}
	}
}
