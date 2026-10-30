package settle

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// Remedies is the remedy line of each held reason code (the stable enum the held
// command prints).
var Remedies = map[string]string{
	ReasonCopyOffset:      "the copy is behind or ahead of the Claude file; wait for the copier, or run doctor",
	ReasonCopyTail:        "the Claude file was edited in place; run doctor to start a new generation",
	ReasonParseBehind:     "the parser has not caught up; wait, or check coverage for a parser error",
	ReasonUnparsed:        "a file of a parsed kind was never parsed; update the parser for it, then held --retry",
	ReasonTornTooLong:     "a torn tail longer than parse.maxLineBytes; raise parse.maxLineBytes or inspect the file",
	ReasonRestoreConflict: "a new file appeared at a path removal had quarantined; compare the two, then remove the quarantined copy by hand",
}

// HeldRow is one held Conversation.
type HeldRow struct {
	Session  string    `json:"session"`
	Reason   string    `json:"reason"`
	Since    time.Time `json:"since"`
	Files    int       `json:"files"`
	Detail   string    `json:"detail,omitempty"`
	Remedy   string    `json:"remedy"`
	AgeSecs  float64   `json:"age_seconds"`
	Failures []Failure `json:"failures,omitempty"`
}

// Held lists the held Conversations, oldest first.
func (s *Settler) Held() ([]HeldRow, error) {
	rows, err := s.st.Stages()
	if err != nil {
		return nil, err
	}
	now := s.cfg.Clock.Now()
	out := []HeldRow{}
	for _, r := range rows {
		if r.Stage != StageHeld {
			continue
		}
		h := HeldRow{
			Session: r.Session, Reason: r.Reason, Since: time.Unix(0, r.SinceNS).UTC(), Remedy: Remedies[r.Reason],
			AgeSecs: now.Sub(time.Unix(0, r.SinceNS)).Seconds(),
		}
		var fails []Failure
		if json.Unmarshal([]byte(r.Detail), &fails) == nil {
			h.Failures, h.Files = fails, len(fails)
			if len(fails) > 0 {
				h.Detail = fails[0].Path + ": " + fails[0].Detail
			}
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out, nil
}

// WhereInfo is the answer of where <session>.
type WhereInfo struct {
	Session string `json:"session"`
	Stage   string `json:"stage"`
	// BlockedBy is the stable list of reasons the Conversation is not removable.
	BlockedBy []string `json:"blocked_by"`
	// NextEligible is the earliest time the age gate opens (nil when not age-gated).
	NextEligible *time.Time `json:"next_eligible,omitempty"`
	// Clear is the command (or action) that clears the first blocker.
	Clear             string          `json:"clear,omitempty"`
	ArchivePath       string          `json:"archive_path,omitempty"`
	Manifest          json.RawMessage `json:"manifest,omitempty"`
	ParserVersion     int             `json:"parser_version,omitempty"`
	ClaudeCodeVersion string          `json:"claude_code_version,omitempty"`
	RemovalEnabled    bool            `json:"removal_enabled"`
	HeldReason        string          `json:"held_reason,omitempty"`
	HeldRemedy        string          `json:"held_remedy,omitempty"`
}

var clearBy = map[string]string{
	BlockRemovalDisabled: "set remove.enable = true in the config once the removal readiness checklist is done",
	BlockLive:            "end the Claude Process that holds the Conversation",
	BlockRegistry:        "close the Claude whose registry file names the Conversation",
	BlockHeld:            "pg-claude-code-recorder held --retry",
	BlockNotSettled:      "wait: it settles after settle.after of quiet",
	BlockInProgress:      "wait: a removal is finishing; restart the recorder if it stays",
	BlockRemoved:         "pg-claude-code-recorder extract <session> to restore it",
	BlockQuietAge:        "wait: removal needs remove.after of quiet (a write resets the age)",
	BlockFormat:          "update the parser for the new Claude Code format, then pg-claude-code-recorder held --retry",
	BlockNoFiles:         "nothing to remove: no file of the Conversation is in the Claude directory",
}

// Where reports a Conversation's stage, what blocks its removal and the command
// that clears the first blocker.
func (s *Settler) Where(session string) (WhereInfo, error) {
	s.run.Lock()
	defer s.run.Unlock()
	now := s.cfg.Clock.Now()
	info := WhereInfo{Session: session, BlockedBy: []string{}, RemovalEnabled: s.cfg.RemoveEnable}
	live, err := s.cfg.Live()
	if err != nil {
		return info, err
	}
	groups, err := s.groups()
	if err != nil {
		return info, err
	}
	row, hasRow, err := s.st.Stage(session)
	if err != nil {
		return info, err
	}
	c := groups[session]
	settled, hasSettled, err := s.st.SettledOf(session)
	if err != nil {
		return info, err
	}
	if c == nil && !hasRow && !hasSettled {
		return info, store.ErrNotFound
	}
	switch {
	case live[session]:
		info.Stage = StageLive
	case hasRow:
		info.Stage = row.Stage
	default:
		info.Stage = StageQuiet
	}
	if info.Stage == StageHeld {
		info.HeldReason, info.HeldRemedy = row.Reason, Remedies[row.Reason]
	}
	if hasSettled {
		info.ArchivePath = s.st.SegDir(session)
		info.Manifest = json.RawMessage(settled.Manifest)
	}
	if c != nil {
		blocked, next := s.blockers(c, row, hasRow, live[session], now)
		info.BlockedBy = blocked
		if !next.IsZero() {
			info.NextEligible = &next
		}
		s.fillVersions(&info, session)
	} else if hasRow && row.Stage == store.StageRemoved {
		info.BlockedBy = []string{BlockRemoved}
		s.fillVersions(&info, session)
	}
	if !s.cfg.RemoveEnable {
		info.BlockedBy = append([]string{BlockRemovalDisabled}, info.BlockedBy...)
	}
	if len(info.BlockedBy) > 0 {
		info.Clear = clearBy[info.BlockedBy[0]]
		if info.BlockedBy[0] == BlockRemoved {
			info.Clear = fmt.Sprintf("pg-claude-code-recorder extract %s", session)
		}
	}
	_ = filepath.Join
	return info, nil
}

// fillVersions sets the parser version of the Conversation's records and the
// Claude Code version that wrote them (the registry's version field).
func (s *Settler) fillVersions(info *WhereInfo, session string) {
	_ = s.st.Read().QueryRow(`SELECT COALESCE(MAX(parser_version),0) FROM records WHERE session=?`, session).Scan(&info.ParserVersion)
	_ = s.st.Read().QueryRow(`SELECT f.s FROM records r JOIN fields f ON f.rec_id=r.id
WHERE r.source='session-registry' AND r.session=? AND f.name='version' ORDER BY r.id DESC LIMIT 1`, session).Scan(&info.ClaudeCodeVersion)
}
