package signal

// VSCodeSignaler is a stub pending VS Code terminal IPC availability.
type VSCodeSignaler struct{}

func (v *VSCodeSignaler) Name() string                    { return "vscode" }
func (v *VSCodeSignaler) Detect(pid int) bool             { return false }
func (v *VSCodeSignaler) Send(pid int, text string) error { return ErrNotImplemented }
