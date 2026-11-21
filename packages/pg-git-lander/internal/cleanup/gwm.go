package cleanup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// PgGwmKeptReason is the reason recorded for a kept pg-gwm worktree; %s is
// the lease name.
const PgGwmKeptReason = "pg-gwm-managed: parked on the placeholder branch; release the lease with pg-gwm release --name %s (or pb drain release)"

// GwmManaged returns the Managed probe over run: it asks pg-gwm
// (`pg-gwm info --path`, the check pb drain uses) whether path is a worktree
// a lease holds. A missing pg-gwm, a failing call or an unmanaged path reads
// false, so a repository without pg-gwm behaves exactly as before.
func GwmManaged(run func(context.Context, cmdrun.Call) cmdrun.Result) func(context.Context, string, string) (string, bool) {
	return func(ctx context.Context, repo, path string) (string, bool) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res := run(cctx, cmdrun.Call{
			Name: "pg-gwm", Args: []string{"info", "--path", path, "--repo", repo, "--format=json"},
		})
		if !res.OK() {
			return "", false
		}
		var l struct {
			Managed bool   `json:"managed"`
			Name    string `json:"name"`
		}
		if json.Unmarshal(res.Stdout, &l) != nil || !l.Managed {
			return "", false
		}
		return l.Name, true
	}
}
