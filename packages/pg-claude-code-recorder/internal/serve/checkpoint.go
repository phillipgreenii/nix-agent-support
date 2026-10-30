package serve

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// checkpointName is the watcher's checkpoint inside the state directory.
const checkpointName = "watcher.checkpoint.json"

// fileCheckpoint implements watcher.Checkpoint over the state directory. Save
// is called after every drained batch of events, so it coalesces: it writes at
// most once per minGap and Flush writes whatever is pending. A lost checkpoint
// only costs a sweep at the next start.
type fileCheckpoint struct {
	path   string
	minGap time.Duration

	mu      sync.Mutex
	pending *watcher.State
	last    time.Time
}

type checkpointJSON struct {
	EventID    uint64    `json:"event_id"`
	EventTime  time.Time `json:"event_time"`
	VolumeUUID string    `json:"volume_uuid"`
	LastSweep  time.Time `json:"last_sweep"`
}

func newFileCheckpoint(stateDir string) *fileCheckpoint {
	return &fileCheckpoint{path: filepath.Join(stateDir, checkpointName), minGap: time.Second}
}

// Load reads the checkpoint; a missing or unreadable file is "no checkpoint".
func (c *fileCheckpoint) Load() (watcher.State, error) {
	b, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return watcher.State{}, nil
	}
	if err != nil {
		return watcher.State{}, err
	}
	var j checkpointJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return watcher.State{}, err
	}
	return watcher.State{EventID: j.EventID, EventTime: j.EventTime, VolumeUUID: j.VolumeUUID, LastSweep: j.LastSweep}, nil
}

// Save records st, writing it now when the last write is older than minGap.
func (c *fileCheckpoint) Save(st watcher.State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = &st
	if time.Since(c.last) < c.minGap {
		return nil
	}
	return c.writeLocked()
}

// Flush writes a pending checkpoint.
func (c *fileCheckpoint) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return nil
	}
	return c.writeLocked()
}

func (c *fileCheckpoint) writeLocked() error {
	st := *c.pending
	b, err := json.Marshal(checkpointJSON{EventID: st.EventID, EventTime: st.EventTime, VolumeUUID: st.VolumeUUID, LastSweep: st.LastSweep})
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.pending = nil
	c.last = time.Now()
	return nil
}
