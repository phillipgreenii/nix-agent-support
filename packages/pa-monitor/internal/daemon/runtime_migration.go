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
// migration must still read them out of any legacy file.
//
// Fields are *bool so absence is distinguishable from false: a post-migration
// runtime.json holds only nudger state (no toggle keys → both nil) and must NOT
// be treated as a legacy file, or the migration would zero the DB toggles and
// delete the live nudger-state file on every boot (the WatermarkStore re-creates
// the file each run, so the migration sees it every time).
type legacyRuntimeToggles struct {
	CaffeinateOn      *bool `json:"caffeinate_on"`
	AutoResumeEnabled *bool `json:"auto_resume_enabled"`
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

// MigrateRuntimeJSON performs the one-shot legacy-runtime.json -> ToggleStore
// migration. If the file is a legacy file (carries caffeinate_on and/or
// auto_resume_enabled keys), it copies the present toggle values into the
// ToggleStore and deletes the file. A missing file — or a post-migration file
// that carries only nudger state (no toggle keys) — is a no-op: the file is
// left untouched and the toggle store is not written.
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

	// No toggle keys at all → this is a post-migration nudger-state file, not a
	// legacy file. Leave it completely alone: writing the toggle store would
	// zero the user's real DB values, and removing the file would wipe live
	// nudger state — both on every boot.
	if rs.CaffeinateOn == nil && rs.AutoResumeEnabled == nil {
		return nil
	}

	// Genuine legacy file: migrate the toggle keys that are present.
	if rs.CaffeinateOn != nil {
		if err := ts.Set(ctx, "caffeinate_on", *rs.CaffeinateOn); err != nil {
			return fmt.Errorf("set caffeinate_on: %w", err)
		}
	}
	if rs.AutoResumeEnabled != nil {
		if err := ts.Set(ctx, "auto_resume_enabled", *rs.AutoResumeEnabled); err != nil {
			return fmt.Errorf("set auto_resume_enabled: %w", err)
		}
	}

	// Nudge watermark rows are intentionally not seeded here — see function
	// doc comment for rationale (option b).

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove runtime.json: %w", err)
	}
	return nil
}
