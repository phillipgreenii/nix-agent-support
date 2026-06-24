package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// legacyRuntimeToggles is the subset of the PRE-migration runtime.json schema
// that carried the user toggles. RuntimeState no longer holds these fields
// (the ToggleStore is their single source of truth), but the one-shot
// migration must still read them out of any legacy file. Kept decoupled from
// RuntimeState so dropping those fields does not change migration behavior.
type legacyRuntimeToggles struct {
	CaffeinateOn      bool `json:"caffeinate_on"`
	AutoResumeEnabled bool `json:"auto_resume_enabled"`
}

// readLegacyRuntimeToggles reads the legacy toggle keys from a pre-migration
// runtime.json. Used only by MigrateRuntimeJSON.
func readLegacyRuntimeToggles(path string) (legacyRuntimeToggles, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return legacyRuntimeToggles{}, err
	}
	var lt legacyRuntimeToggles
	if err := json.Unmarshal(b, &lt); err != nil {
		return legacyRuntimeToggles{}, err
	}
	return lt, nil
}

// MigrateRuntimeJSON reads runtime.json (if present), writes the toggle values
// into ToggleStore, and then deletes the file. A missing file is a no-op.
//
// Nudge watermark seeding is skipped (option b): the NudgeStore requires the
// surrogate int64 session row id, which is not exposed on SessionStore without
// a raw DB query. Because nudge_history starts fresh after migration and the
// daemon re-establishes nudge cooldowns within a few normal ticks, the loss is
// acceptable.
func MigrateRuntimeJSON(
	ctx context.Context,
	path string,
	ts store.ToggleStore,
	_ store.NudgeStore,
	_ store.SessionStore,
) error {
	// Probe for existence before reading so that absence is a true no-op
	// (no writes to the toggle store).
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat runtime.json: %w", err)
	}

	rs, err := readLegacyRuntimeToggles(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read runtime.json: %w", err)
	}

	if err := ts.Set(ctx, "caffeinate_on", rs.CaffeinateOn); err != nil {
		return fmt.Errorf("set caffeinate_on: %w", err)
	}
	if err := ts.Set(ctx, "auto_resume_enabled", rs.AutoResumeEnabled); err != nil {
		return fmt.Errorf("set auto_resume_enabled: %w", err)
	}

	// Nudge watermark rows are intentionally not seeded here — see function
	// doc comment for rationale (option b).

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove runtime.json: %w", err)
	}
	return nil
}
