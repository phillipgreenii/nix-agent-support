package groupsvc

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsKeepTheirTextAndAreDistinct(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"no fetcher keeps the text of the service it came from", ErrNoFetcher, "groupsvc: no fetcher registered for the group"},
		{"gone is neutral", ErrGone, "groupsvc: entity is gone"},
		{"unknown group", ErrUnknownGroup, "groupsvc: unknown group"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
		})
	}
	if errors.Is(ErrGone, ErrNoFetcher) || errors.Is(ErrNoFetcher, ErrUnknownGroup) || errors.Is(ErrUnknownGroup, ErrGone) {
		t.Errorf("the sentinels must be distinct values")
	}
}

func TestUnknownGroupWrappedWithTheNameKeepsTheOldText(t *testing.T) {
	err := fmt.Errorf("%w %q", ErrUnknownGroup, "example-group")
	if !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("errors.Is(%v, ErrUnknownGroup) = false", err)
	}
	if got, want := err.Error(), `groupsvc: unknown group "example-group"`; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestErrGoneSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("fetch: %w", ErrGone)
	if !errors.Is(err, ErrGone) {
		t.Errorf("a wrapped ErrGone is not recognised")
	}
}
