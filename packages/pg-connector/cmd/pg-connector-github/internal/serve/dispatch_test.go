package serve_test

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The drift guard: the ops a client lists in capabilities are exactly the ops
// the assembled daemon registers for that instance, plus capabilities, which
// only the client answers. A client table that grows without the daemon (or the
// reverse) fails here instead of in the field.
func TestClientCapabilitiesMatchTheAssembledDaemonsOps(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{})
	p := runInProcess(t, d, nil)
	defer p.stop(t)

	for _, inst := range []client.Instance{client.PR(), client.CI()} {
		got := p.rt.Ops(inst.Name)
		want := slices.DeleteFunc(inst.OpNames(), func(op string) bool { return op == scriptout.OpCapabilities })
		if !slices.Equal(got, want) {
			t.Errorf("%s: the daemon registers %v, the client lists %v (without capabilities)", inst.Name, got, want)
		}
		// capabilities is answered by the client alone, never forwarded to a
		// daemon handler.
		if slices.Contains(got, scriptout.OpCapabilities) {
			t.Errorf("%s: the daemon must not register capabilities", inst.Name)
		}
		if !slices.Contains(inst.OpNames(), scriptout.OpCapabilities) {
			t.Errorf("%s: the client must list capabilities", inst.Name)
		}
	}
	// Both instances are declared, so a not-yet-delivered op is unknown_op and
	// not an unknown instance.
	if serve.Instances[0] != "pr" || serve.Instances[1] != "ci" {
		t.Errorf("instances = %v", serve.Instances)
	}
}
