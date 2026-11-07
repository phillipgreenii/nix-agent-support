//go:build live

package cli

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/doctor"
)

// TestLiveDoctorSessionResolvesAgentQ runs every doctor check against the real
// machine, including the in-session check, which starts one minimal ccpool
// session (no analysis). Run it on demand:
//
//	go test -tags live -run TestLiveDoctor -timeout 300s ./internal/cli/
func TestLiveDoctorSessionResolvesAgentQ(t *testing.T) {
	results := doctor.Run(context.Background(), doctorDeps(context.Background(), false))
	for _, r := range results {
		t.Log(r.String())
	}
	if r := results[len(results)-1]; r.Name != doctor.CheckSession || r.Status != doctor.Pass {
		t.Fatalf("in-session check: %s", r)
	}
}
