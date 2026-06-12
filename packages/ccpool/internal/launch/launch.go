// Package launch is the single source of truth for the `claude` invocation.
// Every pool launch — new and resume — ALWAYS includes --plugin-dir; without it
// hooks don't load and the store goes stale (spec §8.1). No prompt is ever a
// launch argument; prompts are delivered by send (Plan 3).
package launch

// Spec carries everything needed to build a launch command.
type Spec struct {
	ClaudeBin string // resolved path to the claude binary (or a fake-claude stub in tests)
	UUID      string // generated; passed via --session-id (new only)
	Name      string // friendly key; --name (new) / --resume target (resume)
	PluginDir string // ccpool-plugin store path; ALWAYS appended
	Model     string // optional

	// DangerouslySkipPermissions emits --dangerously-skip-permissions. REQUIRED
	// for dispatched (non-interactive) workers: without it claude stalls on the
	// first tool-permission prompt that no human will answer.
	DangerouslySkipPermissions bool
	// Effort emits --effort <value> (e.g. "max") when non-empty.
	Effort string
}

// BuildNew builds the argv for a brand-new session.
func BuildNew(s Spec) []string {
	args := []string{s.ClaudeBin, "--session-id", s.UUID, "--name", s.Name, "--plugin-dir", s.PluginDir}
	return appendFlags(args, s)
}

// BuildResume builds the argv to resume an existing session by name.
func BuildResume(s Spec) []string {
	args := []string{s.ClaudeBin, "--resume", s.Name, "--plugin-dir", s.PluginDir}
	return appendFlags(args, s)
}

// appendFlags appends the optional claude launch flags shared by new and resume,
// in a fixed order: --dangerously-skip-permissions, --effort <value>, --model
// <value>. Each is omitted when unset (false / empty string).
func appendFlags(args []string, s Spec) []string {
	if s.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	return args
}
