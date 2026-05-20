package daemon

import (
	"encoding/json"
	"os"
)

// RuntimeState is the on-disk shape of $XDG_STATE_HOME/pa-monitor/runtime.json.
// Holds toggles that should survive a daemon restart, e.g. user-requested
// caffeinate. Persisted via atomic write (write-tmp + rename).
type RuntimeState struct {
	CaffeinateOn bool `json:"caffeinate_on"`
}

// ReadRuntimeState reads the file at path. A missing file is not an
// error; an empty RuntimeState is returned.
func ReadRuntimeState(path string) (RuntimeState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeState{}, nil
		}
		return RuntimeState{}, err
	}
	var s RuntimeState
	if err := json.Unmarshal(b, &s); err != nil {
		return RuntimeState{}, err
	}
	return s, nil
}

// WriteRuntimeState writes s to path atomically (write-to-tmp + rename).
func WriteRuntimeState(path string, s RuntimeState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
