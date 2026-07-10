package session

import (
	"strings"
	"time"
)

// Status is the observable session status (ADR 0024 D1). It answers "can this
// session make progress right now, and does it have work?" and is a closed
// enum of exactly three values on every surface (CLI, TUI, OTEL), the DB, and
// the wire: working | blocked | idle. Dormant is NO LONGER a status — a
// long-idle session is Idle with the LongIdle age refinement (see below).
// "waiting for a human" is NO LONGER a status — it is Blocked with a human
// Blocker.
type Status int

const (
	Working Status = iota
	// Blocked: has work but cannot proceed; the reason is the Blocker.
	Blocked
	Idle
)

func (s Status) String() string {
	switch s {
	case Working:
		return "working"
	case Blocked:
		return "blocked"
	case Idle:
		return "idle"
	}
	return "unknown"
}

// Blocker names the reason a Blocked session cannot proceed (ADR 0024 D1). It
// is present ONLY when Status == Blocked; NoBlocker (the zero value) renders as
// the empty string so it is absent on the wire / in the DB when not blocked.
type Blocker int

const (
	// NoBlocker is the zero value: no blocker (Status != Blocked).
	NoBlocker Blocker = iota
	// HumanInput: awaiting human input (AskUserQuestion / permission prompt).
	HumanInput
	// HumanAuthn: awaiting human re-authentication (e.g. HTTP 401).
	HumanAuthn
	// UsageLimit: a terminal usage-limit error on the session (rate-limit 429 /
	// spend-limit) or a non-zero, still-in-future RateLimitResetsAt. Derived
	// from per-session inputs ONLY, never the account-global FiveHourPct
	// (ADR 0024 R2).
	UsageLimit
	// ErrorBlocker: any other terminal blocking error (server/network/
	// model-unavailable); retryability is carried by LastError.
	ErrorBlocker
)

func (b Blocker) String() string {
	switch b {
	case HumanInput:
		return "human_input"
	case HumanAuthn:
		return "human_authn"
	case UsageLimit:
		return "usage_limit"
	case ErrorBlocker:
		return "error"
	}
	return ""
}

// IsHuman reports whether the blocker requires a human to clear it directly
// (the human_* family). Used by the nudger/TUI to suppress auto-resume nudges.
func (b Blocker) IsHuman() bool {
	return b == HumanInput || b == HumanAuthn
}

// ParseBlocker maps a stored/wire blocker string back to a Blocker. The empty
// string (and any unknown value) maps to NoBlocker.
func ParseBlocker(s string) Blocker {
	switch s {
	case "human_input":
		return HumanInput
	case "human_authn":
		return HumanAuthn
	case "usage_limit":
		return UsageLimit
	case "error":
		return ErrorBlocker
	}
	return NoBlocker
}

// AwaitsHuman is the derived predicate (ADR 0024 D1): true when only a human
// can clear the blocker — a human_* blocker, OR an error blocker that is not
// retryable. It is computed where needed (keepAwake, "blocked on human"
// rollups) so the granular Blocker identity is never lost.
func AwaitsHuman(b Blocker, retryable bool) bool {
	switch b {
	case HumanInput, HumanAuthn:
		return true
	case ErrorBlocker:
		return !retryable
	default:
		return false
	}
}

// KeepAwake is the ADR 0024 D2 keep-awake predicate over the new model: keep
// the Mac awake when the session is Working, OR Blocked on a machine-
// recoverable blocker (usage_limit — auto-resume fires at reset — or a
// retryable error). Allow sleep when idle or awaitsHuman.
func KeepAwake(s Status, b Blocker, retryable bool) bool {
	switch s {
	case Working:
		return true
	case Blocked:
		return !AwaitsHuman(b, retryable)
	default:
		return false
	}
}

// LongIdleThreshold is the display-side age cutoff (ADR 0024: Dormant becomes
// an idle age-refinement). An Idle session whose last activity is older than
// this renders as "dormant" (✕) and is hidden from the default TUI view /
// excluded from the per-session session.info gauge. It mirrors the daemon's
// default IdleThreshold (config default 10m); it is a display default, not the
// authoritative per-poll threshold (the poller uses its configured value).
const LongIdleThreshold = 10 * time.Minute

// IsLongIdle reports whether lastActivity is older than the threshold. Zero
// lastActivity is never long-idle (unknown age).
func IsLongIdle(now, lastActivity time.Time, threshold time.Duration) bool {
	return !lastActivity.IsZero() && now.Sub(lastActivity) > threshold
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
	// Blocker is the reason a Blocked session cannot proceed; NoBlocker when
	// Status != Blocked. Derived once in the poller alongside Status.
	Blocker Blocker
	// LongIdle is the age refinement that replaced the Dormant status: an Idle
	// session whose last activity is older than the idle threshold. Set by the
	// poller (which also applies the live-PID clamp) for the OTEL/session.info
	// live path; display clients derive it from TranscriptMTime instead.
	LongIdle bool

	// RegistryStatus / WaitingFor / StatusUpdatedAt mirror the raw
	// ~/.claude/sessions/<pid>.json fields ("busy"/"idle"/"waiting", the
	// waitingFor label, and the turn-start marker). They are decoded by the
	// Discoverer and consumed by the poller's registry-driven activity
	// verdict. Status (above) is the derived verdict; these are the inputs.
	RegistryStatus  string
	WaitingFor      string
	StatusUpdatedAt time.Time

	// Env is the process environment of the agent process, populated by the
	// poller via per-OS readers. Empty when the env could not be read (dead
	// pid, permission denied, unsupported OS). Detectors must tolerate
	// nil/empty maps. Static for the lifetime of a session — read once,
	// reused.
	Env map[string]string

	// PidAlive is set by the Discoverer based on its PidAlive function.
	// Used by the poller to decide whether to write a non-NULL pid to the DB.
	PidAlive bool
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

// Classify maps an mtime age to a coarse activity bucket for the dead-pid
// fallback: Working when within the working window, else Idle. Dormancy is no
// longer a status — it is the LongIdle age refinement (see IsLongIdle). The
// idle argument is retained for signature stability but no longer selects a
// distinct bucket.
func Classify(now, mtime time.Time, working, idle time.Duration) Status {
	if now.Sub(mtime) <= working {
		return Working
	}
	return Idle
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
