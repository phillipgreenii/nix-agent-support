package main

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

func TestCancelExitCode(t *testing.T) {
	if c := cancelExitCode(nil); c != 0 {
		t.Errorf("nil -> %d, want 0", c)
	}
	if c := cancelExitCode(session.ErrCancelUnconfirmed); c != 6 {
		t.Errorf("unconfirmed -> %d, want 6", c)
	}
	if c := cancelExitCode(errors.New("boom")); c != 1 {
		t.Errorf("other -> %d, want 1", c)
	}
}
