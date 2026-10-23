package groupsvc

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// keepAlive is summary.Service as the shared service's retention rule: the
// summary service's own keep-alive rule (accessed within the keep-alive window)
// and access bookkeeping. This is the one seam the keep-alive rewrite changes.
type keepAlive struct{ s *summary.Service }

var _ sharedsvc.KeepAlive = keepAlive{}

// Retain is summary.Service.InKeepAlive.
func (k keepAlive) Retain(ctx context.Context, id string) (bool, error) {
	return k.s.InKeepAlive(ctx, id)
}

// Access is summary.Service.Access.
func (k keepAlive) Access(ctx context.Context, id string) error { return k.s.Access(ctx, id) }
