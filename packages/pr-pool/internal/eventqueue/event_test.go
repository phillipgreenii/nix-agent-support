package eventqueue

import (
	"errors"
	"testing"
	"time"
)

func TestEvent_Validate(t *testing.T) {
	t.Parallel()
	base := Event{ID: "e1", Type: "review-requested", TTL: 15 * time.Minute}
	tests := []struct {
		name    string
		mutate  func(Event) Event
		wantErr bool
	}{
		{"valid", func(e Event) Event { return e }, false},
		{"valid with payload", func(e Event) Event { e.Payload = map[string]any{"k": "v"}; return e }, false},
		{"missing id", func(e Event) Event { e.ID = ""; return e }, true},
		{"missing type", func(e Event) Event { e.Type = ""; return e }, true},
		{"zero ttl", func(e Event) Event { e.TTL = 0; return e }, true},
		{"negative ttl", func(e Event) Event { e.TTL = -time.Second; return e }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mutate(base).Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error not ErrInvalidEvent: %v", err)
			}
		})
	}
}

func TestParseTTL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"15m", 15 * time.Minute, false},
		{"90s", 90 * time.Second, false},
		{"1h30m", 90 * time.Minute, false},
		{"", 0, true},
		{"nonsense", 0, true},
		{"0s", 0, true},
		{"-5m", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTTL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				if !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("error not ErrInvalidEvent: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseTTL(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
