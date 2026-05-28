package transcript

import "testing"

func TestErrorKindIsRetryable(t *testing.T) {
	tests := []struct {
		kind ErrorKind
		want bool
	}{
		{ErrUnknown, true},
		{ErrServerError, true},
		{ErrRateLimit, false},
		{ErrInvalidRequest, false},
		{ErrAuthFailed, false},
		{ErrorKind(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := tt.kind.IsRetryable(); got != tt.want {
				t.Errorf("ErrorKind(%q).IsRetryable() = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
