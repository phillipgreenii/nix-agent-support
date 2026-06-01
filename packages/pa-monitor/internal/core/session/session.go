package session

import (
	"strings"
	"time"
)

type Status int

const (
	Working Status = iota
	Idle
	Dormant
)

func (s Status) String() string {
	switch s {
	case Working:
		return "working"
	case Idle:
		return "idle"
	case Dormant:
		return "dormant"
	}
	return "unknown"
}

type Session struct {
	PID             int
	SessionID       string
	Cwd             string
	Kind            string
	Entrypoint      string
	Name            string
	Branch          string
	StartedAt       time.Time
	TerminalHost    string // populated by poller: "tmux","ghostty","vscode","unknown"
	TranscriptMTime time.Time
	Status          Status

	// Env is the process environment of the agent process, populated by the
	// poller via per-OS readers. Empty when the env could not be read (dead
	// pid, permission denied, unsupported OS). Detectors must tolerate
	// nil/empty maps. Static for the lifetime of a session — read once,
	// reused.
	Env map[string]string
}

// Label returns the display label. If forceID is true, returns the full SessionID.
// Otherwise returns Name when set, else the first 8 chars of SessionID.
func (s *Session) Label(forceID bool) string {
	if forceID {
		return s.SessionID
	}
	if s.Name != "" {
		return s.Name
	}
	if len(s.SessionID) >= 8 {
		return s.SessionID[:8]
	}
	return s.SessionID
}

// TranscriptPath returns the expected ~/.claude/projects/<slug>/<id>.jsonl path.
// claudeHome must point to ~/.claude (without a trailing slash).
func (s *Session) TranscriptPath(claudeHome string) string {
	return claudeHome + "/projects/" + slugify(s.Cwd) + "/" + s.SessionID + ".jsonl"
}

// slugify mirrors Claude Code's on-disk project-directory naming: both "/" and
// "_" in the cwd become "-". Example: "/Users/a/b_c" → "-Users-a-b-c".
func slugify(cwd string) string {
	return strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
}

// Classify maps an mtime to a Status given thresholds.
// If now-mtime <= working, Working. Else if <= idle, Idle. Else Dormant.
func Classify(now, mtime time.Time, working, idle time.Duration) Status {
	age := now.Sub(mtime)
	switch {
	case age <= working:
		return Working
	case age <= idle:
		return Idle
	default:
		return Dormant
	}
}

// TerminalAbbrev maps the daemon-reported terminal_host string into the short
// abbreviations used in CLI tables and dashboard rows: CMUX/TMUX/GHOSTTY/VSCODE/UNKN.
// The cmux refinements ("cmux (bridge disconnected)" etc.) collapse to CMUX so the
// column width stays bounded. Shared between the CLI status formatter and the
// daemon's per-session OTel emitter so a single source-of-truth maps the value.
func TerminalAbbrev(host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.HasPrefix(host, "cmux"):
		return "CMUX"
	case host == "tmux":
		return "TMUX"
	case host == "ghostty":
		return "GHOSTTY"
	case host == "vscode":
		return "VSCODE"
	default:
		return "UNKN"
	}
}
