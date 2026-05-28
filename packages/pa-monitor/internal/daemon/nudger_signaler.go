package daemon

import (
	"fmt"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// SignalerAdapter wraps the signal package's per-PID resolver so the
// nudger package doesn't import internal/signal directly.
type SignalerAdapter struct {
	Signalers []signal.Signaler
}

func (s *SignalerAdapter) Send(pid int, text string) error {
	sig := signal.ResolveSignaler(s.Signalers, pid)
	if sig == nil {
		return fmt.Errorf("no signaler for pid %d", pid)
	}
	return sig.Send(pid, text)
}
