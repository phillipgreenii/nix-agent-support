// Package launch is the single source of truth for the `claude` invocation.
// Every pool launch — new and resume — ALWAYS includes --plugin-dir; without it
// hooks don't load, the store goes stale, and the waiter hangs. No prompt is
// ever a launch argument; prompts are delivered by send (Plan 3).
package launch

// PermissionMode is claude's --permission-mode value. The valid set is fixed by
// Claude Code (https://code.claude.com/docs/en/permission-modes.md). The zero
// value ("") means "no --permission-mode flag", preserving claude's own default
// for an interactive, no-flag launch. bypassPermissions is the synonym for the
// old --dangerously-skip-permissions and is REQUIRED for dispatched
// (non-interactive) workers: without it claude stalls on the first
// tool-permission prompt that no human will answer.
type PermissionMode string

const (
	ModeDefault           PermissionMode = "default"
	ModeAcceptEdits       PermissionMode = "acceptEdits"
	ModePlan              PermissionMode = "plan"
	ModeAuto              PermissionMode = "auto"
	ModeDontAsk           PermissionMode = "dontAsk"
	ModeBypassPermissions PermissionMode = "bypassPermissions"
)

// ValidPermissionModes returns the documented --permission-mode values, in docs
// order. Callers validate user input against this set.
func ValidPermissionModes() []PermissionMode {
	return []PermissionMode{ModeDefault, ModeAcceptEdits, ModePlan, ModeAuto, ModeDontAsk, ModeBypassPermissions}
}

// Valid reports whether m is one of the documented --permission-mode values. The
// empty string (zero value) is NOT valid — it is the "omit the flag" sentinel,
// distinct from an explicit, unknown value a caller must reject.
func (m PermissionMode) Valid() bool {
	for _, v := range ValidPermissionModes() {
		if m == v {
			return true
		}
	}
	return false
}

// Spec carries everything needed to build a launch command.
type Spec struct {
	ClaudeBin string // resolved path to the claude binary (or a fake-claude stub in tests)
	// ClaudeSessionID is the Claude session UUID: passed via --session-id (new)
	// and as the --resume target (resume), so a resume is EXACT and never opens
	// Claude's session picker (ADR 0015).
	ClaudeSessionID string
	Name            string // optional display label; --name (new only), omitted when empty
	PluginDir       string // ccpool-plugin store path; ALWAYS appended
	Model           string // optional

	// PermissionMode emits --permission-mode <value> when non-empty; the zero
	// value omits the flag.
	PermissionMode PermissionMode
	// AllowedTools emits --allowed-tools <value> when non-empty (a passthrough
	// allowlist forwarded verbatim to claude, e.g. "Bash(git *),Edit"). The zero
	// value omits the flag. Paired with PermissionMode=dontAsk it makes a
	// non-interactive worker auto-DENY any tool outside the list instead of
	// stalling on a permission prompt.
	AllowedTools string
	// Effort emits --effort <value> (e.g. "max") when non-empty.
	Effort string
}

// BuildNew builds the argv for a brand-new session, addressing the new Claude
// session by --session-id <claude_session_id>. --name is appended only when a
// display label is supplied (it is optional, ADR 0015).
func BuildNew(s Spec) []string {
	args := []string{s.ClaudeBin, "--session-id", s.ClaudeSessionID}
	if s.Name != "" {
		args = append(args, "--name", s.Name)
	}
	args = append(args, "--plugin-dir", s.PluginDir)
	return appendFlags(args, s)
}

// BuildResume builds the argv to resume an existing session by its
// claude_session_id (ADR 0015) — exact, never the picker.
func BuildResume(s Spec) []string {
	args := []string{s.ClaudeBin, "--resume", s.ClaudeSessionID, "--plugin-dir", s.PluginDir}
	return appendFlags(args, s)
}

// appendFlags appends the optional claude launch flags shared by new and resume,
// in a fixed order: --permission-mode <value>, --allowed-tools <value>,
// --effort <value>, --model <value>. Each is omitted when unset (empty string).
func appendFlags(args []string, s Spec) []string {
	if s.PermissionMode != "" {
		args = append(args, "--permission-mode", string(s.PermissionMode))
	}
	if s.AllowedTools != "" {
		args = append(args, "--allowed-tools", s.AllowedTools)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	return args
}
