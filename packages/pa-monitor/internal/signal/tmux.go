package signal

// TmuxSignaler names the tmux terminal environment. It classifies nothing and
// delivers nothing: the terminal host comes from the recorder's host fact and
// nudges go through the control mod. It remains only as a named entry in the
// signaler list that the startup binary check walks.
type TmuxSignaler struct{}

func (t *TmuxSignaler) Name() string { return "tmux" }

// RequiredBinaries reports the executables the tmux integration needs. Used by
// the daemon's startup check.
func (t *TmuxSignaler) RequiredBinaries() []string { return []string{"tmux"} }
