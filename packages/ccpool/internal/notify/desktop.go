package notify

import "github.com/gen2brain/beeep"

// Desktop posts a native OS notification (spec §10 — local-workstation case).
type Desktop struct{}

func (Desktop) Notify(e Event) error {
	title := "ccpool: " + e.Name
	body := e.State
	if e.CWD != "" {
		body += " — " + e.CWD
	}
	return beeep.Notify(title, body, "")
}
