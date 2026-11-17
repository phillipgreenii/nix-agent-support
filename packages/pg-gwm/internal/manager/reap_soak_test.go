//go:build integration

package manager

import "testing"

// TestRenewRacesReapSoak repeats the renew-versus-reap race 50 times with real
// worktree git. It costs tens of seconds of CPU under -race, so it is not in
// the unit gate; the pg-gwm-integration-tests check runs it.
func TestRenewRacesReapSoak(t *testing.T) { renewRacesReap(t, 50) }
