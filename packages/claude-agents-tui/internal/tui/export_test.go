package tui

// SignalLogForTest is a whitebox test hook exposing signalLog. Not for production use.
func (m *Model) SignalLogForTest(msg string) { m.signalLog(msg) }
