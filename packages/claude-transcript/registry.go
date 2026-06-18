package claudetranscript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// RegistrySession is one row of Claude Code's per-process session registry,
// stored as ~/.claude/sessions/<pid>.json. Claude Code writes this file for the
// whole lifetime of a CLI process and updates status/statusUpdatedAt as the turn
// state changes.
//
// Caveats (observed against the local corpus, document for consumers):
//   - The registry is keyed by PID, so a "busy" row can survive a crash (the
//     process is gone but the file lingers until GC). ALWAYS pid-gate the row
//     before trusting "busy" — see PidAlive / ClassifyActivity's contract.
//   - Status "waiting" goes stale: a "permission prompt" wait was observed stuck
//     for 16+ hours after the human moved on (the transcript kept advancing).
//     Cross-check freshness against transcript message activity before trusting
//     it — see ClassifyActivity.
//   - StatusUpdatedAt is a turn-START marker, NOT a heartbeat: a genuine 16-min
//     turn has a 16-min-old StatusUpdatedAt. Do NOT treat an old StatusUpdatedAt
//     as "stale" for "busy".
//   - WaitingFor: only "permission prompt" has been observed (the field was
//     added in CC 2.1.162); it is frequently absent (JSON null) for "waiting".
//     Unknown values are kept verbatim rather than rejected.
//   - Name and WaitingFor are JSON null when unset; they decode to "".
type RegistrySession struct {
	PID             int
	SessionID       string
	Cwd             string
	Name            string
	Kind            string
	Entrypoint      string
	StartedAt       time.Time
	Status          string
	WaitingFor      string
	StatusUpdatedAt time.Time
}

// rawRegistrySession mirrors the on-disk JSON. Epoch fields are ms-since-epoch.
// Unknown extra fields (procStart, version, peerProtocol, updatedAt, …) are
// ignored. status/waitingFor are kept as raw strings so unexpected values are
// surfaced rather than dropped.
type rawRegistrySession struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Entrypoint      string `json:"entrypoint"`
	StartedAt       int64  `json:"startedAt"`       // ms epoch
	Status          string `json:"status"`          // busy | idle | waiting (raw)
	WaitingFor      string `json:"waitingFor"`      // e.g. "permission prompt" (raw, may be absent)
	StatusUpdatedAt int64  `json:"statusUpdatedAt"` // ms epoch
}

func (r rawRegistrySession) toSession() RegistrySession {
	s := RegistrySession{
		PID:        r.PID,
		SessionID:  r.SessionID,
		Cwd:        r.Cwd,
		Name:       r.Name,
		Kind:       r.Kind,
		Entrypoint: r.Entrypoint,
		Status:     r.Status,
		WaitingFor: r.WaitingFor,
	}
	if r.StartedAt != 0 {
		s.StartedAt = time.UnixMilli(r.StartedAt)
	}
	if r.StatusUpdatedAt != 0 {
		s.StatusUpdatedAt = time.UnixMilli(r.StatusUpdatedAt)
	}
	return s
}

// ReadSessionFile parses a single registry file at path. A malformed or
// unreadable file returns a non-nil error; callers that sweep a directory should
// prefer ReadSessionRegistry, which skips such files (mirroring the discovery
// semantics in pa-monitor).
func ReadSessionFile(path string) (RegistrySession, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return RegistrySession{}, err
	}
	var r rawRegistrySession
	if err := json.Unmarshal(body, &r); err != nil {
		return RegistrySession{}, err
	}
	return r.toSession(), nil
}

// ReadSessionRegistry reads every *.json file under sessionsDir and returns one
// RegistrySession per parseable file. Malformed or unreadable files are silently
// skipped (mirroring pa-monitor's discovery semantics), so a single corrupt file
// never fails the whole sweep. A missing directory yields a nil slice and no
// error. Directories and non-.json entries are ignored.
//
// The returned rows are NOT pid-gated; callers must reject stale "busy" rows
// from dead PIDs themselves (see PidAlive).
func ReadSessionRegistry(sessionsDir string) ([]RegistrySession, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RegistrySession
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		s, err := ReadSessionFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			continue // skip malformed, mirror discovery semantics
		}
		out = append(out, s)
	}
	return out, nil
}

// Registry status values observed in the wild. Kept as plain string constants
// (not a closed enum) because Claude Code may add values and the reader keeps
// raw strings; these document the known set.
const (
	StatusBusy    = "busy"
	StatusIdle    = "idle"
	StatusWaiting = "waiting"
)

// Activity is the normalized, registry-derived session activity verdict shared
// by pa-monitor and ccpool. It is the corroborated meaning of the raw registry
// Status (after freshness + AskUserQuestion cross-checks), NOT the raw field.
type Activity int

const (
	// Idle: the session finished its turn (status "idle"), or a stale "waiting"
	// flag that fell through the freshness check.
	Idle Activity = iota
	// Active: a turn is in progress (status "busy"). Trusted once pid-gated; a
	// stale main transcript is expected during a subagent run.
	Active
	// WaitingForHuman: blocked on a human (a fresh "waiting" flag, or a dangling
	// AskUserQuestion). Suppress both keep-awake and nudges in this state.
	WaitingForHuman
)

// String renders the Activity using the spec's verdict names.
func (a Activity) String() string {
	switch a {
	case Active:
		return "active"
	case WaitingForHuman:
		return "waiting-for-human"
	default:
		return "idle"
	}
}

// Wait reasons returned alongside WaitingForHuman. "" otherwise.
const (
	// WaitReasonPermissionPrompt: registry status=="waiting" with
	// waitingFor=="permission prompt" (or any non-empty waitingFor, kept raw).
	WaitReasonPermissionPrompt = "permission prompt"
	// WaitReasonAskUserQuestion: a dangling AskUserQuestion (awaitingInput).
	WaitReasonAskUserQuestion = "AskUserQuestion"
	// WaitReasonUnknown: waiting-for-human with no labelled cause.
	WaitReasonUnknown = "unknown"
)

// ActivityVerdict is the result of ClassifyActivity: the normalized state plus,
// when WaitingForHuman, the best-effort wait reason (empty otherwise).
type ActivityVerdict struct {
	Activity Activity
	// Reason is set only when Activity == WaitingForHuman. One of the
	// WaitReason* constants. For status=="waiting" with a non-standard
	// (non-empty) waitingFor, Reason carries the raw waitingFor verbatim.
	Reason string
}

// ClassifyActivity is a PURE function (no I/O) that maps a registry row plus two
// already-computed transcript signals to the normalized activity verdict. It
// implements the spec's §4.2/§4.3 precedence exactly:
//
//	waiting-for-human if (reg.Status=="waiting" AND the waiting flag is FRESH)
//	                  OR awaitingInput
//	else Active       if reg.Status=="busy"   (TRUSTED — never demoted on staleness)
//	else Idle
//
// Inputs:
//   - reg: the registry row.
//   - awaitingInput: result of IsAwaitingInput on the main transcript (a dangling
//     AskUserQuestion). This arm is INTENTIONALLY NOT freshness-gated — a dangling
//     question is a real unanswered question until it is answered.
//   - lastActivity: timestamp of the last real transcript MESSAGE event (see
//     LastMessageActivity). Used only for the "waiting" freshness check; mtime is
//     an acceptable proxy.
//   - freshWindow: caller-supplied tolerance. The waiting flag is FRESH iff the
//     transcript has NOT advanced well past StatusUpdatedAt, i.e.
//     lastActivity <= StatusUpdatedAt + freshWindow. (The stale-waiting failure
//     mode: status set at 00:13 but the transcript advanced to 03:43 = stale.)
//     This is a library mechanism; the policy value is the caller's to pick.
//
// Pid-liveness is the CALLER's responsibility: pass an already-gated row (e.g.
// gate on PidAlive before calling). ClassifyActivity itself stays pure so it is
// unit-testable and shared. "busy" is trusted here and never demoted on
// transcript staleness — a busy main transcript is expected to be stale while a
// subagent runs, and demoting it reintroduces the incident bug.
func ClassifyActivity(reg RegistrySession, awaitingInput bool, lastActivity time.Time, freshWindow time.Duration) ActivityVerdict {
	// waiting-for-human (suppresses keep-awake + nudges)
	waitingFresh := reg.Status == StatusWaiting && waitingFlagFresh(reg.StatusUpdatedAt, lastActivity, freshWindow)
	if waitingFresh || awaitingInput {
		return ActivityVerdict{Activity: WaitingForHuman, Reason: waitReason(reg, awaitingInput, waitingFresh)}
	}
	// active / working — TRUST busy (pid already gated by the caller); a stale
	// main transcript is EXPECTED during a subagent run, so do NOT demote.
	if reg.Status == StatusBusy {
		return ActivityVerdict{Activity: Active}
	}
	// done — status=="idle", or a stale "waiting" that fell through.
	return ActivityVerdict{Activity: Idle}
}

// waitingFlagFresh reports whether the registry "waiting" flag is still
// trustworthy: the transcript has NOT advanced well past statusUpdatedAt. A zero
// statusUpdatedAt (field absent) is treated as fresh (there is no advancement to
// measure against — fall back to trusting the flag rather than silently
// demoting). A zero lastActivity (no message activity found) is also fresh: with
// nothing to corroborate against, the flag stands.
func waitingFlagFresh(statusUpdatedAt, lastActivity time.Time, freshWindow time.Duration) bool {
	if statusUpdatedAt.IsZero() || lastActivity.IsZero() {
		return true
	}
	return !lastActivity.After(statusUpdatedAt.Add(freshWindow))
}

// waitReason picks the wait-reason label for a WaitingForHuman verdict, in
// precedence order matching the waiting-for-human OR above.
func waitReason(reg RegistrySession, awaitingInput, waitingFresh bool) string {
	if waitingFresh && reg.WaitingFor != "" {
		return reg.WaitingFor // typically "permission prompt"; raw verbatim
	}
	if awaitingInput {
		return WaitReasonAskUserQuestion
	}
	return WaitReasonUnknown
}

// PidAlive reports whether pid is a live process (kill -0 semantics). This is a
// convenience helper for consumers that need to pid-gate registry rows before
// calling ClassifyActivity (which is pure and does not gate). A non-positive pid
// is never alive. pa-monitor already has its own DefaultPidAlive; this exists so
// other consumers (e.g. ccpool) do not have to reimplement it.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// metadataEventTypes are trailing-record types that do NOT count as human/user
// or assistant MESSAGE activity. They are bookkeeping records Claude Code writes
// around real turns (some, like queue-operation, even carry a timestamp), so
// filtering by type — not by timestamp presence — is required.
var metadataEventTypes = map[string]bool{
	"mode":                  true,
	"permission-mode":       true,
	"last-prompt":           true,
	"custom-title":          true,
	"ai-title":              true,
	"agent-name":            true,
	"pr-link":               true,
	"queue-operation":       true,
	"file-history-snapshot": true,
	"system":                true, // includes turn_duration / stop_hook_summary / api_error / local_command
	"attachment":            true,
}

// LastMessageActivity returns the timestamp of the last REAL assistant/user
// MESSAGE event in the transcript, scanning from the end and skipping trailing
// metadata records (mode, permission-mode, last-prompt, custom-title, ai-title,
// agent-name, pr-link, queue-operation, file-history-snapshot, system/*) as well
// as api-error synthetic assistant events (which are not human/user activity —
// matching the IsTerminal "following event" logic in LastAPIError). ok is false
// when no such event exists.
//
// This feeds the waiting-freshness check in ClassifyActivity: it answers "when
// did the conversation last actually advance?", which is what tells a genuine
// fresh "waiting" apart from one the human abandoned hours ago. File mtime is an
// acceptable proxy when a finer answer is not needed.
func LastMessageActivity(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	type scan struct {
		Type              string    `json:"type"`
		Timestamp         time.Time `json:"timestamp"`
		IsApiErrorMessage bool      `json:"isApiErrorMessage"`
	}

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		b := make([]byte, len(sc.Bytes()))
		copy(b, sc.Bytes())
		lines = append(lines, b)
	}
	if sc.Err() != nil {
		return time.Time{}, false
	}

	for i := len(lines) - 1; i >= 0; i-- {
		var ev scan
		if err := json.Unmarshal(lines[i], &ev); err != nil {
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue // metadata / system / anything non-message
		}
		// api-error synthetic assistant events are not real activity.
		if ev.Type == "assistant" && ev.IsApiErrorMessage {
			continue
		}
		if ev.Timestamp.IsZero() {
			continue // no usable timestamp; keep scanning back
		}
		return ev.Timestamp, true
	}
	return time.Time{}, false
}
