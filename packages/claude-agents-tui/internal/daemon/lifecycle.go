package daemon

import "context"

// Run is the daemon's main loop. v1 stub — pidfile, socket, and gRPC server
// land in subsequent tasks.
func Run(ctx context.Context, p Paths) error {
	_ = p
	<-ctx.Done()
	return nil
}
