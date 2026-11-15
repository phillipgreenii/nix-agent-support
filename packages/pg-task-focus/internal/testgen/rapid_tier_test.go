package testgen

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/rapidtier"

// The unit gate runs this package's rapid property tests for 25 checks each
// (rapid's default is 100); the integration tier (-tags integration, the
// pg-task-focus-property-tests check) runs the full count.
var _ = rapidtier.Cap(25)
