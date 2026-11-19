//go:build integration

package client_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// The same table-driven contract the Fake meets in the unit tier, against a real
// daemon over an empty Claude directory.
func TestRealDaemonMeetsTheAPIContract(t *testing.T) {
	clienttest.RunContract(t, func(t *testing.T) client.API {
		return clienttest.Start(t).Dial(ctx())
	})
}
