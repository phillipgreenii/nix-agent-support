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
}

// BuildNew builds the argv for a brand-new session.
func BuildNew(s Spec) []string {
	args := []string{s.ClaudeBin, "--session-id", s.UUID, "--name", s.Name, "--plugin-dir", s.PluginDir}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	return args
}

// BuildResume builds the argv to resume an existing session by name.
func BuildResume(s Spec) []string {
	args := []string{s.ClaudeBin, "--resume", s.Name, "--plugin-dir", s.PluginDir}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	return args
}
