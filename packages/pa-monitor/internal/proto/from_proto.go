package proto

import (
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// ToTree is the inverse of FromTree: reconstruct an aggregate.Tree from
// the wire DaemonState. Used by remote clients (TUI, future cmux-bridge)
// to render via existing tree-based helpers.
//
// Lossy by design: TUI uses Tree primarily for rendering, and renders
// the full DaemonState anyway. Fields that the renderer doesn't read
// (PRInfo URL, raw burn-rate window-by-window) are preserved where the
// proto carries them.
func ToTree(s *DaemonState) *aggregate.Tree {
	if s == nil {
		return nil
	}
	t := &aggregate.Tree{
		PlanCapUSD:    s.GetPlanCapUsd(),
		WeekCapUSD:    s.GetWeekCapUsd(),
		CCUsageProbed: s.GetCcusageProbed(),
		GeneratedAt:   timeFromTS(s.GetNow()),
	}
	if e := s.GetCcusageError(); e != "" {
		t.CCUsageErr = errors.New(e)
	}
	if ts := s.GetWindowResetsAt(); ts != nil {
		t.WindowResetsAt = timeFromTS(ts)
	}
	for _, pd := range s.GetDirs() {
		t.Dirs = append(t.Dirs, dirFromProto(pd))
	}
	t.ActiveBlock = blockFromProto(s.GetActiveBlock())
	t.ActiveWeek = weekFromProto(s.GetActiveWeek())
	return t
}

func dirFromProto(pd *Directory) *aggregate.Directory {
	if pd == nil {
		return nil
	}
	d := &aggregate.Directory{
		Path:         pd.GetPath(),
		Branch:       pd.GetBranch(),
		WorkingN:     int(pd.GetWorkingN()),
		IdleN:        int(pd.GetIdleN()),
		DormantN:     int(pd.GetDormantN()),
		TotalTokens:  int(pd.GetTotalTokens()),
		TotalCostUSD: pd.GetTotalCostUsd(),
		BurnRateSum:  pd.GetBurnRateSum(),
	}
	if p := pd.GetPrInfo(); p != nil {
		d.PRInfo = &session.PRInfo{
			Number: int(p.GetNumber()),
			Title:  p.GetTitle(),
			URL:    p.GetUrl(),
		}
	}
	for _, sv := range pd.GetSessions() {
		d.Sessions = append(d.Sessions, sessionViewFromProto(sv))
	}
	return d
}

func sessionViewFromProto(sv *SessionView) *aggregate.SessionView {
	if sv == nil {
		return nil
	}
	out := &aggregate.SessionView{
		Session: &session.Session{
			PID:          int(sv.GetPid()),
			SessionID:    sv.GetSessionId(),
			Cwd:          sv.GetCwd(),
			Kind:         sv.GetKind(),
			Entrypoint:   sv.GetEntrypoint(),
			Name:         sv.GetName(),
			Branch:       sv.GetBranch(),
			TerminalHost: sv.GetTerminalHost(),
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			ContextTokens: int(sv.GetContextTokens()),
			Model:         sv.GetModel(),
			FirstPrompt:   sv.GetFirstPrompt(),
			SubagentCount: int(sv.GetSubagentCount()),
			SubshellCount: int(sv.GetSubshellCount()),
			SessionTokens: int(sv.GetSessionTokens()),
			BurnRateShort: sv.GetBurnRateShort(),
			BurnRateLong:  sv.GetBurnRateLong(),
			CostUSD:       sv.GetCostUsd(),
			AwaitingInput: sv.GetAwaitingInput(),
		},
	}
	out.StartedAt = timeFromTS(sv.GetStartedAt())
	out.TranscriptMTime = timeFromTS(sv.GetTranscriptMtime())
	out.RateLimitResetsAt = timeFromTS(sv.GetRateLimitResetsAt())
	out.LastNudgedAt = timeFromTS(sv.GetLastNudgedAt())
	out.LastNudgeSources = sv.GetLastNudgeSources()
	out.Status = statusFromString(sv.GetStatus())
	// Reconstruct env subset so clients can filter by workspace.
	env := map[string]string{}
	if v := sv.GetCmuxWorkspaceId(); v != "" {
		env["CMUX_WORKSPACE_ID"] = v
	}
	if v := sv.GetTmuxSession(); v != "" {
		env["TMUX"] = v
	}
	if v := sv.GetGcRig(); v != "" {
		env["GC_RIG"] = v
	}
	if v := sv.GetGcAgent(); v != "" {
		env["GC_AGENT"] = v
	}
	if v := sv.GetWorkspaceEnv(); v != "" {
		env["WORKSPACE"] = v
	}
	if len(env) > 0 {
		out.Session.Env = env
	}
	return out
}

func blockFromProto(b *Block) *ccusage.Block {
	if b == nil {
		return nil
	}
	out := &ccusage.Block{
		StartTime: timeFromTS(b.GetStartTime()),
		EndTime:   timeFromTS(b.GetEndTime()),
		IsActive:  b.GetIsActive(),
		CostUSD:   b.GetCostUsd(),
		BurnRate: ccusage.BurnRate{
			TokensPerMinute: b.GetTokensPerMinute(),
			CostPerHour:     b.GetCostPerHour(),
		},
		Projection: ccusage.Projection{
			TotalCost:        b.GetProjectionTotalCost(),
			RemainingMinutes: int(b.GetProjectionRemainingMinutes()),
		},
	}
	if ts := b.GetCapHitAt(); ts != nil {
		t := timeFromTS(ts)
		out.CapHitAt = &t
	}
	return out
}

func weekFromProto(w *Week) *ccusage.WeeklyEntry {
	if w == nil {
		return nil
	}
	out := &ccusage.WeeklyEntry{
		Period:    w.GetPeriod(),
		TotalCost: w.GetCostUsd(),
	}
	if ts := w.GetCapHitAt(); ts != nil {
		t := timeFromTS(ts)
		out.CapHitAt = &t
	}
	return out
}

func statusFromString(s string) session.Status {
	switch s {
	case "working":
		return session.Working
	case "idle":
		return session.Idle
	default:
		return session.Dormant
	}
}

// timeFromTS converts a timestamppb proto to time.Time, returning zero
// when the input is nil.
//
// IMPORTANT: the parameter must be the concrete *timestamppb.Timestamp,
// not an interface, to detect the unset case correctly. An unset proto
// timestamp field arrives here as a typed-nil *timestamppb.Timestamp.
// If we accepted an interface (interface{ AsTime() time.Time }), the
// `ts == nil` check would be false for that typed nil (Go interfaces
// only equal nil when BOTH the type and value are nil), and we'd then
// call AsTime() on a nil pointer -- which timestamppb implements as a
// non-zero "return the epoch (1970-01-01)" value. The downstream
// IsZero() check then returns false, so EVERY unset timestamp was being
// translated into a non-zero "1970" reading. This bit the rateLimited
// branch in render/tree.go: every session showed up as paused even
// when the daemon never set RateLimitResetsAt.
func timeFromTS(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// SessionDetailToView converts a SessionDetail proto back to an
// aggregate.SessionView, populating LastError and PendingNudge from
// the detail-level fields. The base session fields are derived from
// the embedded SessionView.
func SessionDetailToView(sd *SessionDetail) *aggregate.SessionView {
	if sd == nil {
		return nil
	}
	sv := sessionViewFromProto(sd.GetView())
	if sv == nil {
		sv = &aggregate.SessionView{Session: &session.Session{}}
	}
	if e := sd.GetLastError(); e != nil {
		sv.LastError = apiErrorFromProto(e)
		// The escalation-aware retryable verdict rides the proto's is_retryable
		// field; it now lives on the SessionView, not the shared record.
		sv.LastErrorRetryable = e.GetIsRetryable()
	}
	if pn := sd.GetPendingNudge(); pn != nil {
		sv.PendingNudge = &aggregate.PendingNudge{Sources: pn.GetSources()}
	}
	return sv
}

func apiErrorFromProto(e *ApiError) *transcript.ErrorRecord {
	if e == nil {
		return nil
	}
	return &transcript.ErrorRecord{
		Kind:         transcript.ErrorKind(e.GetKind()),
		Text:         e.GetText(),
		At:           timeFromTS(e.GetAt()),
		IsTerminal:   e.GetIsTerminal(),
		FromSubagent: e.GetFromSubagent(),
	}
}
