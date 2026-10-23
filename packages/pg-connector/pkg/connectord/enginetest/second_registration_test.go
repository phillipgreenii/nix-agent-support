package enginetest

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
)

// TestWholeSetOverASecondRegistration runs every other test of this package
// again over the second registration (different invented names and role words).
func TestWholeSetOverASecondRegistration(t *testing.T) { vocab.RerunUnderSecond(t) }
