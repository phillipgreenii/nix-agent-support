package store

import "context"

// ToggleStore persists boolean daemon-wide toggles.
// Known keys: "caffeinate_on", "auto_resume_enabled".
type ToggleStore interface {
	Get(ctx context.Context, name string) (bool, bool, error) // value, present, err
	Set(ctx context.Context, name string, value bool) error
	All(ctx context.Context) (map[string]bool, error)
}
