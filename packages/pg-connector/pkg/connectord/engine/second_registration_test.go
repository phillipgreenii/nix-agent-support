package engine

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
)

// TestWholeSetOverASecondRegistration runs every other test of this package
// again with the second registration, whose entity kind, groups, change kinds,
// roles and views are different invented words: a test that holds a name of the
// first registration as a literal, or code that compares to one, fails here.
func TestWholeSetOverASecondRegistration(t *testing.T) { vocab.RerunUnderSecond(t) }
