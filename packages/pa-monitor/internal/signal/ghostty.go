package signal

// GhosttySignaler is a stub pending Ghostty IPC availability.
type GhosttySignaler struct{}

func (g *GhosttySignaler) Name() string                    { return "ghostty" }
func (g *GhosttySignaler) Detect(pid int) bool             { return false }
func (g *GhosttySignaler) Send(pid int, text string) error { return ErrNotImplemented }
