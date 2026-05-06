package signal

// CmuxSignaler is a stub. cmux has send-keys capability but requires a
// dedicated process running inside cmux first due to socket API constraints.
// Detect and Send will be wired up once the cmux IPC mechanism is confirmed.
type CmuxSignaler struct{}

func (c *CmuxSignaler) Name() string                    { return "cmux" }
func (c *CmuxSignaler) Detect(pid int) bool             { return false }
func (c *CmuxSignaler) Send(pid int, text string) error { return ErrNotImplemented }
