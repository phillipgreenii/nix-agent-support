package notify

import "github.com/gen2brain/beeep"

// Desktop posts a native OS notification. It is the local-workstation adapter
// only: on a headless/remote host the toast reaches nobody, so use `exec` there.
type Desktop struct{}

func (Desktop) Notify(e Event) error {
	title := "ccpool: " + e.Name
	body := e.State
	if e.CWD != "" {
		body += " — " + e.CWD
	}
	return beeep.Notify(title, body, "")
}
