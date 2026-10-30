package render

import (
	"errors"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// IsDaemonOffline reports whether err says the recorder is not answering. The
// error crosses the daemon-to-client wire as its text, so the text matches too.
func IsDaemonOffline(err error) bool {
	return err != nil && (errors.Is(err, session.ErrDaemonOffline) || err.Error() == session.ErrDaemonOffline.Error())
}
